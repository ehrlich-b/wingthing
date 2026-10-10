#!/usr/bin/env python3
"""Exercise a checkout-built wt; stdout is bounded JSONL, state is disposable."""
import argparse
import collections
import contextlib
import datetime
import hashlib
import json
import os
import pathlib
import queue
import shutil
import signal
import socket
import stat
import subprocess
import sys
import tempfile
import threading
import time

REPO = pathlib.Path(__file__).resolve().parents[2]
TIMEOUT = 45


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def wait_for(predicate, description, timeout=TIMEOUT):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        value = predicate()
        if value:
            return value
        time.sleep(0.05)
    raise TimeoutError(description)


class Process:
    def __init__(self, args, env, cwd, rpc=False):
        self.proc = subprocess.Popen(args, env=env, cwd=cwd, stdin=subprocess.PIPE,
                                     stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                     start_new_session=True, bufsize=0)
        self.tail = collections.deque(maxlen=64)
        self.replies = queue.Queue(maxsize=256)
        self.threads = []
        for stream, protocol in ((self.proc.stdout, rpc), (self.proc.stderr, False)):
            thread = threading.Thread(target=self.drain, args=(stream, protocol), daemon=True)
            thread.start()
            self.threads.append(thread)
        self.sequence = 0

    def drain(self, stream, protocol):
        try:
            if protocol:
                while True:
                    line = stream.readline(4 * 1024 * 1024 + 1)
                    if not line:
                        break
                    require(len(line) <= 4 * 1024 * 1024, "oversized MCP response")
                    self.replies.put(json.loads(line), timeout=1)
            else:
                while True:
                    chunk = stream.read(1024)
                    if not chunk:
                        break
                    self.tail.append(chunk)
        except Exception as error:
            if protocol:
                with contextlib.suppress(queue.Full):
                    self.replies.put(error, timeout=1)
        finally:
            if protocol:
                with contextlib.suppress(queue.Full):
                    self.replies.put(EOFError("MCP stdout closed"), timeout=1)

    def diagnostics(self):
        return b"".join(list(self.tail)).decode(errors="replace")[-4096:]

    def rpc(self, method, params, timeout=TIMEOUT):
        self.sequence += 1
        ident = self.sequence
        self.proc.stdin.write((json.dumps({"jsonrpc": "2.0", "id": ident,
                                          "method": method, "params": params}) + "\n").encode())
        deadline = time.monotonic() + timeout
        while True:
            try:
                response = self.replies.get(timeout=max(0, deadline - time.monotonic()))
            except queue.Empty:
                raise TimeoutError("MCP " + method)
            if isinstance(response, Exception):
                raise RuntimeError(str(response) + ": " + self.diagnostics())
            if response.get("id") != ident:
                continue
            require("error" not in response, "MCP " + method + ": " + json.dumps(response.get("error")))
            return response["result"]

    def tool(self, name, arguments=None, timeout=TIMEOUT):
        result = self.rpc("tools/call", {"name": name, "arguments": arguments or {}}, timeout)
        require(not result.get("isError"), name + ": " + json.dumps(result))
        return result["structuredContent"]

    def close(self, abrupt=False):
        if self.proc.poll() is None:
            if abrupt:
                self.proc.kill()
            else:
                self.proc.stdin.close()
            try:
                self.proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.proc.kill()
                self.proc.wait(timeout=5)
        for thread in self.threads:
            thread.join(timeout=1)
        for stream in (self.proc.stdin, self.proc.stdout, self.proc.stderr):
            stream.close()


class Gate:
    def __init__(self, work):
        self.path = work / ".fixture-completion-gate"
        os.mkfifo(self.path, 0o600)
        self.fd = os.open(self.path, os.O_RDWR)

    def ready(self):
        wait_for(lambda: pathlib.Path(str(self.path) + ".ready").exists(), "fake Codex native readiness")

    def release(self):
        self.ready()
        os.write(self.fd, b"\x01")


