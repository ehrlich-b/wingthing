#!/usr/bin/env python3
"""Black-box --remote-state proof over a fake SSH transport.

Only OpenSSH is replaced. The stable and preview clients, the receiving
executables, the egg sandbox, private Unix endpoints, replay, attach/detach,
and lifecycle are real. Eggs run harmless owned /bin/sh scripts. Nothing
contacts a host, reads credentials, installs a binary, or runs unsandboxed.

usage: remote_isolation.py STABLE_WT PREVIEW_WT [RECEIPT_PATH]
"""
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time

stable_client, preview_client = [Path(p).resolve() for p in sys.argv[1:3]]
receipt_path = Path(sys.argv[3]) if len(sys.argv) > 3 else None
HOST = "fixture-host"
UNKNOWN = "Session state is unknown; reconnect with the same --remote-state and list sessions before relaunching"
ENVIRONMENT_MARKERS = ("Operation not permitted", "sandbox_apply", "sandbox-exec", "EnforcementError", "bind:", "permission denied")


class FixtureFailure(Exception):
    def __init__(self, step, detail):
        super().__init__(f"{step}: {detail}")
        self.step, self.detail = step, detail


def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def shell_quote(value):
    # Mirrors cmd/wt/remote.go shellQuote, which always quotes.
    return "'" + value.replace("'", "'\"'\"'") + "'"


def tree(root, skip_logs=True):
    """Names, types, and content hashes; sockets and logs only by presence."""
    root = Path(root)
    result = {}
    for path in sorted(root.rglob("*")):
        relative = str(path.relative_to(root))
        if path.is_symlink():
            result[relative] = "link:" + os.readlink(path)
        elif path.is_dir():
            result[relative] = "dir"
        elif path.is_socket() or (skip_logs and path.suffix == ".log"):
            result[relative] = "present"
        elif path.is_file():
            result[relative] = digest(path)
    return result


base = Path(tempfile.mkdtemp(prefix="wtr-", dir="/tmp"))
client_home = base / "client"
remote_home = base / "rh"
remote_bin = base / "rbin"
fixture_bin = base / "bin"
workspace = base / "remote work"
# Isolated state names carry spaces and an apostrophe through the remote shell.
state = str(base / "r st" / "it's pv")
transcript = base / "ssh-transcript"
for directory in [client_home, remote_home, remote_bin, fixture_bin, workspace]:
    directory.mkdir(parents=True)

stable_state = remote_home / ".wingthing"
stable_state.mkdir()
sentinel = stable_state / "stable-sentinel"
sentinel.write_text("stable-state-must-survive\n")
shutil.copy2(stable_client, remote_bin / "wt")
shutil.copy2(preview_client, remote_bin / "wt-preview")
binary_hashes = {name: digest(remote_bin / name) for name in ["wt", "wt-preview"]}
client_hashes = {"stable": digest(stable_client), "preview": digest(preview_client)}
sentinel_hash = digest(sentinel)

egg_script = workspace / "fixture egg.sh"
egg_script.write_text("#!/bin/sh\nprintf 'fixture-ready:%s\\n' \"$1\"\nwhile IFS= read -r line; do printf 'fixture-result:%s:%s\\n' \"$1\" \"$line\"; done\n")
egg_script.chmod(0o700)

# The fake transport starts each remote command like sshd: a fresh login-ish
# environment for the remote owner, then the caller's single command string
# through /bin/sh. It records the exact string it received.
ssh = fixture_bin / "ssh"
ssh.write_text("""#!/bin/sh
[ "$#" -eq 3 ] || { echo "fixture ssh: unexpected argv count $#" >&2; exit 90; }
case "$1" in -T|-t) ;; *) echo "fixture ssh: unexpected mode $1" >&2; exit 91;; esac
[ "$2" = fixture-host ] || { echo "fixture ssh: unexpected host $2" >&2; exit 92; }
printf '%s\\037%s\\037%s\\036' "$1" "$2" "$3" >> "$WT_FIXTURE_TRANSCRIPT"
if [ "$WT_FIXTURE_DISCONNECT" = before ]; then echo 'fixture connection lost' >&2; exit 255; fi
cd "$WT_FIXTURE_HOME" || exit 93
env -i HOME="$WT_FIXTURE_HOME" PATH="$WT_FIXTURE_PATH" USER="${USER-}" LOGNAME="${LOGNAME-}" SHELL=/bin/sh /bin/sh -c "$3"
status=$?
if [ "$WT_FIXTURE_DISCONNECT" = after ]; then echo 'fixture connection lost after remote command' >&2; exit 255; fi
exit $status
""")
ssh.chmod(0o700)

