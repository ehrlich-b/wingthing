"""Offline functional fixtures for the finite forge package, never installed wt."""
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import shutil
import shlex
import signal
import subprocess
import sys
import tempfile
import time
from types import SimpleNamespace
import unittest

from contracts import CHECKS

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[1]

FAKE_CANARY = '''#!/usr/bin/env python3
import json, pathlib, signal, sys
from contracts import CHECKS
root = pathlib.Path(__file__).resolve().parents[2]
mode = sys.argv[1] if len(sys.argv) > 1 else "success"
with (root / "invocations").open("a") as stream:
    stream.write(mode + "\\n")
def emit(check, status="pass", **extra):
    print(json.dumps(dict(check=check, status=status, latency_ms=0,
                         error="fixture cleanup failure" if status == "fail" else None,
                         **extra)), flush=True)
def records():
    for n, check in enumerate(CHECKS[:-1]):
        emit(check, "skip" if n == 11 else "pass", resource_env={key: __import__('os').environ.get(key) for key in ('GOMAXPROCS', 'GOFLAGS', 'TMPDIR')})
    emit("cleanup", "fail" if mode == "cleanup-fail" else "skip" if mode == "cleanup-skip" else "pass")
if mode == "timeout":
    signal.signal(signal.SIGTERM, lambda *_: (records(), sys.exit(0)))
    (root / "ready").touch()
    signal.pause()
else:
    if mode == "cleanup-fail":
        (root / "retained-state").mkdir(exist_ok=True)
    records()
    sys.exit(7 if mode == "nonzero" else (1 if mode == "cleanup-fail" else 0))
'''


class ForgeFixture(unittest.TestCase):
    def setUp(self):
        scratch = REPO / ".scratch"
        scratch.mkdir(exist_ok=True)
        self.root = Path(tempfile.mkdtemp(prefix="forge-test-", dir=scratch))
        self.additional_roots = []
        self.repo = self.root / "checkout with spaces"
        self.scripts = self.repo / "scripts/dogfood"
        self.scripts.mkdir(parents=True)
        shutil.copyfile(HERE / "run.py", self.scripts / "run.py")
        shutil.copyfile(HERE / "contracts.py", self.scripts / "contracts.py")
        fake = self.scripts / "canary.sh"
        fake.write_text(FAKE_CANARY.replace("#!/usr/bin/env python3", "#!" + sys.executable))
        fake.chmod(0o700)
        self.logs = self.repo / ".scratch/dogfood-logs"
        self.logs.mkdir(parents=True)

    def tearDown(self):
        result = getattr(self._outcome, "result", None)
        failed = result and any(test is self for test, _ in result.errors + result.failures)
        if failed:
            print("Failed fixture retained: " + str(self.root), file=sys.stderr)
            for root in self.additional_roots:
                print("Failed canary layout fixture retained: " + str(root), file=sys.stderr)
        else:
            shutil.rmtree(self.root)
            for root in self.additional_roots:
                shutil.rmtree(root)

    def run_wrapper(self, mode="success", timeout=720, environment=None):
        # Only the fixture API overrides the clock bound; CLI always uses 12 minutes.
        code = ("import importlib.util, pathlib, sys; "
                "sys.path.insert(0, str(pathlib.Path(sys.argv[1]).parent)); "
                "spec=importlib.util.spec_from_file_location('runner', sys.argv[1]); "
                "m=importlib.util.module_from_spec(spec); spec.loader.exec_module(m); "
                "sys.exit(m.run_once(pathlib.Path(sys.argv[2]), [sys.argv[3]], "
                "timeout_seconds=float(sys.argv[4]), cleanup_seconds=2))")
        env = {key: value for key, value in os.environ.items() if not key.startswith("WT_DOGFOOD_")}
        env.update(environment or {})
        return subprocess.run([sys.executable, "-B", "-c", code,
                               str(self.scripts / "run.py"), str(self.repo), mode, str(timeout)],
                              capture_output=True, text=True, timeout=10, env=env)

    def receipt(self):
        receipts = sorted(self.logs.glob("*.jsonl"))
        self.assertTrue(receipts)
        return [json.loads(line) for line in receipts[-1].read_text().splitlines()]


