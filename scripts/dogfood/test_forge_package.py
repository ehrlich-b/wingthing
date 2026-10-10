"""Offline functional fixtures for the finite forge package, never installed wt."""
import fcntl
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

from contracts import CHECKS

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[1]

FAKE_CANARY = '''#!/usr/bin/env python3
import json, pathlib, signal, sys
from contracts import CHECKS
root = pathlib.Path(__file__).resolve().parents[2]
mode = sys.argv[1] if len(sys.argv) > 1 else "success"
def emit(check, status="pass", **extra):
    print(json.dumps(dict(check=check, status=status, latency_ms=0,
                         error="fixture cleanup failure" if status == "fail" else None,
                         **extra)), flush=True)
def records():
    for n, check in enumerate(CHECKS[:-1]):
        emit(check, "skip" if n == 11 else "pass")
    emit("cleanup", "fail" if mode == "cleanup-fail" else "pass")
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
        else:
            shutil.rmtree(self.root)

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


class PreparationTests(ForgeFixture):
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


if __name__ == "__main__":
    unittest.main()