client_env = {k: v for k, v in os.environ.items() if not k.startswith(("WT_", "WINGTHING", "FLY_", "SSH_")) and k not in {"GITHUB_CLIENT_ID", "GITHUB_CLIENT_SECRET", "GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_SECRET", "SMTP_HOST", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"}}
client_env.update(
    HOME=str(client_home),
    PATH=str(fixture_bin) + os.pathsep + client_env.get("PATH", "/usr/bin:/bin"),
    WT_FIXTURE_TRANSCRIPT=str(transcript),
    WT_FIXTURE_HOME=str(remote_home),
    WT_FIXTURE_PATH=str(remote_bin) + ":/usr/bin:/bin",
)
remote_env = {"HOME": str(remote_home), "PATH": str(remote_bin) + ":/usr/bin:/bin"}

calls = []


def egg_logs():
    logs = {}
    for log in base.glob("**/eggs/*/egg.log"):
        logs[str(log.relative_to(base))] = log.read_text(errors="replace")[-2000:]
    return logs


def remote(client, *args, select=True, remote_binary=None, stdin="", ok=True, disconnect=None, step=None, timeout=25):
    argv = [str(client), "--remote", HOST]
    if remote_binary is not None:
        argv += ["--remote-binary", remote_binary]
    if select is True:
        argv += ["--remote-state", state]
    elif isinstance(select, str):
        argv += ["--remote-state", select]
    argv += list(args)
    env = dict(client_env)
    if disconnect:
        env["WT_FIXTURE_DISCONNECT"] = disconnect
    result = subprocess.run(argv, env=env, input=stdin, capture_output=True, text=True, timeout=timeout)
    calls.append({"step": step or " ".join(args[:2]), "argv": argv[1:], "rc": result.returncode})
    if ok is not None and (result.returncode == 0) != ok:
        raise FixtureFailure(step or " ".join(args), {"argv": argv, "rc": result.returncode, "stdout": result.stdout[-4000:], "stderr": result.stderr[-4000:], "egg_logs": egg_logs()})
    return result


def records():
    if not transcript.exists():
        return []
    raw = transcript.read_bytes().decode()
    return [record.split("\x1f") for record in raw.split("\x1e") if record]


def check(condition, step, detail):
    if not condition:
        raise FixtureFailure(step, detail)


def poll(step, predicate, timeout=10):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        last = predicate()
        if last:
            return last
        time.sleep(0.2)
    raise FixtureFailure(step, f"condition not met within {timeout}s; last={last!r}")


def sessions(client=preview_client, select=True):
    return json.loads(remote(client, "session", "ps", "--json", select=select, step="inventory").stdout or "[]") or []


def pid_gone(pid):
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return True
    except PermissionError:
        return False  # alive, owned by someone else: never ours to signal
    return False


def assert_absent(path, step):
    check(not Path(path).exists() and not Path(path).is_symlink(), step, f"{path} was created")


def expected_command(binary, *args, channel=None, select=None):
    """The exact remote command string cmd/wt/remote.go must send."""
    words = [binary] + (["--expected-channel", channel] if channel else []) + list(args)
    command = " ".join(shell_quote(word) for word in words)
    if select is not None:
        command = f"WINGTHING_DIR={shell_quote(select)} WINGTHING_PREVIEW_DIR={shell_quote(select)} {command}"
    return command


def last_record(step, mode, command):
    record = records()[-1]
    check(record == [mode, HOST, command], step, {"got": record, "want": [mode, HOST, command]})


def runtime_isolation(step, client, session_id, select=True):
    # egg.meta written by the running egg after it chose its sandbox, not the
    # launch response derived from configuration.
    def reported():
        entry = next((s for s in sessions(client, select=select) if s["id"] == session_id), None)
        return entry if entry and entry.get("isolation") else None
    entry = poll(step, reported)
    check(entry["isolation"] == "wingthing-sandbox", step, entry)
    return entry


def process_command(pid):
    result = subprocess.run(["ps", "-o", "command=", "-p", str(pid)], capture_output=True, text=True)
    return result.stdout.strip() if result.returncode == 0 else ""


def kill_owned_eggs():
    """Stop every egg under this fixture's temp tree, recorded or not.

    A refused launch that started anyway, or a launch whose JSON failed
    validation, still leaves an eggs/<id> directory. Each is stopped through
    the receiver for its state's channel, then any surviving egg process whose
    argv names that exact session is killed. Nothing outside base is touched.
    """
    eggs = []
    # os.walk does not follow symlinks, so the stable alias is visited once,
    # through the real stable state, and provider links cannot loop.
    for directory, subdirs, _ in os.walk(base):
        if Path(directory).name == "eggs":
            eggs += [Path(directory) / name for name in sorted(subdirs)]
            subdirs.clear()
    for egg in eggs:
        if egg.is_symlink():
            continue
        root = egg.parent.parent
        try:
            marker = (root / ".release-channel").read_text().strip()
        except OSError:
            marker = "stable"
        binary = remote_bin / ("wt-preview" if marker == "preview" else "wt")
        env = {**remote_env, "WINGTHING_DIR": str(root)}
        if marker == "preview":
            env["WINGTHING_PREVIEW_DIR"] = str(root)
        # Read the PID first: a successful kill removes egg.pid.
        try:
            pid = int((egg / "egg.pid").read_text().strip())
        except (OSError, ValueError):
            pid = 0
        try:
            subprocess.run([str(binary), "session", "kill", egg.name], env=env, capture_output=True, timeout=15)
        except subprocess.TimeoutExpired:
            pass
        deadline = time.monotonic() + 5
        while pid > 0 and not pid_gone(pid) and time.monotonic() < deadline:
            time.sleep(0.1)
        if pid > 0 and not pid_gone(pid) and f"--session-id {egg.name}" in process_command(pid):
            # The egg may exit between the check and the signal; that must not
            # abort cleanup of the remaining eggs.
            try:
                os.kill(pid, 9)
            except (ProcessLookupError, PermissionError):
                pass


preview_ids = {}
stable_session = None
checks = []
receipt = {"result": "failed", "remote_state": state}
try:
    # 1. Legacy route without --remote-state: a stable sentinel egg in the
    # remote owner's default stable state, and unchanged legacy argv.
    sentinel_args = ["terminal", "--cwd", str(workspace), "--detach", "--json", "--name", "stable-sentinel", "--", "/bin/sh", "-c", "printf 'stable-ready\\n'; exec /usr/bin/tail -f /dev/null"]
    started = json.loads(remote(stable_client, *sentinel_args, select=None, step="stable legacy launch").stdout)
    stable_session = started["session"]
    last_record("stable legacy argv", "-T", expected_command("wt", *sentinel_args))
    remote(stable_client, "session", "wait", stable_session, "--contains", "stable-ready", "--timeout", "10s", "--json", select=None, step="stable ready")
    # Never retried with --unsandboxed: the running egg reports its sandbox.
    stable_pid = runtime_isolation("stable no privilege fallback", stable_client, stable_session, select=None)["pid"]
    check(stable_pid > 0, "stable pid", stable_pid)
    os.kill(stable_pid, 0)
    check((stable_state / "eggs" / stable_session).is_dir(), "stable legacy state", "stable egg is not in the remote default stable state")
    checks.append("absent --remote-state keeps legacy stable argv and default stable state")

    # 2. Read-only channel inspection resolves the selected receiver lexically.
    identity = json.loads(remote(preview_client, "channel", "--json", step="preview channel").stdout)
    check(identity["release_channel"] == "preview" and identity["executable"] == "wt-preview" and identity["state_dir"] == state, "preview identity", identity)
    last_record("preview quoted assignments", "-T", expected_command("wt-preview", "channel", "--json", channel="preview", select=state))
    default_identity = json.loads(remote(preview_client, "channel", "--json", select=None, step="preview default channel").stdout)
    check(default_identity["state_dir"] == str(remote_home / ".wingthing-preview"), "preview default state", default_identity)
    last_record("preview legacy argv", "-T", expected_command("wt-preview", "channel", "--json", channel="preview"))
    assert_absent(state, "channel inspection is read-only")
    assert_absent(remote_home / ".wingthing-preview", "default preview inspection is read-only")
    check(remote(preview_client, "--json", step="empty inventory").stdout == "[]\n", "empty inventory", "selected state is not empty")
    checks.append("remote channel --json reports the exact lexical selected state without creating it")

    # 3. Two preview sessions in the selected state, sandboxed, exact IDs.
    launches = {}
    for name in ["first", "second"]:
        launch = remote(preview_client, "terminal", "--cwd", str(workspace), "--name", name, "--json", "--", "/bin/sh", str(egg_script), name, step=f"launch {name}")
        launched = json.loads(launch.stdout)
        # Record first so cleanup addresses it even if validation fails;
        # kill_owned_eggs also covers launches whose JSON never parsed.
        preview_ids[name] = launched["session"]
        launches[name] = launched
        check(launched.get("cwd") == str(workspace), f"launch {name}", launched)
        remote(preview_client, "session", "wait", launched["session"], "--contains", f"fixture-ready:{name}", "--timeout", "10s", "--json", step=f"ready {name}")
        # Never retried with --unsandboxed: a sandbox that cannot enforce fails,
        # and the running egg's inventory reports the sandbox it applied.
        runtime_isolation(f"{name} no privilege fallback", preview_client, launched["session"])
    for name, session_id in preview_ids.items():
        check((Path(state) / "eggs" / session_id).is_dir(), "selected state", f"{name} not under selected state")
        assert_absent(remote_home / ".wingthing-preview", "default preview state untouched")
        assert_absent(stable_state / "eggs" / session_id, "stable state untouched")
    launched_ps = {s["id"]: s for s in sessions()}
    check(set(launched_ps) == set(preview_ids.values()), "preview inventory", launched_ps)
    pids = {session_id: launched_ps[session_id]["pid"] for session_id in preview_ids.values()}
    check(all(pid > 0 for pid in pids.values()), "preview pids", pids)
    check(stable_session not in launched_ps, "preview cannot enumerate stable", launched_ps)
    check(set(s["id"] for s in sessions(stable_client, select=None)) == {stable_session}, "stable cannot enumerate preview", "stable inventory changed")
    checks.append("two sandboxed preview sessions live only in the quoted selected state")

    # 4. Readers: two read-only attachments observe live output without
    # claiming input; closing their input detaches them.
    first = preview_ids["first"]
    writer_before = launched_ps[first].get("writer_id", "")
    readers = []
    for _ in range(2):
        argv = [str(preview_client), "--remote", HOST, "--remote-state", state, "attach", first, "--read-only"]
        readers.append(subprocess.Popen(argv, env=client_env, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True))
    try:
        observed = poll("two readers", lambda: next((s for s in sessions() if s["id"] == first and s["readers"] >= 2), None))
        check(observed.get("writer_id", "") == writer_before, "readers claim no writer", observed)
        remote(preview_client, "session", "send", first, "--stdin", "--enter", "--json", stdin="reader-canary", step="send while readers attached")
        remote(preview_client, "session", "wait", first, "--contains", "fixture-result:first:reader-canary", "--timeout", "10s", "--json", step="reader canary")
    finally:
        reader_results = []
        for reader in readers:
            # communicate() closes stdin itself (EOF detaches the reader);
            # closing it first makes communicate() flush a closed file.
            try:
                out, err = reader.communicate(timeout=15)
            except subprocess.TimeoutExpired:
                reader.kill()
                out, err = reader.communicate()
            reader_results.append((reader.returncode, out, err))
    for code, out, err in reader_results:
        check(code == 0 and "detached from" in err and "fixture-result:first:reader-canary" in out, "reader detach", {"rc": code, "stdout": out[-2000:], "stderr": err[-2000:]})
    poll("readers released", lambda: any(s["id"] == first and s["readers"] == 0 for s in sessions()))
    checks.append("two read-only readers see live output, claim no writer, and detach on EOF")

    # 5. Detach by name and exact ID with the chord, and by closing input.
    for ref in ["first", first]:
        result = remote(preview_client, "attach", ref, stdin="\x02q", step=f"detach {ref}")
        check("detached from" in result.stderr, "chord detach", result.stderr)
    result = remote(preview_client, "attach", first, step="eof detach")
    check("detached from" in result.stderr, "eof detach", result.stderr)
    checks.append("detach chord by name and ID, and stdin EOF, leave the session running")

    # 6. Status 255 is unknown, never success or failure. Before execution:
    # nothing ran. After execution: input was delivered. Both say unknown and
    # neither is retried.
    before = len(records())
    result = remote(preview_client, "attach", first, disconnect="before", ok=None, step="disconnect before")
    check(result.returncode == 255 and UNKNOWN in result.stderr and "fixture connection lost" in result.stderr, "255 before", {"rc": result.returncode, "stderr": result.stderr})
    second = preview_ids["second"]
    result = remote(preview_client, "session", "send", second, "--stdin", "--enter", "--json", stdin="unknown-delivery-canary", disconnect="after", ok=None, step="disconnect after")
    check(result.returncode == 255 and UNKNOWN in result.stderr, "255 after", {"rc": result.returncode, "stderr": result.stderr})
    check(len(records()) == before + 2, "no transport retry", records()[before:])
    remote(preview_client, "session", "wait", second, "--contains", "fixture-result:second:unknown-delivery-canary", "--timeout", "10s", "--json", step="unknown delivery inspected")
    checks.append("SSH 255 reports unknown state with the same --remote-state, without retry; reconnect inspection finds the real outcome")

    # 7. Reconnect: identical IDs, names, and PIDs.
    reconnect = {s["id"]: s for s in sessions()}
    check(set(reconnect) == set(preview_ids.values()), "reconnect identity", reconnect)
    for name, session_id in preview_ids.items():
        check(reconnect[session_id]["name"] == name and reconnect[session_id]["pid"] == pids[session_id], "reconnect identity", reconnect[session_id])
        os.kill(pids[session_id], 0)
    remote(preview_client, "session", "send", first, "--stdin", "--enter", "--json", stdin="exact-first-input", step="exact input")
    remote(preview_client, "session", "wait", first, "--contains", "fixture-result:first:exact-first-input", "--timeout", "10s", "--json", step="exact input observed")
    first_read = json.loads(remote(preview_client, "session", "read", first, "--json", step="read first").stdout)["ansi"]
    second_read = json.loads(remote(preview_client, "session", "read", second, "--json", step="read second").stdout)["ansi"]
    check("exact-first-input" in first_read and "exact-first-input" not in second_read and "reader-canary" not in second_read, "input stays on selected session", {"first": first_read[-500:], "second": second_read[-500:]})
    checks.append("reconnect preserves exact session IDs, names, and egg PIDs; input stays on the addressed session")

    # 8. Wrong channel and stable aliases fail before any write.
    stable_before = tree(stable_state)
    selected_before = tree(state)
    wrong = str(base / "wrong channel")
    result = remote(preview_client, "session", "ps", "--json", select=wrong, remote_binary="wt", ok=False, step="preview client to stable receiver")
    check("release channel mismatch" in result.stderr, "wrong channel", result.stderr)
    assert_absent(wrong, "wrong channel before writes")
    result = remote(preview_client, "terminal", "--cwd", str(workspace), "--json", "--", "/bin/sh", str(egg_script), "must-not-start", select=str(stable_state), ok=False, step="preview into stable state")
    check("overlaps stable state" in result.stderr, "stable state refused", result.stderr)
    alias = base / "alias to stable"
    alias.symlink_to(stable_state)
    result = remote(preview_client, "terminal", "--cwd", str(workspace), "--json", "--", "/bin/sh", str(egg_script), "must-not-start", select=str(alias), ok=False, step="preview into stable alias")
    check("overlaps stable state" in result.stderr, "stable alias refused", result.stderr)
    # A selected stable state binds the stable channel too, so receivers built
    # before the state channel-marker check reject the request outright.
    result = remote(stable_client, "session", "ps", "--json", ok=False, step="stable receiver into preview state")
    last_record("stable selected argv", "-T", expected_command("wt", "session", "ps", "--json", channel="stable", select=state))
    check('state belongs to "preview" channel' in result.stderr, "stable refuses preview state", result.stderr)
    check(tree(stable_state) == stable_before, "stable state unchanged", "stable state tree changed")
    check(tree(state) == selected_before, "preview state unchanged", "selected preview state tree changed")
    check(not any("must-not-start" in s.get("command", "") for s in sessions()), "no refused launch", "a refused launch started")
    checks.append("wrong channel, stable state, stable alias, and stable receiver on preview state all fail before writes")

    # 9. Explicit stop affects only the selected preview sessions.
    for session_id in preview_ids.values():
        remote(preview_client, "session", "stop", session_id, "--json", step="stop preview")
    poll("preview stopped", lambda: sessions() == [])
    for session_id, pid in pids.items():
        poll("preview pid exited", lambda pid=pid: pid_gone(pid))
    preview_ids.clear()

    # 10. The stable sentinel stayed live with the same PID, bytes, and binaries.
    final_stable = {s["id"]: s for s in sessions(stable_client, select=None)}
    check(set(final_stable) == {stable_session} and final_stable[stable_session]["pid"] == stable_pid, "stable sentinel live", final_stable)
    os.kill(stable_pid, 0)
    check(digest(sentinel) == sentinel_hash, "stable sentinel file hash", "changed")
    check({name: digest(remote_bin / name) for name in binary_hashes} == binary_hashes, "remote binaries unchanged", "changed")
    check({"stable": digest(stable_client), "preview": digest(preview_client)} == client_hashes, "client binaries unchanged", "changed")
    for name in [".wingthing", ".wingthing-preview"]:
        assert_absent(client_home / name, "client state untouched")
    checks.append("stable sentinel egg PID, sentinel file hash, and every binary unchanged; client state never created")

    receipt.update(result="passed", stable_session=stable_session, stable_pid=stable_pid, sentinel_sha256=sentinel_hash, binaries_sha256=binary_hashes, transport_records=len(records()), checks=checks)
except FixtureFailure as failure:
    text = json.dumps(failure.detail)
    receipt.update(step=failure.step, detail=failure.detail, passed_checks=checks, possible_environment_block=any(marker in text for marker in ENVIRONMENT_MARKERS))
except Exception as error:  # noqa: BLE001 - every failure needs a receipt
    receipt.update(step="fixture", detail=repr(error), passed_checks=checks, egg_logs=egg_logs())
finally:
    # Directly, never via SSH: every egg under this fixture's own temp tree,
    # including unrecorded ones, before the tree is removed.
    try:
        kill_owned_eggs()
    except Exception as error:  # noqa: BLE001 - the receipt must still print
        receipt["cleanup_error"] = repr(error)
    receipt["transport_calls"] = calls
    if os.environ.get("WT_KEEP_FIXTURE") != "1":
        shutil.rmtree(base, ignore_errors=True)
    else:
        receipt["fixture_dir"] = str(base)
    rendered = json.dumps(receipt, indent=2) + "\n"
    if receipt_path:
        receipt_path.write_text(rendered)
    print(rendered, end="")
sys.exit(0 if receipt["result"] == "passed" else 1)
