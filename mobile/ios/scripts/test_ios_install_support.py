import contextlib
import io
import json
import os
from pathlib import Path
import plistlib
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
import urllib.parse

import ios_install_support as support


FIELDS = {"v": "1", "origin": "https://home.example", "account": "owner", "wing": "mac",
          "key": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", "token": "fixture-private-token"}


def link(fields=FIELDS):
    return "wingthing://home?" + urllib.parse.urlencode(fields, quote_via=urllib.parse.quote)


def phone(identifier="phone-one", transport="local", pairing="paired", mode="enabled"):
    return {"identifier": identifier, "hardwareProperties": {"productType": "iPhone18,1", "udid": "UDID-" + identifier},
            "connectionProperties": {"transportType": transport, "pairingState": pairing},
            "deviceProperties": {"name": "Test iPhone", "developerModeStatus": mode}}


class InstallerTests(unittest.TestCase):
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

    def test_installer_reuses_team_and_builds_installs_launches_only_selected_device(self):
        clone = Path(__file__).resolve().parents[3]
        scratch = clone / ".scratch/ios"
        scratch.mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory(dir=scratch) as directory:
            root = Path(directory)
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
                "defaults": 'cp "$MOCK_PREFS" "$3"',
                "taskpolicy": 'shift; exec "$@"',
                "nice": 'shift 2; exec "$@"',
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
            for name in ("WT_IOS_TEAM", "WT_IOS_BUNDLE", "WT_IOS_DEVICE", "WT_IOS_CLONE"):
                environment.pop(name, None)
            command = ["/bin/bash", str(clone / "mobile/ios/scripts/install_phone.sh"), "--install-only"]
            for _ in range(2):
                result = subprocess.run(command, env=environment, text=True, capture_output=True)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            # Exercise private handoff too, with HTTP verification covered above.
            source = root / "private-link"
            source.write_text(link()); source.chmod(0o600)
            environment.update(WT_IOS_SETUP_LINK_FILE=str(source), REAL_PYTHON=sys.executable,
                               MOCK_CLIPBOARD=str(root / "clipboard"))
            for name, body in {
                "python3": 'if [[ ${2:-} == verify ]]; then exit 0; fi\nexec "$REAL_PYTHON" "$@"',
                "pbcopy": 'cat >"$MOCK_CLIPBOARD"',
            }.items():
                path = mockbin / name
                path.write_text("#!/bin/bash\nset -euo pipefail\n" + body + "\n"); path.chmod(0o755)
            result = subprocess.run(command[:-1] + ["--connect-only"], env=environment, text=True, capture_output=True)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertNotIn(FIELDS["token"], result.stdout + result.stderr)
            self.assertEqual((root / "clipboard").read_text(), link())
            self.assertFalse((root / "state/setup-link").exists())
            self.assertFalse((root / "state/identity-link").exists())
            calls = (root / "calls").read_text()
            self.assertNotIn(FIELDS["token"], calls)
            self.assertIn("--payload-url wingthing://home?", calls)
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
