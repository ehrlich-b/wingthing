"""Local, synthetic checks of receipt admission and the real mirror validator."""
import argparse
import contextlib
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("release_verify", Path(__file__).with_name("release-verify.py"))
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


class ReleaseVerificationTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.dist = self.root / "dist"
        self.dist.mkdir()
        for name in release.ASSETS:
            (self.dist / name).write_text(f"synthetic {name}\n")
        (self.dist / "SHA256SUMS").write_text("".join(
            f"{release.digest(self.dist / name)}  {name}\n" for name in release.ASSETS))
        log = self.root / "check.log"
        log.write_text("synthetic passing log; no real release gates run\n")
        self.receipt = self.root / "gate.json"
        self.value = {"head": "fixture-head", "tag": "v0.0.0", "profile": "local",
                      "dry_run": True, "commands": [{"command": ["make", "check"],
                      "exit_code": 0, "log": str(log), "sha256": release.digest(log)}]}
        self.save()
        self.args = argparse.Namespace(tag="v0.0.0", dry_run=True, dist=str(self.dist),
                                       gate=[str(self.receipt)], output=str(self.root / "verified"))
        self.source = patch.object(release, "source", return_value="fixture-head")
        self.source.start()
        self.addCleanup(self.source.stop)

    def save(self):
        self.receipt.write_text(json.dumps(self.value))

    def test_complete_synthetic_release_and_receipt(self):
        with contextlib.redirect_stdout(io.StringIO()):
            release.package(self.args)
        receipt = json.loads((Path(self.args.output) / "receipt.json").read_text())
        self.assertEqual(receipt["head"], "fixture-head")
        self.assertTrue(receipt["dry_run"])
        self.assertFalse(receipt["published"])
        self.assertEqual(receipt["assets"], {n: release.digest(self.dist / n)
                         for n in release.ASSETS + ["SHA256SUMS"]})

    def test_bad_binary_checksum_rejected_by_real_validator(self):
        (self.dist / release.ASSETS[0]).write_text("corrupt bytes\n")
        with self.assertRaises(subprocess.CalledProcessError):
            release.package(self.args)
        self.assertFalse(Path(self.args.output).exists())
        # A failed destination is reusable after repairing the inputs.
        (self.dist / release.ASSETS[0]).write_text(f"synthetic {release.ASSETS[0]}\n")
        release.package(self.args)
        receipt = (Path(self.args.output) / "receipt.json").read_bytes()
        with self.assertRaises(FileExistsError):
            release.package(self.args)
        self.assertEqual((Path(self.args.output) / "receipt.json").read_bytes(), receipt)

    def test_foreign_head_failed_gate_and_changed_command_rejected(self):
        for field, value in (("head", "foreign-head"), ("tag", "v1.0.0")):
            with self.subTest(field=field):
                original = self.value[field]
                self.value[field] = value
                self.save()
                with self.assertRaises(ValueError):
                    release.package(self.args)
                self.value[field] = original
        for field, value in (("exit_code", 1), ("command", ["true"])):
            self.value["commands"][0][field] = value
            self.save()
            with self.assertRaises(ValueError):
                release.package(self.args)

    def test_missing_profile_names_receipt_and_does_not_stage(self):
        del self.value["profile"]
        self.save()
        with self.assertRaisesRegex(ValueError, "missing or unsupported gate profile.*gate.json"):
            release.package(self.args)
        self.assertFalse(Path(self.args.output).exists())

    def test_source_change_during_packaging_cleans_output(self):
        with patch.object(release, "source", side_effect=["fixture-head", "changed-head"]):
            with self.assertRaisesRegex(ValueError, "source or staged assets changed"):
                release.package(self.args)
        self.assertFalse(Path(self.args.output).exists())

    def test_source_errors_explain_missing_and_prerelease_tags(self):
        self.source.stop()
        with self.assertRaisesRegex(ValueError, "release tag must be vMAJOR.MINOR.PATCH"):
            release.source("v0.0.0-rc1", False)
        def git_output(command, **kwargs):
            if command[:3] == ["git", "status", "--porcelain"]:
                return ""
            if "--verify" in command:
                raise subprocess.CalledProcessError(128, command)
            return "fixture-head"
        with patch.object(release, "run", side_effect=git_output):
            with self.assertRaisesRegex(ValueError, "release tag is missing or invalid: v0.0.0"):
                release.source("v0.0.0", False)

    def test_modified_log_rejected(self):
        Path(self.value["commands"][0]["log"]).write_text("tampered log\n")
        with self.assertRaisesRegex(ValueError, "log checksum"):
            release.package(self.args)

    def test_silent_success_log_is_valid_but_empty_binary_is_not(self):
        log = Path(self.value["commands"][0]["log"])
        log.write_bytes(b"")
        self.assertEqual(release.digest(log, allow_empty=True), hashlib.sha256(b"").hexdigest())
        self.value["commands"][0]["sha256"] = release.digest(log, allow_empty=True)
        self.save()
        with contextlib.redirect_stdout(io.StringIO()):
            release.package(self.args)
        binary = self.dist / release.ASSETS[0]
        binary.write_bytes(b"")
        with self.assertRaises(ValueError):
            release.digest(binary)

    def test_dry_run_cannot_replace_release_profiles(self):
        self.args.dry_run = False
        with self.assertRaisesRegex(ValueError, "required gate profiles"):
            release.package(self.args)


if __name__ == "__main__":
    unittest.main()
