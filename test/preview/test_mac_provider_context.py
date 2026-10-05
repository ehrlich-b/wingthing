import contextlib
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from mac_provider_context import native_hook_published, read_jsonl, stop_session, write_fake_provider


class MacProviderContextFixtureTest(unittest.TestCase):
    def test_jsonl_waits_for_complete_records(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "journal.jsonl"
            self.assertEqual(read_jsonl(path), [])
            path.write_text('{"type":"session_started"}\n{"type":"session_exit"')
            self.assertEqual(read_jsonl(path), [{"type": "session_started"}])
            with path.open("a") as output:
                output.write('}\n')
            self.assertEqual(read_jsonl(path), [{"type": "session_started"}, {"type": "session_exit"}])

    def test_fake_provider_reads_settings_file_and_runs_native_hook(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            calls, fake, settings = (root / name for name in ("calls.jsonl", "claude", "settings.json"))
            spool = root / "data" / ".claude" / "wingthing-events" / "egg"
            spool.mkdir(parents=True)
            published = spool / "seq.00000000000000000001.json"
            settings.write_text(json.dumps({"hooks": {"SessionStart": [{"hooks": [{
                "command": f"cat > '{published}'"}]}]}}))
            settings.chmod(0o600)
            write_fake_provider(fake, calls)
            env = dict(os.environ, HOME=str(root / "os-home"), CLAUDE_CONFIG_DIR=str(root / "data" / ".claude"))
            for args in (("--settings", str(settings)), ("--settings", "settings.json"),
                         ("--settings=" + str(settings),),
                         ("--settings", settings.read_text())):
                with self.subTest(args=args):
                    process = subprocess.Popen([str(fake), "--session-id", "ours", *args],
                                               env=env, cwd=root, stdout=subprocess.PIPE,
                                               stderr=subprocess.PIPE, text=True)
                    try:
                        self.assertEqual(process.stdout.readline().strip(), "FIXTURE_CONTEXT_READY")
                        self.assertTrue(native_hook_published(spool, "ours"))
                        self.assertFalse(native_hook_published(spool, "foreign"))
                        receipt = json.loads(calls.read_text().splitlines()[-1])
                        self.assertEqual(receipt["selectors"]["HOME"], str(root / "os-home"))
                        self.assertEqual(receipt["settings"], [json.loads(settings.read_text())])
                    finally:
                        process.terminate()
                        process.communicate(timeout=5)
            for args, route in ((["auth", "status", "--json"], "status"), (["-p", "fixture"], "headless")):
                result = subprocess.run([str(fake), *args], env=env, cwd=root,
                                        capture_output=True, text=True, timeout=5)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(json.loads(calls.read_text().splitlines()[-1])["route"], route)

    def test_cleanup_accepts_already_exited_session(self):
        result = subprocess.CompletedProcess([], 1, "", 'Error: session "ours" not found')
        with patch("mac_provider_context.subprocess.run", return_value=result):
            stop_session("wt", {}, ".", "ours")

    def test_cleanup_preserves_primary_failure_and_reports_other_errors(self):
        result = subprocess.CompletedProcess([], 1, "", "unexpected cleanup failure")
        with patch("mac_provider_context.subprocess.run", return_value=result):
            with self.assertRaisesRegex(AssertionError, "unexpected cleanup failure"):
                stop_session("wt", {}, ".", "ours")
            with contextlib.redirect_stderr(io.StringIO()) as output:
                with self.assertRaisesRegex(AssertionError, "original readiness failure"):
                    try:
                        raise AssertionError("original readiness failure")
                    finally:
                        stop_session("wt", {}, ".", "ours")
            self.assertIn("unexpected cleanup failure", output.getvalue())


if __name__ == "__main__":
    unittest.main()
