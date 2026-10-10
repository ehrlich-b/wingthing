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

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[1]

FAKE_CANARY = '''#!/usr/bin/env python3
import json, pathlib, signal, sys
root = pathlib.Path(__file__).resolve().parents[2]
mode = sys.argv[1] if len(sys.argv) > 1 else "success"
def emit(check, status="pass", **extra):
    print(json.dumps(dict(check=check, status=status, latency_ms=0,
                         error="fixture cleanup failure" if status == "fail" else None,
                         **extra)), flush=True)
def records():
    for n in range(12):
        emit("fixture_" + str(n), "skip" if n == 11 else "pass")
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

    def run_wrapper(self, mode="success", timeout=720):
        # Only the fixture API overrides the clock bound; CLI always uses 12 minutes.
        code = ("import importlib.util, pathlib, sys; "
                "spec=importlib.util.spec_from_file_location('runner', sys.argv[1]); "
                "m=importlib.util.module_from_spec(spec); spec.loader.exec_module(m); "
                "sys.exit(m.run_once(pathlib.Path(sys.argv[2]), [sys.argv[3]], "
                "timeout_seconds=float(sys.argv[4]), cleanup_seconds=2))")
        return subprocess.run([sys.executable, "-B", "-c", code,
                               str(self.scripts / "run.py"), str(self.repo), mode, str(timeout)],
                              capture_output=True, text=True, timeout=10)

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

    def test_receipts_rotate_to_24_and_are_private(self):
        for n in range(25):
            (self.logs / ("20000101T000000.%06dZ.jsonl" % n)).write_text("{}\n")
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


if __name__ == "__main__":
    unittest.main()