class Canary:
    def __init__(self, args):
        self.args = args
        self.failed = False
        self.processes = []
        self.states = []
        self.gates = []
        self.sockets = []
        self.egg_pids = set()
        scratch = REPO / ".scratch"
        scratch.mkdir(mode=0o700, exist_ok=True)
        self.root = pathlib.Path(tempfile.mkdtemp(prefix="d", dir=scratch))
        self.home, self.bin, self.work, self.tmp = (self.root / x for x in ("h", "b", "w", "t"))
        for path in (self.home, self.bin, self.work, self.tmp):
            path.mkdir(mode=0o700)
        # Copy into the fixture home so a sandboxed parent can execute its client.
        self.binary = self.home / "wt"
        shutil.copyfile(args.wt, self.binary)
        self.binary.chmod(0o700)
        self.binary_hash = hashlib.sha256(self.binary.read_bytes()).hexdigest()
        self.env = {"HOME": str(self.home), "WINGTHING_DIR": str(self.root / "s"),
                    "PATH": str(self.bin) + os.pathsep + os.environ.get("PATH", "/bin:/usr/bin"),
                    "SHELL": "/bin/sh", "TMPDIR": str(self.tmp),
                    "WT_MCP_CLIENT": "", "WT_CONVERSATION_EXECUTION_ID": "",
                    "WT_FAKE_SSH_ROOT": str(self.root / "ssh"), "WT_FAKE_SSH_EXECUTE": "1"}
        for provider in ("codex", "scoped_claude"):
            script = (REPO / "internal/testprovider" / (provider + ".py")).read_text()
            target = self.bin / ("claude" if provider == "scoped_claude" else provider)
            target.write_text("#!" + sys.executable + "\n" + script)
            target.chmod(0o700)
        self.state = self.new_state("s")
        self.mcp = None
        self.wing = None

    def emit(self, name, status, started, error=None, **extra):
        record = {"check": name, "status": status,
                  "latency_ms": round((time.monotonic() - started) * 1000, 2),
                  "error": str(error)[:4096] if error else None,
                  "timestamp": datetime.datetime.now(datetime.timezone.utc).isoformat(),
                  "platform": sys.platform, "binary_sha256": self.binary_hash, **extra}
        print(json.dumps(record, ensure_ascii=False), flush=True)
        if status == "fail":
            self.failed = True

    def check(self, name, action):
        started = time.monotonic()
        try:
            detail = action() or {}
            status = detail.pop("status_override", "pass")
            if status == "skip" and self.args.require_all:
                raise RuntimeError(detail.get("reason", "required check unavailable"))
            self.emit(name, status, started, **detail)
            return True
        except Exception as error:
            self.emit(name, "fail", started, error)
            return False

    def new_state(self, name):
        state = self.root / name
        state.mkdir(mode=0o700)
        (state / "wing.yaml").write_text('allow_unsandboxed: true\nroost: "http://127.0.0.1:1"\n')
        (state / "wing.yaml").chmod(0o600)
        self.states.append(state)
        return state

    def environment(self, state=None):
        return dict(self.env, WINGTHING_DIR=str(state or self.state))

    def process(self, arguments, state=None, cwd=None, rpc=False):
        proc = Process([str(self.binary)] + arguments, self.environment(state), cwd or self.work, rpc)
        self.processes.append(proc)
        return proc

    def command(self, arguments, state=None, env=None):
        result = subprocess.run([str(self.binary)] + arguments, env=env or self.environment(state),
                                cwd=self.work, capture_output=True, timeout=TIMEOUT)
        require(result.returncode == 0, result.stderr.decode(errors="replace")[-4096:])
        return result.stdout

    def start_wing(self, state=None, work=None):
        proc = self.process(["wing", "start", "--local-only", "--foreground", "--paths", str(work or self.work)], state)
        def ready():
            require(proc.proc.poll() is None, "wing exited: " + proc.diagnostics())
            return "wing: local control ready" in proc.diagnostics()
        wait_for(ready, "local-only wing startup")
        return proc

    def stop_wing(self, abrupt=False):
        # Foreground wings have no daemon PID file. Signal the exact owned child.
        self.wing.proc.send_signal(signal.SIGKILL if abrupt else signal.SIGINT)
        code = self.wing.proc.wait(timeout=TIMEOUT)
        for thread in self.wing.threads:
            thread.join(timeout=1)
        expected = (-signal.SIGKILL,) if abrupt else (0, 1, -signal.SIGINT)
        require(code in expected,
                "wing shutdown failed (exit " + str(code) + "): " + self.wing.diagnostics())

    def client(self, state=None, connect=False):
        proc = self.process(["mcp", "connect" if connect else "stdio", "--unsandboxed"], state, rpc=True)
        initialized = proc.rpc("initialize", {"protocolVersion": "2025-11-25", "capabilities": {},
                                              "clientInfo": {"name": "dogfood-canary", "version": "1"}})
        require(initialized["protocolVersion"] == "2025-11-25", "Tasks protocol not negotiated")
        require("tasks" in initialized["capabilities"], "Tasks capability missing")
        proc.proc.stdin.write(b'{"jsonrpc":"2.0","method":"notifications/initialized"}\n')
        return proc

    def gate(self, work):
        gate = Gate(work)
        self.gates.append(gate)
        return gate

    def launch(self, name, task=False):
        work = self.work / name
        work.mkdir(mode=0o700)
        gate = self.gate(work)
        arguments = {"agent": "codex", "model": "fixture-model", "prompt": name,
                     "cwd": str(work), "timeout_seconds": 120, "idempotency_key": name}
        if task:
            result = self.mcp.rpc("tools/call", {"name": "agent_run", "arguments": arguments, "task": {"ttl": 60000}})
            require(not result.get("isError") and result["task"]["status"] == "working", "task admission failed")
            ident = result["task"]["taskId"]
        else:
            result = self.mcp.tool("agent_run", arguments)
            require(result["cwd"] == str(work), "subdirectory cwd changed (#19)")
            ident = result["run_id"]
        gate.ready()
        status = self.mcp.tool("agent_status", {"run_id": ident})
        require(status["status"] in ("running", "pending"), "run ended before gate release")
        return ident, status["session_id"], gate

    def outcome(self, ident, name):
        waited = self.mcp.tool("agent_wait", {"run_id": ident, "timeout_seconds": 30})
        require(waited["status"] == "done", "agent_wait: " + json.dumps(waited))
        result = self.mcp.tool("agent_result", {"run_id": ident})
        require(result["output"] == "Fake Codex fixture-model: Ω🙂 " + name, "native output mismatch")
        require(result["run_id"] == ident and result["turn_id"] == "fake-turn", "run identity changed")
        return result

    def local_start(self):
        self.wing = self.start_wing()
        self.mcp = self.client()
        self.mcp.tool("wingthing_capabilities")
        wings = self.mcp.tool("wing_list")["wings"]
        require(len(wings) == 1, "local wing directory mismatch")
        self.wing_id = wings[0]["wing_id"]
        require(not any((self.state / x).exists() for x in ("device_token.yaml", "local_device_token.yaml")), "local-only wrote relay credentials")

    def daemon_boot(self):
        state = self.new_state("daemon")
        output = self.command(["wing", "start", "--local-only", "--paths", str(self.work)], state)
        self.egg_pids.add(int((state / "wing.pid").read_text()))
        require(b"local control: ready" in output, "daemon startup did not acknowledge control readiness")
        client = self.client(state)
        client.tool("wingthing_capabilities")
        require(len(client.tool("wing_list")["wings"]) == 1, "daemon wing directory mismatch")
        client.close()
        require(not any((state / x).exists() for x in ("device_token.yaml", "local_device_token.yaml")), "daemon wrote relay credentials")
        self.command(["wing", "stop"], state)

    def terminal(self):
        session = self.mcp.tool("terminal_start", {"command": ["/bin/sh"], "cwd": str(self.work)})["session"]
        self.egg_pids.add(int((self.state / "eggs" / session / "egg.pid").read_text()))
        self.mcp.tool("terminal_send", {"session": session, "input": "printf 'WT_TERMINAL_CANARY_OK\\n'", "enter": True})
        self.mcp.tool("terminal_wait", {"session": session, "contains": "WT_TERMINAL_CANARY_OK", "timeout_seconds": 10})
        self.mcp.tool("terminal_stop", {"session": session})
        wait_for(lambda: not (self.state / "eggs" / session / "egg.pid").exists(), "terminal egg shutdown")

    def stdio(self):
        ident, _, gate = self.launch("stdio")
        gate.release()
        self.outcome(ident, "stdio")

    def disconnect(self):
        ident, session, gate = self.launch("disconnect")
        self.mcp.close(abrupt=True)
        self.mcp = self.client()
        require(self.mcp.tool("agent_status", {"run_id": ident})["session_id"] == session, "client disconnect changed session")
        gate.release()
        self.outcome(ident, "disconnect")

    def restart(self):
        ident, session, gate = self.launch("restart")
        self.mcp.close()
        owner_path = self.state / "eggs" / session / "egg.owner"
        owner_before = owner_path.read_bytes() if owner_path.exists() else None
        self.stop_wing(abrupt=True)
        self.wing = self.start_wing()
        self.mcp = self.client()
        status = self.mcp.tool("agent_status", {"run_id": ident})
        require(status["session_id"] == session and status["wing_id"] == self.wing_id, "wing restart changed durable identity")
        require(owner_before is not None and owner_path.read_bytes() == owner_before, "wing restart changed ownership")
        gate.release()
        self.outcome(ident, "restart")

    def tasks(self):
        ident, _, gate = self.launch("tasks", task=True)
        require(self.mcp.rpc("tasks/get", {"taskId": ident})["status"] == "working", "tasks/get lost active task")
        listing = self.mcp.rpc("tasks/list", {})
        require(any(x["taskId"] == ident for x in listing["tasks"]), "tasks/list lost active task")
        # Replace both client and wing while a native task is still working.
        self.mcp.close(abrupt=True)
        self.stop_wing()
        self.wing = self.start_wing()
        self.mcp = self.client()
        require(self.mcp.rpc("tasks/get", {"taskId": ident})["status"] == "working", "restart lost native task")
        gate.release()
        result = self.mcp.rpc("tasks/result", {"taskId": ident})
        require(result["structuredContent"] == self.outcome(ident, "tasks"), "task/agent results differ")
        require(result["_meta"]["io.modelcontextprotocol/related-task"]["taskId"] == ident, "task correlation lost")
        require(self.mcp.rpc("tasks/get", {"taskId": ident})["status"] == "completed", "task did not complete")
        cancelled, _, _ = self.launch("cancel", task=True)
        require(self.mcp.rpc("tasks/cancel", {"taskId": cancelled})["status"] == "cancelled", "cancel did not persist")
        self.mcp.close()
        self.stop_wing(abrupt=True)
        self.wing = self.start_wing()
        self.mcp = self.client()
        require(self.mcp.rpc("tasks/get", {"taskId": cancelled})["status"] == "cancelled", "wing restart lost cancellation")
        result = self.mcp.rpc("tasks/result", {"taskId": cancelled})
        require(result["structuredContent"]["status"] == "stopped", "cancelled task returned a successful run")
        require(result["_meta"]["io.modelcontextprotocol/related-task"]["taskId"] == cancelled, "cancelled result correlation lost")

    def fake_ssh(self, remote_state):
        # Reuse the repository's fake OpenSSH transport. Never contact a host.
        root = self.root / "ssh"
        root.mkdir(mode=0o700)
        metadata = json.loads(self.command(["mcp", "inspect"], remote_state))
        (root / "forge.json").write_text(json.dumps(metadata))
        listener = socket.socket(socket.AF_UNIX)
        listener.bind(str(root / "supervisor.sock"))
        listener.listen()
        self.sockets.append(listener)
        def supervise():
            while True:
                try:
                    connection, _ = listener.accept()
                except OSError:
                    return
                self.sockets.append(connection)
                # Holding this fd is the explicit lifetime gate of the fake.
        threading.Thread(target=supervise, daemon=True).start()
        script = (REPO / "internal/testssh/ssh.py").read_text()
        (self.bin / "ssh").write_text("#!" + sys.executable + "\n" + script)
        (self.bin / "ssh").chmod(0o700)
        self.env.update(WT_FAKE_SSH_ROOT=str(root), WT_FAKE_SSH_EXECUTE="1")
        self.command(["mcp", "connect", "add", "forge", "--ssh", "forge",
                      "--wingthing-dir", str(remote_state), "--wt-binary", str(self.binary)])
        return metadata

    def remembered_fixture(self):
        self.remote_state = self.new_state("r")
        self.remote_work = self.root / "rw"
        self.remote_work.mkdir(mode=0o700)
        self.start_wing(self.remote_state, self.remote_work)
        metadata = self.fake_ssh(self.remote_state)
        proc = self.client(connect=True)
        wings = proc.tool("wing_list")["wings"]
        require(any(w["wing_id"] == metadata["wing_id"] for w in wings), "fake remembered wing missing")
        work = self.remote_work / "ssh-run"
        work.mkdir(mode=0o700)
        gate = self.gate(work)
        receipt = proc.tool("agent_run", {"wing_id": metadata["wing_id"], "agent": "codex", "model": "fixture-model",
                                           "cwd": str(work), "prompt": "ssh request", "timeout_seconds": 120})
        gate.ready()
        proc.close(abrupt=True)
        proc = self.client(connect=True)
        gate.release()
        proc.tool("agent_wait", {"wing_id": metadata["wing_id"], "run_id": receipt["run_id"], "timeout_seconds": 30})
        result = proc.tool("agent_result", {"wing_id": metadata["wing_id"], "run_id": receipt["run_id"]})
        require(result["status"] == "done" and result["output"] == "Fake Codex fixture-model: Ω🙂 ssh request", "fake SSH result mismatch")
        require(all(result[x] == receipt[x] for x in ("run_id", "session_id", "wing_id")), "fake SSH changed child identity")
        proc.close()

    def mailbox(self):
        # The protected-write boundary is deliberately unavailable on Linux.
        if sys.platform != "darwin":
            return {"status_override": "skip", "reason": "Linux protected-write sandbox cannot enforce wt claude's scoped parent boundary; see GAPS.md"}
        probe = subprocess.run(["/usr/bin/sandbox-exec", "-p", "(version 1)(allow default)", "/usr/bin/true"], capture_output=True)
        if probe.returncode and b"sandbox_apply: Operation not permitted" in probe.stderr:
            return {"status_override": "skip", "reason": "host forbids nested sandbox-exec; native mailbox acceptance unavailable"}
        require(probe.returncode == 0, "sandbox preflight failed")
        require(hasattr(self, "remote_work"), "blocked by remembered SSH fixture")
        local_gate, remote_gate = self.gate(self.work), self.gate(self.remote_work)
        key = self.home / ".ssh/id_ed25519"
        key.parent.mkdir(mode=0o700)
        key.write_text("fake fixture key; not a credential")
        key.chmod(0o600)
        control = json.loads(self.command(["mcp", "inspect"]))["control_socket"]
        viewer = self.process(["claude", "--name", "dogfood-parent", "--", "--model", "fake-parent",
                               "--fixture-control", control, "--fixture-state-file", str(self.state / "wing.yaml"),
                               "--fixture-ssh-key", str(key)])
        path = self.work / "receipts.json"
        def parent_ready():
            require(viewer.proc.poll() is None, "wt claude exited: " + viewer.diagnostics())
            return path.exists()
        wait_for(parent_ready, "scoped Claude receipts")
        captured = json.loads(path.read_text())
        require(len(captured["receipts"]) == 2, "parent did not admit both children")
        local_gate.ready()
        remote_gate.ready()
        # Disconnect the viewer, then replace the mailbox MCP subprocess.
        viewer.close(abrupt=True)
        session = captured["parent_session_id"]
        self.mcp.tool("terminal_send", {"session": session, "input": "reconnect", "enter": True})
        reconnect = self.work / "mailbox-reconnected.json"
        wait_for(reconnect.exists, "parent mailbox reconnect")
        require(json.loads(reconnect.read_text())["pid"] != captured["mailbox_pid"], "mailbox did not replace client")
        local_gate.release()
        remote_gate.release()
        self.mcp.tool("terminal_send", {"session": session, "input": "recover", "enter": True})
        results_path = self.work / "results.json"
        wait_for(results_path.exists, "parent mailbox results")
        results = json.loads(results_path.read_text())
        require(len(results) == len(captured["receipts"]), "parent lost a child result")
        for receipt, result in zip(captured["receipts"], results):
            require(result["status"] == "done" and result["ready"] is True, "child not complete")
            require(all(result[x] == receipt[x] for x in ("run_id", "session_id", "wing_id")), "mailbox changed child identity")
        self.mcp.tool("terminal_stop", {"session": session})

    def remembered_ssh(self):
        if not self.args.ssh_target:
            return {"status_override": "skip", "reason": "no SSH target configured; fake transport has its own check"}
        # Read-only acceptance of an existing personal wing; no remote starts/runs.
        env = self.environment()
        env["PATH"] = os.environ.get("PATH", "/bin:/usr/bin")
        env["HOME"] = str(pathlib.Path.home())
        if "SSH_AUTH_SOCK" in os.environ:
            env["SSH_AUTH_SOCK"] = os.environ["SSH_AUTH_SOCK"]
        self.command(["mcp", "connect", "add", "live-canary", "--ssh", self.args.ssh_target,
                      "--wingthing-dir", self.args.ssh_wingthing_dir, "--wt-binary", self.args.ssh_wt_binary], env=env)
        proc = Process([str(self.binary), "mcp", "connect"], env, self.work, rpc=True)
        self.processes.append(proc)
        proc.rpc("initialize", {"protocolVersion": "2025-11-25", "capabilities": {},
                                "clientInfo": {"name": "dogfood-ssh-canary", "version": "1"}})
        wings = proc.tool("wing_list")["wings"]
        remote = next((w for w in wings if w.get("name") == "live-canary"), None)
        require(remote is not None, "remembered SSH wing missing")
        proc.tool("wingthing_capabilities", {"wing_id": remote["wing_id"]})
        proc.close()

    def real_codex(self):
        if not self.args.real_codex:
            return {"status_override": "skip", "reason": "real Codex is opt-in (--real-codex)"}
        codex = shutil.which("codex")
        require(codex is not None, "codex is not on PATH")
        source = pathlib.Path(os.environ.get("CODEX_HOME", str(pathlib.Path.home() / ".codex"))) / "auth.json"
        require(stat.S_ISREG(source.lstat().st_mode), "existing file-backed Codex login required")
        profile = self.home / ".codex"
        profile.mkdir(mode=0o700, exist_ok=True)
        shutil.copyfile(source, profile / "auth.json")
        (profile / "auth.json").chmod(0o600)
        (profile / "config.toml").write_text('features.plugins = false\nweb_search = "disabled"\n')
        real_bin = self.root / "real-bin"
        real_bin.mkdir(mode=0o700)
        (real_bin / "codex").symlink_to(codex)
        # A separate wing captures the real provider PATH at startup.
        state = self.new_state("live")
        old_env = self.env.copy()
        self.env.update(PATH=str(real_bin) + os.pathsep + os.environ.get("PATH", ""), CODEX_HOME=str(profile))
        try:
            self.start_wing(state)
            client = self.client(state)
            prompt = "Do not use tools, read files, browse, or run commands. Reply with exactly WT_CODEX_CANARY_OK."
            receipt = client.tool("agent_run", {"agent": "codex", "model": self.args.model, "cwd": str(self.work),
                                                 "prompt": prompt, "timeout_seconds": 180})
            client.close(abrupt=True)
            client = self.client(state)
            client.tool("agent_wait", {"run_id": receipt["run_id"], "timeout_seconds": 210}, timeout=240)
            result = client.tool("agent_result", {"run_id": receipt["run_id"]})
            require(result["status"] == "done" and result.get("output", "").strip() == "WT_CODEX_CANARY_OK", "real Codex did not return exact native completion")
            client.close()
        finally:
            self.env = old_env

    def cleanup(self):
        # Stop via authenticated egg endpoints while each private wing is alive.
        for state in self.states:
            for marker in (state / "eggs").glob("*/egg.pid"):
                self.egg_pids.add(int(marker.read_text()))
                with contextlib.suppress(Exception):
                    self.command(["egg", "stop", marker.parent.name], state)
            if (state / "wing.pid").exists():
                with contextlib.suppress(Exception):
                    self.command(["wing", "stop"], state)
        for proc in reversed(self.processes):
            if proc.proc.poll() is None:
                proc.proc.send_signal(signal.SIGINT)
            proc.close()
        for connection in self.sockets:
            connection.close()
        for gate in self.gates:
            os.close(gate.fd)
        # Never delete an unverified live egg's metadata. Failed cleanup is a gate.
        def eggs_gone():
            return not any(list((state / "eggs").glob("*/egg.pid")) for state in self.states)
        wait_for(eggs_gone, "cleanup left egg PID markers; retained " + str(self.root), timeout=10)
        def exited(pid):
            try:
                os.kill(pid, 0)
                return False
            except ProcessLookupError:
                return True
        wait_for(lambda: all(exited(pid) for pid in self.egg_pids), "cleanup left live egg processes; retained " + str(self.root), timeout=10)
        require(not any((state / "wing.pid").exists() for state in self.states), "cleanup left a daemon PID marker")
        inventory = "verified"
        try:
            listing = subprocess.run(["/bin/ps", "-axo", "command="], capture_output=True, text=True, timeout=5)
            require(listing.returncode == 0, "process inventory failed")
            require(str(self.root) not in listing.stdout, "cleanup left fixture descendants; retained " + str(self.root))
        except PermissionError:
            inventory = "unavailable: host denies ps; egg PIDs/endpoints and direct child exits verified"
        shutil.rmtree(self.root)
        return {"descendant_inventory": inventory}

    def run(self):
        checks = [("local_only_start", self.local_start), ("local_only_daemon_boot", self.daemon_boot),
                  ("terminal_start_send_stop", self.terminal),
                  ("mcp_stdio_agent_run_wait_result", self.stdio), ("mcp_client_disconnect", self.disconnect),
                  ("wing_restart_mid_run", self.restart), ("mcp_tasks_get_result_list_cancel", self.tasks),
                  ("remembered_ssh_fixture", self.remembered_fixture),
                  ("wt_claude_scoped_mailbox", self.mailbox), ("remembered_ssh_connect", self.remembered_ssh),
                  ("real_codex", self.real_codex)]
        try:
            for name, action in checks:
                if not self.check(name, action) and name == "local_only_start":
                    for other, _ in checks[1:]:
                        self.emit(other, "fail", time.monotonic(), "blocked by local wing startup")
                    break
        finally:
            self.check("cleanup", self.cleanup)
        return 1 if self.failed else 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--wt", type=pathlib.Path, default=REPO / "wt", help="explicit checkout-built binary (never installed wt)")
    parser.add_argument("--real-codex", action="store_true", help="use a private copy of an existing file-backed login for one no-tools prompt")
    parser.add_argument("--model", default="gpt-5.6-luna")
    parser.add_argument("--require-all", action="store_true", help="treat unavailable or unconfigured checks as failures")
    parser.add_argument("--ssh-target", help="optional existing personal SSH wing (read-only check)")
    parser.add_argument("--ssh-wingthing-dir")
    parser.add_argument("--ssh-wt-binary")
    args = parser.parse_args()
    args.wt = args.wt.resolve()
    require(args.wt.is_file() and args.wt.is_relative_to(REPO), "--wt must be a built binary inside this checkout")
    require(not args.ssh_target or (args.ssh_wingthing_dir and args.ssh_wt_binary), "SSH target requires --ssh-wingthing-dir and --ssh-wt-binary")
    os.umask(0o077)
    def interrupted(signum, frame):
        raise KeyboardInterrupt("interrupted by signal " + str(signum))
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    return Canary(args).run()


if __name__ == "__main__":
    sys.exit(main())