class WrapperTests(ForgeFixture):
    def test_canary_fixture_keeps_private_layout_and_short_socket(self):
        binary = self.root / "fake-wt"
        binary.write_text("never executed by this layout fixture\n")
        spec = importlib.util.spec_from_file_location("canary_layout_fixture", HERE / "canary.py")
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        canary = module.Canary(SimpleNamespace(wt=binary))
        self.additional_roots.append(canary.root)
        self.assertEqual(canary.home, canary.root)
        self.assertEqual(canary.state.parent, canary.home)
        self.assertEqual(canary.binary.parent, canary.home)
        self.assertNotEqual(canary.state.parent, canary.work)
        self.assertEqual(canary.root.stat().st_mode & 0o777, 0o700)
        self.assertEqual(canary.state.stat().st_mode & 0o777, 0o700)
        self.assertEqual((canary.state / "wing.yaml").stat().st_mode & 0o777, 0o600)
        socket = canary.state / "eggs" / ("f" * 16) / "egg.sock"
        self.assertLessEqual(len(str(socket).encode()), 103 if sys.platform == "darwin" else 107)

    def test_success_and_optional_skip(self):
        result = self.run_wrapper()
        self.assertEqual(result.returncode, 0, result.stderr)
        records = self.receipt()
        self.assertEqual(len(records), 14)
        self.assertEqual(records[-1]["checks"], 13)
        self.assertEqual(records[-1]["status"], "pass")
        self.assertEqual(records[-3]["status"], "skip")

    def test_nonzero_exit_is_failure(self):
        result = self.run_wrapper("nonzero")
        self.assertEqual(result.returncode, 1)
        self.assertEqual(self.receipt()[-1]["exit_code"], 7)
        self.assertEqual(self.receipt()[-1]["status"], "fail")

    def test_watchdog_allows_cleanup_but_still_fails(self):
        result = self.run_wrapper("timeout", timeout=0.2)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual(self.receipt()[-2]["check"], "cleanup")
        self.assertEqual(self.receipt()[-2]["status"], "pass")
        self.assertEqual(self.receipt()[-1]["status"], "fail")

    def test_overlapping_invocation_skips_without_receipt(self):
        with (self.logs / "run.lock").open("a") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            result = self.run_wrapper()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout)["status"], "skip")
        self.assertEqual(list(self.logs.glob("*.jsonl")), [])

    def test_stale_lock_contents_do_not_lock_or_delete_state(self):
        (self.logs / "run.lock").write_text("stale owner 999999\n")
        retained = self.repo / "unrelated-recent-state"
        retained.mkdir()
        self.assertEqual(self.run_wrapper().returncode, 0)
        self.assertTrue(retained.is_dir())

    def test_cleanup_failure_retains_fixture_state_and_receipt(self):
        result = self.run_wrapper("cleanup-fail")
        self.assertEqual(result.returncode, 1)
        self.assertTrue((self.repo / "retained-state").is_dir())
        self.assertEqual(self.receipt()[-2]["status"], "fail")
        self.assertEqual(self.receipt()[-1]["status"], "fail")
        previous_files = list(self.logs.glob("*.jsonl"))
        repeat = self.run_wrapper()
        self.assertEqual(repeat.returncode, 1)
        self.assertIn("unresolved previous cleanup", repeat.stdout)
        self.assertEqual(list(self.logs.glob("*.jsonl")), previous_files)

    def test_overlap_preserves_an_earlier_failure(self):
        self.assertEqual(self.run_wrapper("nonzero").returncode, 1)
        with (self.logs / "run.lock").open("a") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            result = self.run_wrapper()
        self.assertEqual(result.returncode, 1)
        self.assertTrue(json.loads(result.stdout)["previous_failure"])

    def test_interrupted_stale_state_refuses_and_is_preserved(self):
        state = {"schema": 1, "phase": "running", "cleanup_status": "unknown"}
        (self.logs / "runner-state.json").write_text(json.dumps(state))
        result = self.run_wrapper()
        self.assertEqual(result.returncode, 1)
        self.assertEqual(json.loads((self.logs / "runner-state.json").read_text()), state)
        self.assertEqual(list(self.logs.glob("*.jsonl")), [])

    def test_expiry_refuses_without_starting_and_preserves_failure(self):
        environment = {"WT_DOGFOOD_EPOCH": "2000-01-01T00:00:00Z", "WT_DOGFOOD_UNTIL": "2000-01-02T00:00:00Z"}
        self.assertEqual(self.run_wrapper(environment=environment).returncode, 3)
        self.assertEqual(list(self.logs.glob("*.jsonl")), [])
        self.assertEqual(self.run_wrapper("nonzero").returncode, 1)
        self.assertEqual(self.run_wrapper(environment=environment).returncode, 1)
        self.assertEqual(len(list(self.logs.glob("*.jsonl"))), 1)

    def test_partial_finite_environment_refuses(self):
        result = self.run_wrapper(environment={"WT_DOGFOOD_UNTIL": "2099-01-01T00:00:00Z"})
        self.assertEqual(result.returncode, 1)
        self.assertEqual(list(self.logs.glob("*.jsonl")), [])

    def test_unknown_cleanup_cannot_be_cleared_by_another_run(self):
        self.assertEqual(self.run_wrapper("cleanup-skip").returncode, 1)
        self.assertEqual(self.run_wrapper().returncode, 1)
        self.assertEqual((self.repo / "invocations").read_text().splitlines(), ["cleanup-skip"])

    def test_corrupt_state_refuses_and_preserves_evidence(self):
        file = self.logs / "runner-state.json"
        file.write_text('{"schema":1,"phase":"invalid","cleanup_status":"pass"}\n')
        before = file.read_bytes()
        self.assertEqual(self.run_wrapper().returncode, 1)
        self.assertEqual(file.read_bytes(), before)

    def test_stale_temporary_state_does_not_get_overwritten(self):
        file = self.logs / "runner-state.new"
        file.write_text("unrelated earlier diagnostic")
        self.assertEqual(self.run_wrapper().returncode, 0)
        self.assertEqual(file.read_text(), "unrelated earlier diagnostic")

    def test_receipts_rotate_to_24_and_are_private(self):
        legacy = [{"check": check, "status": "pass", "latency_ms": 0, "error": None} for check in CHECKS]
        legacy.append({"check": "runner", "status": "pass", "latency_ms": 0, "error": None})
        for n in range(25):
            (self.logs / ("20000101T000000.%06dZ.jsonl" % n)).write_text(
                "".join(json.dumps(record) + "\n" for record in legacy))
        self.assertEqual(self.run_wrapper().returncode, 0)
        receipts = sorted(self.logs.glob("*.jsonl"))
        self.assertEqual(len(receipts), 24)
        self.assertLessEqual(receipts[-1].stat().st_size, 256 * 1024)
        self.assertEqual(receipts[-1].stat().st_mode & 0o777, 0o600)


