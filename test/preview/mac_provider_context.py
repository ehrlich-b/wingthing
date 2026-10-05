#!/usr/bin/env python3
"""Composed preview CLI regression. Fake vendor only; no credential lookup."""
import hashlib
import json
import os
from pathlib import Path
import pwd
import subprocess
import sys
import tempfile
import time
import unicodedata


SELECTORS = ("HOME", "USER", "LOGNAME", "CLAUDE_CONFIG_DIR",
             "CLAUDE_SECURESTORAGE_CONFIG_DIR", "CFFIXED_USER_HOME")


def write_fake_provider(fake, calls):
    fake.write_text(f'''#!{sys.executable}
import hashlib,json,os,pathlib,signal,subprocess,sys,unicodedata
args=sys.argv[1:]
if args==['--version']:
    print('Disposable context fixture; not Claude');sys.exit(0)
env={{k:os.environ.get(k) for k in {SELECTORS!r}}}
config=env['CLAUDE_CONFIG_DIR']
suffix=hashlib.sha256(unicodedata.normalize('NFC',config).encode()).hexdigest()[:8]
kind='status' if args==['auth','status','--json'] else 'headless' if '-p' in args else 'native'
settings=[]
for i,arg in enumerate(args):
    if arg=='--settings': value=args[i+1]
    elif arg.startswith('--settings='): value=arg.split('=',1)[1]
    else: continue
    settings.append(json.loads(value) if value.lstrip().startswith('{{') else json.loads(pathlib.Path(value).read_text()))
receipt={{'route':kind,'selectors':env,'cwd':os.getcwd(),'argv':args,
          'synthetic_service_suffix':suffix,'settings':settings}}
if kind=='native':
    provider=args[args.index('--session-id')+1]
    project=pathlib.Path(config)/'projects'/os.getcwd().replace('/','-')
    project.mkdir(parents=True,exist_ok=True)
    (project/(provider+'.jsonl')).write_text(json.dumps({{'type':'assistant','sessionId':provider,'message':{{'role':'assistant','content':[{{'type':'text','text':'fixture only'}}]}}}})+'\\n')
    for value in settings:
        for entry in value.get('hooks',{{}}).get('SessionStart',[]):
            for action in entry.get('hooks',[]):
                subprocess.run(['/bin/sh','-c',action['command']],input=json.dumps({{'session_id':provider,'hook_event_name':'SessionStart'}}).encode(),check=True)
with pathlib.Path({str(calls)!r}).open('a') as output:output.write(json.dumps(receipt)+'\\n')
if kind=='status':
    print(json.dumps({{'loggedIn':True,'authMethod':'claude.ai','apiProvider':'firstParty','configDirectory':config,'email':'fixture@example.invalid','orgName':'Fixture','subscriptionType':'max'}}));sys.exit(0)
if kind=='headless':
    print(json.dumps({{'type':'assistant','message':{{'content':[{{'type':'text','text':'fixture headless output'}}]}}}}))
    print(json.dumps({{'type':'result','input_tokens':1,'output_tokens':1}}));sys.exit(0)
print('FIXTURE_CONTEXT_READY',flush=True)
signal.pause()
''')
    fake.chmod(0o700)


def native_hook_published(spool, provider_id):
    return spool.is_dir() and any(
        (value := json.loads(path.read_text())).get("session_id") == provider_id
        and value.get("hook_event_name") == "SessionStart"
        for path in spool.glob("*.json"))


def read_jsonl(path):
    if not path.is_file():
        return []
    return [json.loads(line) for line in path.read_text().splitlines(keepends=True)
            if line.endswith("\n")]


def stop_session(binary, environment, workspace, session):
    primary_failure = sys.exc_info()[1]
    try:
        result = subprocess.run([str(binary), "egg", "stop", session], env=environment,
                                cwd=workspace, capture_output=True, text=True, timeout=15)
        assert result.returncode == 0 or f'session "{session}" not found' in result.stderr, result.stderr[-1800:]
    except (AssertionError, subprocess.TimeoutExpired) as cleanup_failure:
        if primary_failure is None:
            raise
        print(f"fixture cleanup failed: {cleanup_failure}", file=sys.stderr)


