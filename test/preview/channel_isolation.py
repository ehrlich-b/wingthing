#!/usr/bin/env python3
"""Disposable local black-box channel proof; no vendor login or network access."""
import hashlib
import json
import os
import platform
from pathlib import Path
import shutil
import socket
import subprocess
import sys
import tempfile

stable_binary, preview_binary, package = [Path(p).resolve() for p in sys.argv[1:]]

def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()

def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]

with tempfile.TemporaryDirectory(prefix="wtp-", dir="/tmp") as temporary:
    base = Path(temporary)
    home = base / "home"
    home.mkdir()
    stable_state = home / ".wingthing"
    stable_state.mkdir()
    sentinel = stable_state / "stable-sentinel"
    sentinel.write_text("stable-state-must-survive\n")
    stable_tokens = stable_state / "token.json"
    stable_tokens.write_text('{"token":"nonsecret-stable-fixture"}\n')
    bin_dir = base / "bin"
    bin_dir.mkdir()
    stable_install = bin_dir / "wt"
    shutil.copy2(stable_binary, stable_install)
    stable_hash = digest(stable_install)
    stable_sentinel = digest(sentinel)
    stable_token_hash = digest(stable_tokens)
    env = {k: v for k, v in os.environ.items() if not k.startswith(("WT_", "WINGTHING", "FLY_")) and k not in {"GITHUB_CLIENT_ID", "GITHUB_CLIENT_SECRET", "GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_SECRET", "SMTP_HOST"}}
    env.update(HOME=str(home), WT_PREVIEW_INSTALL_DIR=str(bin_dir), ANTHROPIC_API_KEY="nonsecret-ambient-fixture", CLAUDE_CODE_OAUTH_TOKEN="nonsecret-ambient-fixture", CODEX_HOME=str(home / ".codex"))
    def run(binary, *args, ok=True, extra=None):
        result = subprocess.run([str(binary), *args], env={**env, **(extra or {})}, capture_output=True, text=True, timeout=35)
        if (result.returncode == 0) != ok:
            logs = "\n".join(p.read_text(errors="replace") for p in home.glob(".wingthing*/eggs/*/egg.log"))
            raise AssertionError(f"{args}: expected success={ok}; {result.stdout} {result.stderr}\n{logs}")
        return result.stdout
    def intact():
        assert digest(stable_install) == stable_hash
        assert digest(sentinel) == stable_sentinel
        assert digest(stable_tokens) == stable_token_hash

    # Inspection is read-only, including a not-yet-created preview state dir.
    identity = json.loads(run(preview_binary, "channel", "--json"))
    assert identity["release_channel"] == "preview" and identity["executable"] == "wt-preview"
    assert not (home / ".wingthing-preview").exists()

    # Exact-address rejection happens before initial CLI config/session writes,
    # and the same runtime error reaches real MCP clients as a tool result.
    socket_limit = 103 if platform.system() == "Darwin" else 107
    socket_errors = {}
    for channel_binary in [stable_install, preview_binary]:
        long_state = base / (channel_binary.name + "-" + "x" * 120)
        process_marker = base / (channel_binary.name + "-must-not-start")
        long_env = {**env, "WINGTHING_DIR": str(long_state)}
        result = subprocess.run([str(channel_binary), "terminal", "--detach", "--json", "--", "/bin/sh", "-c", f"touch {process_marker}"], env=long_env, capture_output=True, text=True, timeout=5)
        assert result.returncode != 0 and not long_state.exists() and not process_marker.exists()
        for fragment in [str(long_state / "eggs"), "egg.sock", "bytes", f"at most {socket_limit} bytes", "WINGTHING_DIR", "shorter"]:
            assert fragment in result.stderr, result.stderr
        socket_errors[channel_binary.name + "_cli"] = result.stderr.strip()
        requests = [
            {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {}},
            {"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {"name": "terminal_start", "arguments": {"cwd": str(base), "command": ["/bin/sh", "-c", f"touch {process_marker}"]}}},
        ]
        result = subprocess.run([str(channel_binary), "mcp", "stdio", "--client", "socket-fixture"], env=long_env, input="".join(json.dumps(r) + "\n" for r in requests), capture_output=True, text=True, timeout=5)
        assert result.returncode == 0, result.stderr
        response = next(json.loads(line)["result"] for line in result.stdout.splitlines() if json.loads(line).get("id") == 2)
        message = response["structuredContent"]["error"]
        assert response["isError"] and not (long_state / "eggs").exists() and not process_marker.exists()
        for fragment in [str(long_state / "eggs"), "egg.sock", "bytes", f"at most {socket_limit} bytes", "WINGTHING_DIR", "shorter"]:
            assert fragment in message, message
        assert (long_state / "mcp-audit.log").exists()
        socket_errors[channel_binary.name + "_mcp"] = message
    run(preview_binary, "init", extra={"WINGTHING_DIR": str(stable_state)}, ok=False)
    alias = base / "stable-alias"
    alias.symlink_to(stable_state)
    run(preview_binary, "init", extra={"WINGTHING_DIR": str(alias)}, ok=False)
    run(stable_install, "--expected-channel", "preview", "init", ok=False)
    run(preview_binary, "start", "--org", "slide", ok=False)
    run(preview_binary, "login", "--roost", "https://slide.example.test", ok=False)
    intact()

    run(package / "preview-package.sh", "install")
    installed = bin_dir / "wt-preview"
    run(installed, "init")
    state = home / ".wingthing-preview"
    assert (state / ".release-channel").read_text().strip() == "preview"
    assert not (state / "token.json").exists()
    run(stable_install, "session", "ps", "--json", extra={"WINGTHING_DIR": str(state)}, ok=False)

    # A stable egg lives in disposable stable state. Preview cannot enumerate
    # it, even if its metadata is accidentally copied into the preview tree.
    stable_session = json.loads(run(stable_install, "terminal", "--detach", "--json", "--name", "stable-proof", "--", "/bin/sh", "-c", "printf 'stable-ready\\n'; exec /usr/bin/tail -f /dev/null"))["session"]
    try:
        assert (json.loads(run(installed, "session", "ps", "--json")) or []) == []
        shutil.copytree(stable_state / "eggs" / stable_session, state / "eggs" / stable_session, ignore=shutil.ignore_patterns("*.sock"))
        assert (json.loads(run(installed, "session", "ps", "--json")) or []) == []
        run(installed, "attach", stable_session, ok=False)
        shutil.rmtree(state / "eggs" / stable_session)
        assert stable_session in run(stable_install, "session", "ps", "--json")
        preview_session = json.loads(run(installed, "terminal", "--detach", "--json", "--name", "preview-proof", "--", "/bin/sh", "-c", "printf 'preview-ready\\nhome=%s\\nkey=%s\\ncodex=%s\\n' \"$HOME\" \"${ANTHROPIC_API_KEY-unset}\" \"${CODEX_HOME-unset}\"; exec /usr/bin/tail -f /dev/null"))["session"]
        run(installed, "session", "wait", preview_session, "--contains", "preview-ready", "--timeout", "5s", "--json")
        snapshot = json.loads(run(installed, "session", "read", preview_session, "--json"))["ansi"]
        assert "key=unset" in snapshot and "codex=unset" in snapshot
        assert str(state / "provider-home") in snapshot
        debug = state / "provider-home" / ".claude" / "debug"
        debug.mkdir(parents=True)
        debug_log = debug / "fixture.log"
        debug_log.write_text("nonsecret-provider-history-fixture")
        debug_latest = debug / "latest"
        for target in ["fixture.log", str(debug_log)]:
            debug_latest.symlink_to(target)
            assert preview_session in run(installed, "session", "ps", "--json")
            debug_latest.unlink()
        for target in [str(sentinel), "missing.log", "latest"]:
            debug_latest.symlink_to(target)
            run(installed, "session", "ps", "--json", ok=False)
            debug_latest.unlink()
        port = free_port()
        run(installed, "roost", "start", "--addr", f"127.0.0.1:{port}")
        try:
            pid = int((state / "roost.pid").read_text())
            os.kill(pid, 0)
            # Same verified preview artifact is a local upgrade/rollback fixture.
            architecture = {"arm64": "arm64", "aarch64": "arm64", "x86_64": "amd64", "amd64": "amd64"}[platform.machine()]
            assets = [package / f"wt-preview-{platform.system().lower()}-{architecture}"]
            assert assets[0].is_file()
            run(installed, "update", "--file", str(assets[0]))
            new_pid = int((state / "roost.pid").read_text())
            assert new_pid != pid
            os.kill(new_pid, 0)
            assert preview_session in run(installed, "session", "ps", "--json")
            run(installed, "session", "kill", preview_session)
            intact()
        finally:
            run(installed, "roost", "stop")
        # Stable binary replacement rejected even when renamed preview locally.
        renamed = base / "wt"
        shutil.copy2(installed, renamed)
        run(renamed, "update", "--file", str(assets[0]), ok=False)
        intact()
        run(package / "preview-package.sh", "uninstall")
        assert not installed.exists() and state.exists()
        assert stable_session in run(stable_install, "session", "ps", "--json")
        intact()
    finally:
        run(stable_install, "session", "kill", stable_session)
    receipt = {"result": "passed", "stable_binary_sha256_before": stable_hash, "stable_binary_sha256_after": digest(stable_install), "stable_sentinel_sha256_before": stable_sentinel, "stable_sentinel_sha256_after": digest(sentinel), "stable_token_sha256_before": stable_token_hash, "stable_token_sha256_after": digest(stable_tokens), "version": identity["version"], "socket_path_limit_bytes": socket_limit, "socket_path_errors": socket_errors, "checks": ["read-only identity", "long socket CLI path fails before state or process creation", "long socket MCP path returns actionable audited tool error", "state overlap and symlink refusal", "no auth import", "no org/shared coordinator", "stable egg copy refused", "preview egg reads clean provider HOME and survives daemon upgrade", "provider links resolve only inside exact preview state", "local install", "preview daemon start/upgrade/restart/stop", "stable binary preserved", "preview uninstall preserves stable process and all state"]}
    (package / "isolation-receipt.json").write_text(json.dumps(receipt, indent=2) + "\n")
    print(json.dumps(receipt, indent=2))