class PackageFixture(ForgeFixture):
    def setUp(self):
        super().setUp()
        shutil.copyfile(HERE / "run.sh", self.scripts / "run.sh")
        (self.scripts / "run.sh").chmod(0o700)
        (self.repo / "wt").write_text("#!/bin/sh\nexit 99\n")
        (self.repo / "wt").chmod(0o700)
        self.package = self.root / "package with spaces"

    def prepare(self, until="2099-01-01T00:00:00Z"):
        return subprocess.run([sys.executable, "-B", str(HERE / "prepare-forge.py"),
                               "--repo", str(self.repo), "--out", str(self.package),
                               "--until", until], capture_output=True, text=True, timeout=5)


class PreparationTests(PackageFixture):
    def test_generate_private_package_with_spaces_without_execution(self):
        result = self.prepare()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(json.loads(result.stdout)["installed"])
        manifest = json.loads((self.package / "manifest.json").read_text())
        self.assertEqual(manifest["repo"], str(self.repo))
        self.assertEqual(self.package.stat().st_mode & 0o777, 0o700)
        for name in manifest["files"]:
            self.assertEqual((self.package / name).stat().st_mode & 0o077, 0)
        syntax = subprocess.run(["bash", "-n", str(self.package / "install.sh")], capture_output=True)
        self.assertEqual(syntax.returncode, 0, syntax.stderr)
        self.assertEqual(subprocess.run(["bash", "-n", str(self.package / "remove.sh")]).returncode, 0)
        env = subprocess.run(["bash", "-c", 'source "$1"; printf "%s\\n" "$WT_DOGFOOD_REPO" "$WT_DOGFOOD_UNTIL"',
                              "fixture", str(self.package / "forge.env")], capture_output=True, text=True)
        self.assertEqual(env.stdout.splitlines(), [str(self.repo), "2099-01-01T00:00:00Z"])
        service = (self.package / "wingthing-dogfood.service").read_text()
        self.assertIn('$${WT_DOGFOOD_REPO}', service)
        self.assertIn("UMask=0077", service)
        self.assertIn("KillMode=control-group", service)
        self.assertNotIn("set-environment", (self.package / "install.sh").read_text())

    def test_missing_binary_refuses_without_creating_output(self):
        (self.repo / "wt").unlink()
        result = self.prepare()
        self.assertEqual(result.returncode, 2)
        self.assertIn("missing checkout-local executable", result.stderr)
        self.assertFalse(self.package.exists())

    def test_expired_or_non_utc_deadline_refuses(self):
        for until in ("2000-01-01T00:00:00Z", "2099-01-01T00:00:00", "2099-01-01T00:00:00-04:00"):
            with self.subTest(until=until):
                self.assertEqual(self.prepare(until).returncode, 2)
                self.assertFalse(self.package.exists())

    def test_existing_output_is_preserved(self):
        self.package.mkdir()
        edit = self.package / "operator-note"
        edit.write_text("keep this")
        result = self.prepare()
        self.assertEqual(result.returncode, 2)
        self.assertIn("preserved", result.stderr)
        self.assertEqual(edit.read_text(), "keep this")

    def test_repeated_preparation_keeps_epoch_and_mtimes(self):
        self.assertEqual(self.prepare().returncode, 0)
        before = {file.name: (file.read_bytes(), file.stat().st_mtime_ns) for file in self.package.iterdir()}
        result = self.prepare()
        self.assertEqual(result.returncode, 0, result.stderr)
        after = {file.name: (file.read_bytes(), file.stat().st_mtime_ns) for file in self.package.iterdir()}
        self.assertEqual(before, after)

    def test_operator_edits_and_deadline_changes_refuse(self):
        self.assertEqual(self.prepare().returncode, 0)
        self.assertEqual(self.prepare("2099-01-02T00:00:00Z").returncode, 2)
        edit = self.package / "10-forge.conf"
        edit.write_text(edit.read_text() + "# operator edit\n")
        result = self.prepare()
        self.assertEqual(result.returncode, 2)
        self.assertIn("operator edits preserved", result.stderr)
        self.assertTrue(edit.read_text().endswith("# operator edit\n"))

    def test_shell_metacharacters_in_checkout_path_are_inert(self):
        new_repo = self.root / 'checkout % $() " \\ and apostrophe\''
        self.repo.rename(new_repo)
        self.repo = new_repo
        self.scripts = self.repo / "scripts/dogfood"
        self.logs = self.repo / ".scratch/dogfood-logs"
        result = self.prepare()
        self.assertEqual(result.returncode, 0, result.stderr)
        loaded = subprocess.run(["bash", "-c", 'source "$1"; printf "%s" "$WT_DOGFOOD_REPO"',
                                 "fixture", str(self.package / "forge.env")], capture_output=True, text=True)
        self.assertEqual(loaded.returncode, 0, loaded.stderr)
        self.assertEqual(loaded.stdout, str(self.repo))
        dropin = (self.package / "10-forge.conf").read_text()
        self.assertIn("%%", dropin)
        self.assertEqual(subprocess.run(["bash", "-n", str(self.package / "install.sh")]).returncode, 0)


