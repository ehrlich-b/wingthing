import contextlib
import io
import http.server
import threading
import json
import os
from pathlib import Path
import plistlib
import shlex
import shutil
import zipfile
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
import urllib.parse

import ios_install_support as support


@contextlib.contextmanager
def mode_server(mode):
    class Handler(http.server.BaseHTTPRequestHandler):
        def do_POST(self):
            if self.path != "/admin" or self.headers.get("Authorization") != "Bearer fixture-token":
                self.send_error(403)
                return
            body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
            if body != {"Op": "mode"}:
                self.send_error(400)
                return
            self.send_response(200)
            self.end_headers()
            self.wfile.write(json.dumps({"wing_id": "fixture-wing", "mode": mode}).encode())

        def log_message(self, *_):
            pass

    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever)
    thread.start()
    try:
        yield {"port": server.server_port, "address": "127.0.0.1:" + str(server.server_port),
               "token": "fixture-token", "wing_id": "fixture-wing"}
    finally:
        server.shutdown()
        server.server_close()
        thread.join()


FIELDS = {"v": "1", "origin": "https://home.example", "account": "owner", "wing": "mac",
          "key": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", "token": "fixture-private-token"}


def link(fields=FIELDS):
    return "wingthing://home?" + urllib.parse.urlencode(fields, quote_via=urllib.parse.quote)


def phone(identifier="phone-one", transport="local", pairing="paired", mode="enabled"):
    return {"identifier": identifier, "hardwareProperties": {"productType": "iPhone18,1", "udid": "UDID-" + identifier},
            "connectionProperties": {"transportType": transport, "pairingState": pairing},
            "deviceProperties": {"name": "Test iPhone", "developerModeStatus": mode}}


@contextlib.contextmanager
def installer_fixture():
    with tempfile.TemporaryDirectory() as directory:
        root = Path(directory)
        clone = root / "clone"
        scripts = clone / "mobile/ios/scripts"
        scripts.mkdir(parents=True)
        source = Path(__file__).resolve().parent
        for name in ("install_phone.sh", "ios_install_support.py", "wt-ios-install"):
            shutil.copy2(source / name, scripts / name)
        project = clone / "mobile/ios/Wingthing.xcodeproj"
        project.mkdir()
        (project / "project.pbxproj").touch()
        mockbin = root / "mockbin"; mockbin.mkdir()
        xcode = root / "Xcode.app"
        (xcode / "Contents/Developer/usr/bin").mkdir(parents=True)
        (xcode / "Contents/Developer/usr/bin/xcodebuild").touch()
        (xcode / "Contents/Developer/usr/bin/xcodebuild").chmod(0o755)
        devices = root / "devices.json"
        devices.write_text(json.dumps({"result": {"devices": [phone()]}}))
        prefs = root / "prefs.plist"
        prefs.write_bytes(plistlib.dumps({"IDEProvisioningTeams": [{"teamID": "TESTTEAM01"}]}))
        mocks = {
            "uname": 'printf "Darwin\\n"',
            "plutil": ':',
            "pbcopy": ':',
            "open": ':',
            "defaults": 'cp "$MOCK_PREFS" "$3"',
            "taskpolicy": 'shift; exec "$@"',
            "nice": 'shift 2; exec "$@"',
            "ps": '[[ $* == "-p 123 -o pid= -o lstart=" ]]; printf "123 Thu Jan 1 00:00:00 2026\\n"',
            "xcodebuild": '''printf '%s\\n' "$*" >>"$MOCK_CALLS"
if [[ $1 == -checkFirstLaunchStatus ]]; then exit 0; fi
while [[ $# -gt 0 ]]; do
  if [[ $1 == -derivedDataPath ]]; then mkdir -p "$2/Build/Products/Release-iphoneos/Wingthing.app"; fi
  shift
done''',
            "xcrun": '''printf '%s\\n' "$*" >>"$MOCK_CALLS"
if [[ $1 == devicectl && $2 == list ]]; then
  while [[ $# -gt 0 ]]; do
    if [[ $1 == --json-output ]]; then cp "$MOCK_DEVICES" "$2"; break; fi
    shift
  done
fi''',
        }
        for name, body in mocks.items():
            path = mockbin / name
            path.write_text("#!/bin/bash\nset -euo pipefail\n" + body + "\n"); path.chmod(0o755)
        environment = dict(os.environ, PATH=str(mockbin) + ":" + os.environ["PATH"], WT_IOS_XCODE=str(xcode),
                           WT_IOS_STATE=str(root / "state"), WT_IOS_ENTRYPOINT=str(root / "entrypoint"),
                           MOCK_PREFS=str(prefs), MOCK_DEVICES=str(devices), MOCK_CALLS=str(root / "calls"))
        for name in ("WT_IOS_TEAM", "WT_IOS_BUNDLE", "WT_IOS_DEVICE", "WT_IOS_SETUP_LINK_FILE"):
            environment.pop(name, None)
        environment["WT_IOS_CLONE"] = str(clone)
        environment["WT_IOS_GO"] = shutil.which("go")
        command = ["/bin/bash", str(clone / "mobile/ios/scripts/install_phone.sh"), "--install-only"]
        yield root, clone, environment, command


def offline_pair_module(root, clone, environment):
    """A real Go build with one tiny dependency available only in a local cache."""
    cache = root / "user module cache"
    download = cache / "cache/download/fixture.test/dependency/@v"
    download.mkdir(parents=True)
    mod = "module fixture.test/dependency\n\ngo 1.26.9\n"
    (download / "v1.0.0.mod").write_text(mod)
    (download / "v1.0.0.info").write_text('{"Version":"v1.0.0","Time":"2026-01-01T00:00:00Z"}')
    with zipfile.ZipFile(download / "v1.0.0.zip", "w") as archive:
        archive.writestr("fixture.test/dependency@v1.0.0/go.mod", mod)
        archive.writestr("fixture.test/dependency@v1.0.0/dependency.go",
                         'package dependency\nconst Message = "offline fixture"\n')
    (clone / "go.mod").write_text("module github.com/ehrlich-b/wingthing\n\ngo 1.26.9\n"
                                 "require fixture.test/dependency v1.0.0\n")
    main = clone / "cmd/wt"
    main.mkdir(parents=True)
    (main / "main.go").write_text(r'''package main
import ("fmt"; "os"; "path/filepath"; "fixture.test/dependency")
func main() {
    f, err := os.OpenFile(os.Getenv("MOCK_PAIR_CALLS"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
    if err != nil { panic(err) }; defer f.Close()
    fmt.Fprintln(f, os.Args[1:])
    if os.Args[1] == "wing" && os.Args[2] == "status" {
        fmt.Println("wing daemon is running (pid 123)")
        mode := os.Getenv("MOCK_LEGACY_MODE")
        if mode == "local-only" { fmt.Println("  local control: ready") }
        if mode == "relay" { fmt.Println("  relay: connected") }
        if mode == "" {
            os.WriteFile(filepath.Join(os.Getenv("WINGTHING_DIR"), "phone-control.json"),
                         []byte(`{"address":"0.0.0.0:6078"}`), 0600)
        }
    }
    if os.Args[1] == "egg" { fmt.Println(os.Getenv("MOCK_SESSIONS")) }
    if os.Args[1] == "pair" { fmt.Println(dependency.Message) }
}
''')
    wing = root / "main wing"
    wing.mkdir()
    environment.update(GOMODCACHE=str(cache), WT_IOS_WING_DIR=str(wing),
                       WT_IOS_BUNDLE="dev.wingthing.fixture", MOCK_PAIR_CALLS=str(root / "pair-calls"),
                       GOFLAGS="-mod=mod", GOPROXY="https://invalid.example", GOPRIVATE="fixture.test/*")
    environment.pop("MOCK_LEGACY_MODE", None)
    return cache, wing


class InstallerTests(unittest.TestCase):
    def test_cold_offline_cache_builds_from_user_file_proxy(self):
        with installer_fixture() as (root, clone, environment, command):
            cache, _ = offline_pair_module(root, clone, environment)
            self.assertFalse((clone / ".scratch").exists())
            original_cache = {p.relative_to(cache): p.read_bytes() for p in cache.rglob("*") if p.is_file()}
            result = subprocess.run(command[:-1] + ["--connect-only"], env=environment,
                                    text=True, capture_output=True, timeout=60)
            log = (root / "state/wing-build.log").read_text()
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr + log)
            self.assertIn("offline fixture", result.stdout)
            self.assertTrue((clone / ".scratch/go-mod/fixture.test/dependency@v1.0.0/dependency.go").is_file())
            self.assertTrue((clone / ".scratch/go-mod/cache/download/fixture.test/dependency/@v/v1.0.0.zip").is_file())
            self.assertEqual({p.relative_to(cache): p.read_bytes() for p in cache.rglob("*") if p.is_file()},
                             original_cache)
            support.check_pair_output(clone / ".scratch/bin/wt-ios-pair")

    def test_pre_existing_mock_output_fails_with_named_error_without_overwriting(self):
        with installer_fixture() as (root, clone, environment, command):
            offline_pair_module(root, clone, environment)
            output = clone / ".scratch/bin/wt-ios-pair"
            output.parent.mkdir(parents=True)
            mock = b"#!/bin/bash\necho stale fixture\n"
            output.write_bytes(mock)
            output.chmod(0o700)
            result = subprocess.run(command[:-1] + ["--connect-only"], env=environment,
                                    text=True, capture_output=True, timeout=30)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("PAIR_CLI_OUTPUT_CONFLICT", result.stderr)
            self.assertEqual(output.read_bytes(), mock)
            self.assertFalse((root / "pair-calls").exists())
            self.assertFalse((root / "state/wing-build.log").exists())
            self.assertFalse((clone / ".scratch/go-mod").exists())

    def test_pre_pairing_wing_restart_preserves_mode_and_names_sessions(self):
        for mode in ("relay", "local-only", "unknown", "stale-relay", "stale-local-only"):
            with self.subTest(mode=mode), installer_fixture() as (root, clone, environment, command):
                _, wing = offline_pair_module(root, clone, environment)
                # Saved launch arguments must not override verified status.
                (wing / "wing.args").write_text("wing\nstart\n--local-only\n")
                (wing / "wing.pid").write_text("123\n")
                reported_mode = mode.removeprefix("stale-")
                (wing / "wing.status").write_text(json.dumps({
                    "state": "local" if reported_mode == "local-only" else "connected",
                    "ts": "2025-12-31T23:59:59Z" if mode.startswith("stale-") else "2026-01-01T00:00:02Z"}))
                environment.update(MOCK_LEGACY_MODE=reported_mode,
                                   MOCK_SESSIONS="NAME  ID\nlong running research  egg-123\nrelease work  egg-456")
                result = subprocess.run(command[:-1] + ["--connect-only"], env=environment,
                                        text=True, capture_output=True, timeout=60)
                log = (root / "state/wing-build.log").read_text()
                self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr + log)
                calls = (root / "pair-calls").read_text().splitlines()
                self.assertFalse((wing / "phone-control.json").exists())
                if mode == "unknown" or mode.startswith("stale-"):
                    self.assertIn("MAIN_WING_MODE_UNKNOWN", result.stderr)
                    self.assertEqual(calls, ["[wing status]"])
                    self.assertNotIn("&&", result.stderr)
                    continue
                self.assertEqual(calls, ["[wing status]", "[egg list]"])
                self.assertIn("long running research  egg-123", result.stderr)
                self.assertIn("release work  egg-456", result.stderr)
                commands = [line for line in result.stderr.splitlines() if "&&" in line]
                self.assertEqual(len(commands), 1, result.stderr)
                stop, start = [shlex.split(part) for part in commands[0].split("&&")]
                wt = ["env", "-u", "WINGTHING_PREVIEW_DIR", "WINGTHING_DIR=" + str(wing), str(root / "state/wt-pair")]
                self.assertEqual(stop, wt + ["wing", "stop"])
                self.assertEqual(start, wt + ["wing", "start"] +
                                 (["--local-only", "--listen", "0.0.0.0"] if mode == "local-only" else []))
                self.assertIn("left the running wing untouched", result.stderr)

    def test_legacy_restart_rejects_stale_or_unbound_status(self):
        status = "wing daemon is running (pid 123)\n  relay: disconnected\n"
        self.assertIsNone(support.legacy_restart_mode(status))
        with tempfile.TemporaryDirectory() as directory:
            wing = Path(directory)
            (wing / "wing.pid").write_text("123\n")
            live_ps = subprocess.CompletedProcess([], 0, "123 Thu Jan 1 00:00:00 2026\n", "")
            with patch.object(support.subprocess, "run", return_value=live_ps) as ps:
                for ts in ("2025-12-31T23:59:59Z", "2026-01-01T00:00:00Z", "invalid", "2999-01-01T00:00:00Z", None):
                    (wing / "wing.status").write_text(json.dumps({"state": "disconnected", "ts": ts}))
                    self.assertIsNone(support.legacy_restart_mode(status, wing), ts)
                (wing / "wing.status").write_text(json.dumps({"state": "disconnected", "ts": "2026-01-01T00:00:02Z"}))
                self.assertEqual(support.legacy_restart_mode(status, wing), "relay")
                self.assertEqual(ps.call_args.args[0], ["ps", "-p", "123", "-o", "pid=", "-o", "lstart="])
                self.assertEqual(ps.call_args.kwargs["env"]["TZ"], "UTC")
                self.assertEqual(ps.call_args.kwargs["env"]["LC_ALL"], "C")
                self.assertIsNone(support.legacy_restart_mode(status.replace("  relay: disconnected", "  local control: ready"), wing))
                (wing / "wing.pid").write_text("456\n")
                self.assertIsNone(support.legacy_restart_mode(status, wing))
                (wing / "wing.pid").write_text("123\n")
                ps.return_value = subprocess.CompletedProcess([], 0, "456 Thu Jan 1 00:00:00 2026\n", "")
                self.assertIsNone(support.legacy_restart_mode(status, wing))
                ps.side_effect = subprocess.TimeoutExpired("ps", 2)
                self.assertIsNone(support.legacy_restart_mode(status, wing))
            (wing / "wing.status").unlink()
            self.assertIsNone(support.legacy_restart_mode(status, wing))

    def test_device_selection_excludes_wireless_unpaired_and_non_phones(self):
        ipad = phone("ipad")
        ipad["hardwareProperties"]["productType"] = "iPad16,1"
        document = {"result": {"devices": [phone(), phone("wifi", "network"), phone("unpaired", pairing="unpaired"), ipad]}}
        self.assertEqual(support.connected_iphones(document), [("phone-one", "UDID-phone-one", "Test iPhone", "enabled")])

    def test_team_discovery_reads_only_provisioning_metadata(self):
        self.assertEqual(support.signing_teams({"IDEProvisioningTeams": {"account": [{"teamID": "TESTTEAM01"}]},
                                               "otherCredential": "SECRETTM01"}), ["TESTTEAM01"])

    def test_private_link_round_trip_and_malformed_input(self):
        self.assertEqual(support.setup_fields(link()), FIELDS)
        unusual = dict(FIELDS, token="fixture +/&=?%")
        self.assertEqual(support.setup_fields(link(unusual)), unusual)
        for bad in [link() + "&token=other", link() + "&unknown=1", link().replace("home?", "home/?"),
                    link(dict(FIELDS, origin="http://127.0.0.1:8080")), link(dict(FIELDS, origin="https://user:pass@home.example")),
                    link(dict(FIELDS, key="short")), link() + "%ZZ"]:
            with self.assertRaises(ValueError):
                support.setup_fields(bad)

    def test_public_identity_output_contains_no_bearer(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "setup"
            path.write_text(link()); path.chmod(0o600)
            output = io.StringIO()
            with patch.object(support.sys, "argv", ["helper", "identity", str(path)]), contextlib.redirect_stdout(output):
                support.main()
            self.assertNotIn("token", output.getvalue())
            path.chmod(0o644)
            with self.assertRaises(ValueError):
                support.read_setup(path)

    def test_home_verification_binds_bearer_account_and_array_roster_pin(self):
        class Response:
            status = 200
            def __init__(self, request, body):
                self.url, self.body = request.full_url, json.dumps(body).encode()
            def __enter__(self): return self
            def __exit__(self, *args): pass
            def read(self, count): return self.body[:count]

        bodies = {"/health": {"ok": True}, "/auth/check": {"ok": True, "user_id": "owner"},
                  "/api/app/wings": [{"wing_id": "mac", "public_key": FIELDS["key"]}]}
        calls = []
        class Opener:
            def open(self, request, timeout):
                self_check.assertEqual(request.headers["Authorization"], "Bearer " + FIELDS["token"])
                calls.append(request.full_url)
                return Response(request, bodies[urllib.parse.urlsplit(request.full_url).path])
        self_check = self
        with patch.object(support.urllib.request, "build_opener", return_value=Opener()):
            support.verify_home(FIELDS)
            self.assertEqual(len(calls), 3)
            bodies["/auth/check"]["user_id"] = "other"
            with self.assertRaises(ValueError): support.verify_home(FIELDS)
            bodies["/auth/check"]["user_id"] = "owner"
            bodies["/api/app/wings"][0]["public_key"] = "wrong"
            with self.assertRaises(ValueError): support.verify_home(FIELDS)
        self.assertIsNone(support.NoRedirects().redirect_request(None, None, 302, None, None, "https://other.example"))

    def test_live_mode_ignores_dead_daemon_files(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "wing.args").write_text("wing\nstart\n--foreground\n--local-only\n--labels=--listen")
            (root / "wing.status").write_text('{"roost_url":"https://dead.example"}')
            self.assertIsNone(support.running_mode(root))
            for mode in ("local-only", "relay", "", "connecting", None, True):
                with self.subTest(mode=mode), mode_server(mode) as control:
                    (root / "phone-control.json").write_text(json.dumps(control))
                    self.assertEqual(support.running_mode(root), mode if mode in ("local-only", "relay") else None)
                    # Identity mismatch and rejected control credentials are ambiguous.
                    for key in ("wing_id", "token"):
                        wrong = dict(control, **{key: "wrong"})
                        (root / "phone-control.json").write_text(json.dumps(wrong))
                        self.assertIsNone(support.running_mode(root))
                # The old control descriptor also cannot establish a dead daemon's mode.
                (root / "phone-control.json").write_text(json.dumps(control))
                self.assertIsNone(support.running_mode(root))

    def test_installer_reuses_team_and_builds_installs_launches_only_selected_device(self):
        with installer_fixture() as (root, clone, environment, command):
            mockbin = root / "mockbin"
            devices = root / "devices.json"
            for _ in range(2):
                result = subprocess.run(command, env=environment, text=True, capture_output=True)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            # The connect step uses the main wing and never shares an account bearer.
            fake_go = mockbin / "fake-go"
            fake_go.write_text("""#!/bin/bash
set -euo pipefail
if [[ $1 == env ]]; then exec "$MOCK_REAL_GO" "$@"; fi
printf '%s\\n' "$GOPROXY" >>"$MOCK_PROXY_CALLS"
while [[ $# -gt 0 ]]; do
 if [[ $1 == -o ]]; then target=$2; break; fi
 shift
done
cp "$MOCK_PAIR_BINARY" "$target"
""")
            fake_go.chmod(0o755)
            pair_impl = root / "pair-impl"
            pair_impl.write_text("""#!/bin/bash
printf '%s\\n' "$*" >>"$MOCK_PAIR_CALLS"
if [[ $1 == wing && $2 == status ]]; then
  if [[ $MOCK_WING_INITIAL != missing ]]; then
    printf 'wing daemon is running (pid 123)\\n'
    python3 - "$MOCK_CONTROL" "$MOCK_WING_INITIAL" "$WINGTHING_DIR/phone-control.json" <<'PYCONTROL'
import json, sys
control = json.loads(sys.argv[1])
control["address"] = sys.argv[2] + ":" + str(control.get("port", 0))
with open(sys.argv[3], "w") as output:
    json.dump(control, output)
PYCONTROL
  fi
fi
if [[ $1 == wing && $2 == start ]]; then
  address=127.0.0.1
  [[ $* != *'--listen 0.0.0.0'* ]] || address=0.0.0.0
  printf '{"address":"%s:6078"}\\n' "$address" >"$WINGTHING_DIR/phone-control.json"
fi
if [[ $1 == pair ]]; then
  grep -q '0.0.0.0' "$WINGTHING_DIR/phone-control.json" || exit 1
  printf 'Scan this with the Wingthing app\\n'
fi
""")
            launcher = root / "launcher.go"
            launcher.write_text('package main; import ("os"; "os/exec"); func main() { '
                                'c := exec.Command("/bin/bash", append([]string{os.Getenv("MOCK_PAIR_IMPL")}, os.Args[1:]...)...); '
                                'c.Stdout=os.Stdout; c.Stderr=os.Stderr; if c.Run()!=nil {os.Exit(1)} }')
            native = root / "native-pair"
            real_go = shutil.which("go")
            subprocess.run([real_go, "build", "-p", "2", "-o", str(native), str(launcher)],
                           check=True, env=dict(os.environ, GOTOOLCHAIN="local"), capture_output=True)
            environment.update(WT_IOS_GO=str(fake_go), MOCK_PAIR_CALLS=str(root / "pair-calls"),
                               MOCK_REAL_GO=real_go, MOCK_PAIR_BINARY=str(native), MOCK_PAIR_IMPL=str(pair_impl),
                               MOCK_PROXY_CALLS=str(root / "proxy-calls"))
            environment.pop("WT_IOS_SETUP_LINK_FILE", None)
            cases = [("missing", "unknown"), ("0.0.0.0", "unknown"),
                     ("127.0.0.1", "local-only"), ("[::1]", "unknown"),
                     ("[::ffff:127.0.0.1]", "relay"), ("127.0.0.1", "unknown"),
                     ("127.0.0.1", "relay"), ("127.0.0.1", "ambiguous")]
            for index, (initial, metadata) in enumerate(cases):
                with self.subTest(wing=initial, metadata=metadata):
                    wing = root / ("main-wing-" + str(index)); wing.mkdir()
                    environment["WT_IOS_WING_DIR"] = str(wing)
                    # Dead daemon files must never determine the restart mode.
                    (wing / "wing.args").write_text("wing\nstart\n--foreground\n--roost\nhttps://dead.example\n--labels=--listen")
                    (wing / "wing.status").write_text('{"roost_url":"https://dead.example"}')
                    environment["MOCK_WING_INITIAL"] = initial
                    (root / "pair-calls").write_text("")
                    with mode_server(metadata) as control:
                        environment["MOCK_CONTROL"] = json.dumps(control)
                        if metadata == "unknown":
                            environment["MOCK_CONTROL"] = '{"port":0}'
                        result = subprocess.run(command[:-1] + ["--connect-only"], env=environment,
                                                text=True, capture_output=True, timeout=30)
                    pair_calls = (root / "pair-calls").read_text().splitlines()
                    if initial not in ("missing", "0.0.0.0"):
                        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                        self.assertEqual(pair_calls, ["wing status"])
                        self.assertIn("listens on loopback only", result.stderr)
                        self.assertIn("WINGTHING_DIR=" + str(wing), result.stderr)
                        self.assertIn("wing stop", result.stderr)
                        starts = [shlex.split(line) for line in result.stderr.splitlines()
                                  if " wing start " in line]
                        if metadata in {"unknown", "ambiguous"}:
                            self.assertEqual(len(starts), 2, result.stderr)
                            self.assertIn("unavailable or ambiguous", result.stderr)
                            self.assertIn("wing start --local-only --listen 0.0.0.0", result.stderr)
                            self.assertIn("wing start --listen 0.0.0.0", result.stderr)
                        else:
                            self.assertEqual(len(starts), 1, result.stderr)
                            self.assertNotIn("unavailable or ambiguous", result.stderr)
                            restart = starts[0][starts[0].index("wing"):]
                            expected = ["wing", "start"]
                            if metadata == "local-only":
                                expected.append("--local-only")
                            self.assertEqual(restart, expected + ["--listen", "0.0.0.0"])
                        self.assertIn("must be added by hand", result.stderr)
                        self.assertNotIn("--labels", result.stderr)
                        self.assertNotIn("--roost", result.stderr)
                        self.assertNotIn("Press Enter", result.stderr)
                    else:
                        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                        expected = ["wing status"]
                        if initial == "missing":
                            expected.append("wing start --local-only --listen 0.0.0.0")
                        expected.append("pair")
                        self.assertEqual(pair_calls, expected)
                    self.assertFalse((root / "state/setup-link").exists())
            calls = (root / "calls").read_text()
            self.assertNotIn("--payload-url", calls)
            for expected in ["generic/platform=iOS", "DEVELOPMENT_TEAM=TESTTEAM01", "CODE_SIGNING_ALLOWED=YES",
                             "-allowProvisioningUpdates", "-allowProvisioningDeviceRegistration",
                             "device install app --device phone-one", "device process launch --device phone-one"]:
                self.assertIn(expected, calls)
            self.assertEqual((root / "state/team").read_text().strip(), "TESTTEAM01")
            devices.write_text(json.dumps({"result": {"devices": []}}))
            before = calls.count("DEVELOPMENT_TEAM=")
            result = subprocess.run(command, env=environment, text=True, capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual((root / "calls").read_text().count("DEVELOPMENT_TEAM="), before)


if __name__ == "__main__":
    unittest.main()
