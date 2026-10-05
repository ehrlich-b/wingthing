#!/usr/bin/env python3
"""Exercise the actual preview CLI with a disposable fake Claude executable."""
import json
import os
from pathlib import Path
import pty
import select
import shlex
import subprocess
import sys
import tempfile
import time


def main():
    binary = Path(sys.argv[1]).resolve()
    with tempfile.TemporaryDirectory(prefix="wt-provider-guide-") as temporary:
        root = Path(temporary).resolve()
        host = root / "host"
        stable = host / ".wingthing"
        stable.mkdir(parents=True)
        sentinel = stable / "token.json"
        sentinel.write_text("fixture-stable-token-never-read-or-import")
        state = root / "preview state"
        home = state / "provider-home"
        config_dir = home / ".claude"
        fake_bin = root / "bin"
        fake_bin.mkdir()
        calls = root / "fake-vendor-calls.jsonl"
        mode = root / "mode"
        mode.write_text("authenticated")
        fake = fake_bin / "claude"
        fake.write_text(f"""#!{sys.executable}
import json, os, pathlib, sys
with pathlib.Path({str(calls)!r}).open('a') as output:
    output.write(json.dumps({{'argv': sys.argv[1:], 'env': dict(os.environ), 'cwd': os.getcwd()}}) + '\\n')
assert sys.argv[1:] == ['auth', 'status', '--json'], 'only explicit status is permitted in this fixture'
mode = pathlib.Path({str(mode)!r}).read_text()
if mode == 'error':
    print('fixture-secret-error', file=sys.stderr)
    sys.exit(2)
if mode == 'malformed':
    print('fixture-secret-malformed')
    sys.exit(1)
report = {{'loggedIn': mode != 'logged-out', 'authMethod': 'none' if mode == 'logged-out' else 'claude.ai',
    'apiProvider': 'firstParty', 'configDirectory': '/foreign/namespace' if mode == 'foreign' else os.environ['CLAUDE_CONFIG_DIR'],
    'email': 'personal@example.com', 'orgName': 'Personal fixture', 'subscriptionType': 'max',
    'accessToken': 'fixture-secret-token', 'apiKeySource': 'fixture-secret-source'}}
print(json.dumps(report))
sys.exit(1 if mode == 'logged-out' else 0)
""")
        fake.chmod(0o700)
        env = {
            "HOME": str(host), "PATH": f"{fake_bin}:/usr/bin:/bin",
            "WINGTHING_DIR": str(state), "LANG": "en_US.UTF-8",
            "CLAUDE_CONFIG_DIR": "/foreign/profile",
            "CLAUDE_SECURESTORAGE_CONFIG_DIR": "/foreign/keychain",
            "CLAUDE_CODE_OAUTH_TOKEN": "fixture-secret-ambient",
            "ANTHROPIC_API_KEY": "fixture-secret-ambient",
            "ANTHROPIC_BASE_URL": "https://foreign.invalid",
            "CODEX_HOME": "/foreign/codex", "HTTP_PROXY": "https://foreign.invalid",
        }

        def invoke(*args):
            result = subprocess.run([str(binary), "provider", *args, "--json"], env=env,
                                    cwd=root, capture_output=True, text=True, timeout=10)
            assert result.returncode == 0, f"CLI failed: {result.stderr}"
            assert "fixture-secret" not in result.stdout + result.stderr, "raw provider data leaked"
            return json.loads(result.stdout)

        status = invoke("status", "claude")
        assert status["state"] == "profile_not_initialized"
        guide = invoke("setup-guide", "claude")
        assert not state.exists() and not calls.exists(), "guide or inspection initialized state or invoked vendor"
        assert guide["data_home"] == str(home) and guide["config_directory"] == str(config_dir)
        assert "'provider' 'login' 'claude'" in guide["login_command"]
        assert "env -i" in guide["login_command"] and str(state) in guide["login_command"]
        assert "foreign" not in guide["login_command"] and "fixture-secret" not in guide["login_command"]
        assert shlex.split(guide["status_command"])[-4:] == ["provider", "status", "claude", "--json"]

        # Only test-owned fixture state is prepared; no printed login is executed.
        home.mkdir(parents=True)
        (state / ".release-channel").write_text("preview\n")
        before = sorted(str(p.relative_to(state)) for p in state.rglob("*"))
        status = invoke("status", "claude")
        assert status["state"] == "reported_authenticated"
        assert status["reported_account"]["email"] == "personal@example.com"
        assert "unverified" in status["observation"]
        call = json.loads(calls.read_text().splitlines()[-1])
        assert call["argv"] == ["auth", "status", "--json"]
        assert call["cwd"] == str(home)
        assert call["env"]["HOME"] == status["os_home"] and call["env"]["CLAUDE_CONFIG_DIR"] == str(config_dir)
        assert not any("foreign" in value or "fixture-secret" in value for value in call["env"].values())

        for fixture_mode, expected in [("logged-out", "reported_not_logged_in"), ("error", "unknown"),
                                        ("malformed", "unknown"), ("foreign", "unknown")]:
            mode.write_text(fixture_mode)
            report = invoke("status", "claude")
            assert report["state"] == expected, fixture_mode
            if expected == "unknown":
                assert "reported_account" not in report and "reported_logged_in" not in report
        assert before == sorted(str(p.relative_to(state)) for p in state.rglob("*")), "diagnostic changed preview state"
        login = subprocess.run([str(binary), "provider", "login", "claude"], env=env, cwd=root,
                               capture_output=True, text=True, timeout=10)
        assert login.returncode != 0 and "private user terminal" in login.stderr
        # A real controlling PTY is still refused for a detected Wingthing egg.
        # Only fixture error output is read; the fake provider must never start.
        pid, terminal = pty.fork()
        if pid == 0:
            os.chdir(root)
            os.execve(str(binary), [str(binary), "provider", "login", "claude"], dict(env, WT_SESSION_ID="fixture-managed-egg"))
        output = bytearray()
        deadline = time.monotonic() + 10
        reaped = False
        try:
            while time.monotonic() < deadline:
                ready, _, _ = select.select([terminal], [], [], 0.1)
                if ready:
                    try:
                        data = os.read(terminal, 4096)
                    except OSError:
                        break
                    if not data:
                        break
                    output.extend(data)
            else:
                os.kill(pid, 9)
                raise AssertionError("managed PTY refusal timed out")
            _, wait_status = os.waitpid(pid, 0)
            reaped = True
            assert os.waitstatus_to_exitcode(wait_status) != 0
            assert b"managed-agent context" in output, output.decode(errors="replace")
        finally:
            os.close(terminal)
            if not reaped:
                try:
                    os.kill(pid, 9)
                except ProcessLookupError:
                    pass
                os.waitpid(pid, 0)
        assert before == sorted(str(p.relative_to(state)) for p in state.rglob("*")), "refused login created state"
        assert sentinel.read_text() == "fixture-stable-token-never-read-or-import", "stable sentinel changed"
        assert len(calls.read_text().splitlines()) == 5, "unexpected automatic provider invocation"
        print(json.dumps({"result": "pass", "binary": str(binary), "fake_vendor_only": True,
                          "status_invocations": 5, "login_invocations": 0,
                          "captured_login_refused": True,
                          "managed_controlling_pty_login_refused": True,
                          "preview_state_unchanged": True, "stable_sentinel_unchanged": True}))


if __name__ == "__main__":
    main()