class StatusTests(ForgeFixture):
    def write_receipt(self, start="2026-10-10T10:00:00Z", platform="darwin", overrides=None,
                      until=None, epoch=None):
        overrides = overrides or {}
        records = []
        for check in CHECKS:
            status = "skip" if check in ("remembered_ssh_connect", "real_codex") else "pass"
            if platform == "linux" and check.startswith("wt_claude_"):
                status = "unsupported-on-linux"
            record = dict(check=check, status=status, latency_ms=1, error=None,
                          timestamp=start, platform=platform, binary_sha256="b" * 64)
            record.update(overrides.get(check, {}))
            records.append(record)
        failed = any(record["status"] == "fail" for record in records)
        records.append(dict(check="runner", status="fail" if failed else "pass", latency_ms=1,
                            error="fixture failure" if failed else None, timestamp=start,
                            started_at=start, platform=platform, checks=13, exit_code=1 if failed else 0,
                            binary_sha256="b" * 64, source_commit="c" * 40, source_sha256="d" * 64,
                            epoch=epoch, until=until))
        file = self.logs / (start.replace(":", "").replace("-", "") + ".jsonl")
        file.write_text("".join(json.dumps(record) + "\n" for record in records))
        return file

    def status(self, now="2026-10-10T10:30:00Z"):
        result = subprocess.run([sys.executable, "-B", str(HERE / "status.py"), "--logs", str(self.logs),
                                 "--now", now, "--json"], capture_output=True, text=True, timeout=5)
        self.assertFalse(result.stderr, result.stderr)
        return result, json.loads(result.stdout)

    def test_missing_logs_have_documented_nonzero_status(self):
        result, report = self.status()
        self.assertEqual(result.returncode, 2)
        self.assertEqual(report["overall"], "missing-runs")
        self.assertIsNone(report["last_complete_run"])
        self.assertFalse(report["all_green"])

    def test_optional_skips_are_limited_fixture_coverage(self):
        self.write_receipt()
        result, report = self.status()
        self.assertEqual(result.returncode, 0)
        self.assertEqual(report["overall"], "fixture-pass-limited-acceptance")
        self.assertFalse(report["all_green"])
        self.assertEqual(set(report["optional_unavailable"]), {"remembered_ssh_connect", "real_codex"})
        self.assertEqual(report["last_complete_run"]["binary_sha256"], "b" * 64)
        self.assertEqual(report["last_complete_run"]["source_sha256"], "d" * 64)

    def test_required_mac_skip_remains_unavailable(self):
        self.write_receipt(overrides={"wt_claude_scoped_mailbox": {"status": "skip", "reason": "nested sandbox denied"}})
        result, report = self.status()
        self.assertEqual(result.returncode, 1)
        self.assertIn("wt_claude_scoped_mailbox", report["required_unavailable"])

    def test_linux_unsupported_checks_are_explicit(self):
        self.write_receipt(platform="linux")
        result, report = self.status()
        self.assertEqual(result.returncode, 0)
        self.assertEqual(len(report["unsupported"]), 2)
        self.assertFalse(report["all_green"])
        self.assertEqual(report["checks"]["wt_claude_scoped_mailbox"]["requirement"], "unsupported")

    def test_gaps_report_missing_schedule_and_latest_run(self):
        self.write_receipt()
        self.write_receipt(start="2026-10-10T11:30:00Z")
        result, report = self.status("2026-10-10T12:30:00Z")
        self.assertEqual(result.returncode, 1)
        self.assertEqual([gap["kind"] for gap in report["schedule"]["gaps"]], ["between", "latest"])
        self.assertEqual(report["last_complete_run"]["started_at"], "2026-10-10T11:30:00Z")

    def test_expiry_caps_expected_gaps(self):
        self.write_receipt(epoch="2026-10-10T10:00:00Z", until="2026-10-10T10:45:00Z")
        result, report = self.status("2026-10-10T23:00:00Z")
        self.assertEqual(result.returncode, 0)
        self.assertTrue(report["schedule"]["expired"])
        self.assertEqual(report["schedule"]["gaps"], [])

    def test_failed_cleanup_and_incomplete_latest_attempt_remain_visible(self):
        self.write_receipt(overrides={"cleanup": {"status": "fail", "error": "retained fixture /private/example"}})
        result, report = self.status()
        self.assertEqual(result.returncode, 1)
        self.assertEqual(report["last_cleanup"]["status"], "fail")
        partial = self.logs / "20261010T101000Z.jsonl"
        partial.write_text('{"check":"local_only_start","status":"pass"}\n')
        result, report = self.status()
        self.assertEqual(result.returncode, 1)
        self.assertEqual(report["complete_runs"], 1)
        self.assertEqual(report["last_cleanup"]["status"], "unknown")
        self.assertEqual(report["last_attempt"], str(partial))

    def test_malformed_duplicate_and_unknown_identity_never_pass(self):
        file = self.write_receipt()
        records = [json.loads(line) for line in file.read_text().splitlines()]
        records[-1]["source_sha256"] = None
        file.write_text("".join(json.dumps(record) + "\n" for record in records))
        self.assertEqual(self.status()[0].returncode, 1)
        records[1] = records[0]
        file.write_text("".join(json.dumps(record) + "\n" for record in records))
        self.assertEqual(self.status()[0].returncode, 2)
        file.write_text("not JSON\n")
        self.assertEqual(self.status()[0].returncode, 2)