def main():
    if sys.platform != "darwin":
        raise SystemExit("mac_provider_context requires macOS; no substitute backend")
    binary = Path(sys.argv[1]).resolve(strict=True)
    os_home = str(Path(pwd.getpwuid(os.getuid()).pw_dir).resolve())
    with tempfile.TemporaryDirectory(prefix="wtpc-", dir="/tmp") as temporary:
        lexical = Path(temporary)
        root = lexical.resolve()
        assert lexical != root, "fixture requires /tmp -> /private/tmp alias"
        host, workspace, fake_bin = (root / p for p in ("host", "work", "bin"))
        for directory in (host, workspace, fake_bin):
            directory.mkdir(mode=0o700)
        stable = host / ".claude"
        stable.mkdir(mode=0o700)
        stable_settings = stable / "settings.json"
        stable_value = json.dumps({"model": "fixture-host-model-must-not-import",
                                   "env": {"FIXTURE_HOST_SETTING": "must-not-import"}})
        stable_settings.write_text(stable_value)
        state = root / "state"
        data = state / "provider-home"
        data.mkdir(parents=True, mode=0o700)
        (state / ".release-channel").write_text("preview\n")
        cfg = data / ".claude"
        calls = workspace / "calls.jsonl"
        fake = fake_bin / "claude"
        write_fake_provider(fake, calls)
        base = {"HOME": str(host), "PATH": f"{fake_bin}:/usr/bin:/bin:/usr/sbin:/sbin",
                "LANG": "en_US.UTF-8", "TERM": "xterm-256color", "TMPDIR": "/private/tmp",
                "USER": "fixture-user", "LOGNAME": "fixture-logname",
                "CLAUDE_CONFIG_DIR": "/fixture-foreign/config",
                "CLAUDE_SECURESTORAGE_CONFIG_DIR": "/fixture-foreign/service",
                "CFFIXED_USER_HOME": "/fixture-foreign/preferences",
                "ANTHROPIC_API_KEY": "synthetic-must-not-cross"}

        def invoke(environment, *args, timeout=15):
            result = subprocess.run([str(binary), *args], env=environment, cwd=workspace,
                                    capture_output=True, text=True, timeout=timeout)
            assert result.returncode == 0, (args, result.stderr[-1800:])
            return result

        def receipts():
            return read_jsonl(calls)

        checked = []
        for spelling in (lexical / "state", state):
            environment = dict(base, WINGTHING_DIR=str(spelling))
            status = json.loads(invoke(environment, "provider", "status", "claude", "--json").stdout)
            assert status["state"] == "reported_authenticated"
            assert status["os_home"] == os_home and status["data_home"] == str(data)
            checked.append(receipts()[-1])
            before = len(receipts())
            launch = json.loads(invoke(environment, "egg", "claude", "--json", "--cwd", str(workspace)).stdout)
            session = launch["session"]
            assert launch["isolation"] == "wingthing-sandbox"
            egg_dir = state / "eggs" / session

            def diagnostics():
                paths = (egg_dir / "egg.log", state / "logs" / (session + ".log"),
                         egg_dir / "lifecycle.jsonl")
                return "\n".join(path.read_text()[-3000:] for path in paths if path.is_file())

            try:
                deadline = time.monotonic() + 15
                while time.monotonic() < deadline:
                    new = receipts()[before:]
                    if any(item["route"] == "native" for item in new):
                        break
                    if any(event.get("type") in ("session_exit", "session_failed")
                           for event in read_jsonl(egg_dir / "lifecycle.jsonl")):
                        raise AssertionError("fake native provider exited before readiness\n" + diagnostics())
                    time.sleep(0.05)
                else:
                    raise AssertionError("fake native provider did not reach final process\n" + diagnostics())
                native = next(item for item in new if item["route"] == "native")
                checked.append(native)
                settings_path = Path(native["argv"][native["argv"].index("--settings") + 1])
                assert settings_path.resolve() == (egg_dir / "claude-settings.json").resolve(), "native settings escaped the egg directory"
                assert settings_path.stat().st_mode & 0o777 == 0o600, "native settings are not private"
                meta_path = egg_dir / "egg.meta"
                deadline = time.monotonic() + 3
                while time.monotonic() < deadline:
                    meta = dict(line.split("=", 1) for line in meta_path.read_text().splitlines() if "=" in line)
                    if meta.get("isolation") == "wingthing-sandbox":
                        break
                    time.sleep(0.05)
                assert meta["provider_home"] == str(data) and meta["cwd"] == str(workspace)
                assert meta["isolation"] == "wingthing-sandbox"
                spool = cfg / "wingthing-events" / session
                assert native_hook_published(spool, meta["provider_session_id"]), "native hook was not executed in data home"
                assert list((cfg / "projects").rglob(meta["provider_session_id"] + ".jsonl"))
            finally:
                stop_session(binary, environment, workspace, session)
            result = invoke(environment, "run", "--agent", "claude", "synthetic context fixture")
            assert "fixture headless output" in result.stdout
            checked.append(receipts()[-1])

        expected = {"HOME": os_home, "USER": None, "LOGNAME": None,
                    "CLAUDE_CONFIG_DIR": str(cfg), "CLAUDE_SECURESTORAGE_CONFIG_DIR": None,
                    "CFFIXED_USER_HOME": None}
        suffix = hashlib.sha256(unicodedata.normalize("NFC", str(cfg)).encode()).hexdigest()[:8]
        for receipt in checked:
            assert receipt["selectors"] == expected, receipt
            assert receipt["synthetic_service_suffix"] == suffix
            assert receipt["cwd"] == str(data if receipt["route"] == "status" else workspace)
            assert "fixture-host-model" not in json.dumps(receipt)
            if receipt["route"] == "native":
                assert receipt["settings"], "native lifecycle settings missing"
                assert str(data) in json.dumps(receipt["settings"])
                assert os_home + "/.claude" not in json.dumps(receipt["settings"])
        assert stable_settings.read_text() == stable_value
        assert len(checked) == 6
        print(json.dumps({"result": "pass", "fake_vendor_only": True, "routes": [r["route"] for r in checked],
                          "canonical_alias_identity": True, "native_history_and_hook_spool": True,
                          "credential_access_proven": False, "synthetic_service_suffix": True}))


if __name__ == "__main__":
    main()
