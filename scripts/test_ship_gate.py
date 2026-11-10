"""Tiny isolated git fixtures for receipt rejection; no Fly process is started."""
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("ship_gate", ROOT / "scripts/ship-gate.py")
ship = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ship)


class ReceiptTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.previous = Path.cwd()
        os.chdir(self.temp.name)
        self.addCleanup(os.chdir, self.previous)
        subprocess.run(["git", "init", "-q"], check=True)
        Path("source.txt").write_text("committed source\n")
        Path(".gitignore").write_text("/downloads/\n/receipt.txt\n/.ship-downloads/\n")
        subprocess.run(["git", "add", "."], check=True)
        env = dict(os.environ, GIT_AUTHOR_NAME="Bryan Ehrlich", GIT_AUTHOR_EMAIL="ehrlich.bryan@gmail.com",
                   GIT_COMMITTER_NAME="Bryan Ehrlich", GIT_COMMITTER_EMAIL="ehrlich.bryan@gmail.com")
        subprocess.run(["git", "commit", "-qm", "Create receipt validation fixture."], check=True, env=env)
        self.artifact = Path("downloads/v0.149.5/wt-linux-amd64")
        self.artifact.parent.mkdir(parents=True)
        self.artifact.write_bytes(b"receipt parser fixture, never executed")
        self.receipt = dict(version=1, head=ship.source(), platform="Linux", gates=ship.GATES,
                            baseline=str(self.artifact), baseline_sha256=ship.digest(self.artifact))
        self.write_receipt()

    def write_receipt(self):
        Path("receipt.txt").write_text(json.dumps(self.receipt))

    def test_valid_then_changed_head_source_and_artifact(self):
        ship.verify("receipt.txt")
        self.receipt["head"] = "0" * 40
        self.write_receipt()
        with self.assertRaisesRegex(ValueError, "does not match"):
            ship.verify("receipt.txt")
        self.receipt["head"] = ship.source()
        self.write_receipt()
        Path("source.txt").write_text("dirty\n")
        with self.assertRaisesRegex(ValueError, "clean source"):
            ship.verify("receipt.txt")

    def test_changed_release_artifact(self):
        self.artifact.write_bytes(b"changed")
        with self.assertRaisesRegex(ValueError, "changed"):
            ship.verify("receipt.txt")

    def test_incomplete_promotion_set(self):
        self.receipt["gates"] = ship.GATES[:-1]
        self.write_receipt()
        with self.assertRaisesRegex(ValueError, "promotion set"):
            ship.verify("receipt.txt")

    def test_dry_run_rejects_missing_and_mismatched_receipts_before_fly(self):
        # Use the real Makefile but run from this fixture repository.
        # The absolute script is read-only; all fixture writes stay here.
        makefile = (ROOT / "Makefile").read_text().replace("scripts/ship-gate.py", str(ROOT / "scripts/ship-gate.py"))
        Path("Makefile").write_text(makefile)
        subprocess.run(["git", "add", "Makefile"], check=True)
        env = dict(os.environ, GIT_AUTHOR_NAME="Bryan Ehrlich", GIT_AUTHOR_EMAIL="ehrlich.bryan@gmail.com",
                   GIT_COMMITTER_NAME="Bryan Ehrlich", GIT_COMMITTER_EMAIL="ehrlich.bryan@gmail.com")
        subprocess.run(["git", "commit", "-qm", "Add deployment preflight fixture."], check=True, env=env)
        for receipt in ("", "missing.txt", "receipt.txt"):
            with self.subTest(receipt=receipt):
                result = subprocess.run(["make", "-n", "deploy", "RECEIPT=" + receipt], text=True, capture_output=True)
                self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertNotIn("fly deploy", result.stdout + result.stderr)
                self.assertIn("ship-gate:", result.stderr)
        self.receipt["head"] = ship.source()
        self.write_receipt()
        result = subprocess.run(["make", "-n", "deploy", "RECEIPT=receipt.txt"], text=True, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("fly deploy", result.stdout)
        self.assertNotIn("make --no-print-directory check gate", result.stdout)

    def test_download_context_preserves_release_bytes(self):
        ship.stage_downloads()
        self.assertEqual((Path(".ship-downloads") / self.artifact.relative_to("downloads")).read_bytes(),
                         self.artifact.read_bytes())
        Path(".ship-downloads/stale").write_text("stale")
        with self.assertRaisesRegex(ValueError, "stale files"):
            ship.stage_downloads()


if __name__ == "__main__":
    unittest.main()