class GeneratedUnitTests(PackageFixture):
    def setUp(self):
        super().setUp()
        self.unit_dir = self.root / "private user units"
        self.bin_dir = self.root / "fake commands"
        self.bin_dir.mkdir()
        self.capture = self.root / "systemctl.jsonl"
        command = self.bin_dir / "systemctl"
        command.write_text("#!" + sys.executable + '''
import json, os, sys
with open(os.environ['FIXTURE_SYSTEMCTL_CAPTURE'], 'a') as stream:
    stream.write(json.dumps(sys.argv[1:]) + '\\n')
''')
        command.chmod(0o700)
        self.env = dict(os.environ, PATH=str(self.bin_dir) + os.pathsep + os.environ.get("PATH", ""),
                        FIXTURE_SYSTEMCTL_CAPTURE=str(self.capture))

    def calls(self):
        return [json.loads(line) for line in self.capture.read_text().splitlines()] if self.capture.exists() else []

    def package_script(self, action):
        return subprocess.run(["bash", str(self.package / (action + ".sh")), str(self.unit_dir)],
                              env=self.env, capture_output=True, text=True, timeout=5)

    def test_install_reload_only_then_remove_exact_owned_files(self):
        self.assertEqual(self.prepare().returncode, 0)
        installed = self.package_script("install")
        self.assertEqual(installed.returncode, 0, installed.stderr)
        self.assertEqual(self.calls(), [["--user", "daemon-reload"]])
        self.assertTrue((self.unit_dir / "wingthing-dogfood.service.d/10-forge.conf").is_file())
        self.assertEqual((self.unit_dir / "wingthing-dogfood.service").stat().st_mode & 0o777, 0o600)
        unrelated = self.unit_dir / "operator.service"
        unrelated.write_text("preserve")
        removed = self.package_script("remove")
        self.assertEqual(removed.returncode, 0, removed.stderr)
        self.assertEqual(self.calls()[1:], [["--user", "disable", "--now", "wingthing-dogfood.timer"],
                                           ["--user", "stop", "wingthing-dogfood.service"],
                                           ["--user", "daemon-reload"]])
        self.assertEqual(unrelated.read_text(), "preserve")
        self.assertFalse((self.unit_dir / "wingthing-dogfood.service").exists())

    def test_installed_operator_edit_refuses_before_any_call_or_write(self):
        self.assertEqual(self.prepare().returncode, 0)
        self.assertEqual(self.package_script("install").returncode, 0)
        target = self.unit_dir / "wingthing-dogfood.service.d/10-forge.conf"
        target.write_text("# deliberate operator override\n")
        before = self.calls()
        for action in ("install", "remove"):
            result = self.package_script(action)
            self.assertEqual(result.returncode, 2)
            self.assertIn("Operator edits preserved", result.stderr)
            self.assertEqual(self.calls(), before)
            self.assertEqual(target.read_text(), "# deliberate operator override\n")

    def test_package_edit_refuses_without_systemctl(self):
        self.assertEqual(self.prepare().returncode, 0)
        (self.package / "wingthing-dogfood.timer").write_text("operator timer edit\n")
        result = self.package_script("install")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Edited package file", result.stderr)
        self.assertEqual(self.calls(), [])
        self.assertFalse(self.unit_dir.exists())

    def test_service_environment_and_expiry_with_bounded_systemd_parser_fixture(self):
        self.assertEqual(self.prepare().returncode, 0)
        environment = dict(self.env)
        for line in (self.package / "10-forge.conf").read_text().splitlines():
            if line.startswith("Environment="):
                key, value = shlex.split(line.split("=", 1)[1])[0].replace("%%", "%").split("=", 1)
                environment[key] = value
        service = (self.package / "wingthing-dogfood.service").read_text()
        # Emulate only the generated subset. This is NOT Linux/systemd acceptance.
        command = next(line.split("=", 1)[1] for line in service.splitlines() if line.startswith("ExecStart="))
        argv = shlex.split(command.replace("$$", "$"))
        self.assertEqual(argv[:2], ["/bin/sh", "-c"])
        result = subprocess.run(argv, env=environment, capture_output=True, text=True, timeout=5)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.receipt()[0]["resource_env"]["GOMAXPROCS"], "2")
        self.assertEqual(self.receipt()[0]["resource_env"]["GOFLAGS"], "-p=2")
        self.assertEqual(self.receipt()[0]["resource_env"]["TMPDIR"], str(self.repo / ".scratch/tmp"))
        for directive in ("Nice=15", "UMask=0077", "TimeoutStartSec=15min", "TimeoutStopSec=60s",
                          "KillMode=control-group", "SuccessExitStatus=3"):
            self.assertIn(directive, service)
        timer = (self.package / "wingthing-dogfood.timer").read_text()
        self.assertIn("OnUnitActiveSec=45min", timer)
        self.assertIn("AccuracySec=1min", timer)
        self.assertIn("Persistent=false", timer)
        environment.update(WT_DOGFOOD_EPOCH="2000-01-01T00:00:00Z", WT_DOGFOOD_UNTIL="2000-01-02T00:00:00Z")
        result = subprocess.run(argv, env=environment, capture_output=True, text=True, timeout=5)
        self.assertEqual(result.returncode, 3, result.stderr)
        self.assertEqual((self.repo / "invocations").read_text().splitlines(), ["success"])

    def test_actual_concurrent_wrapper_never_starts_second_canary(self):
        command = [sys.executable, "-B", str(self.scripts / "run.py"), "timeout"]
        first = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        try:
            deadline = time.monotonic() + 3
            while not (self.repo / "ready").exists() and first.poll() is None and time.monotonic() < deadline:
                time.sleep(0.01)
            self.assertTrue((self.repo / "ready").exists())
            result = self.run_wrapper()
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(json.loads(result.stdout)["status"], "skip")
            self.assertEqual((self.repo / "invocations").read_text().splitlines(), ["timeout"])
        finally:
            first.send_signal(signal.SIGTERM)
            stdout, stderr = first.communicate(timeout=5)
        self.assertEqual(first.returncode, 1, stderr)
        self.assertEqual(self.receipt()[-2]["check"], "cleanup")
        self.assertEqual(self.receipt()[-2]["status"], "pass")
        self.assertEqual(self.receipt()[-1]["status"], "fail")


if __name__ == "__main__":
    unittest.main()
