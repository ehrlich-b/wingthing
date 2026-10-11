"""Two-host scale replay verified by a separate arithmetic implementation."""
import hashlib
import json
import subprocess
import sys
import unittest

import epoch_oracle
import ledger
import rehearse_epoch
import test_forge_package as fixtures
from contracts import utc


class EpochScaleTests(fixtures.ForgeFixture):
    def test_896_host_slots_match_independent_consumer_and_bounds(self):
        out = self.root / "replay"
        packet = rehearse_epoch.rehearse(out)
        self.assertTrue(packet["synthetic_fixture_only"])
        self.assertEqual(sum(v["counts"]["planned_slots"] for v in packet["hosts"].values()), 896)
        expected = {
            "mac-fixture": {"attempted": 446, "unique_slots": 445, "completed_attempts": 445,
                            "completed_slots": 444, "fixture_pass_slots": 442, "failed_slots": 1,
                            "skipped_slots": 1, "missing_slots": 3, "unknown_slots": 1, "duplicate_slots": 1},
            "forge-fixture": {"attempted": 446, "unique_slots": 446, "completed_attempts": 446,
                              "completed_slots": 446, "fixture_pass_slots": 443, "failed_slots": 1,
                              "skipped_slots": 1, "missing_slots": 2, "unknown_slots": 1, "duplicate_slots": 0}}
        for host, entry in packet["hosts"].items():
            path = out / entry["ledger"]
            oracle = epoch_oracle.inspect(path, utc(rehearse_epoch.UNTIL))
            self.assertEqual(oracle["counts"], entry["counts"])
            for key, value in expected[host].items():
                self.assertEqual(entry["counts"][key], value, host + ": " + key)
            self.assertLess(path.stat().st_size, ledger.TOTAL_LIMIT)
            self.assertTrue(all(len(line) + 1 <= ledger.RECORD_LIMIT for line in path.read_bytes().splitlines()))
            self.assertEqual(hashlib.sha256(path.read_bytes()).hexdigest(), entry["sha256"])
            self.assertEqual(sum(v["attempted"] for v in entry["cohorts"]), entry["counts"]["attempted"])
            self.assertEqual(sum(v["failed"] for v in entry["cohorts"]), 1)
            self.assertTrue(entry["attention_required"])
        arguments = [sys.executable, "-B", str(fixtures.HERE / "epoch_oracle.py"), "--now", rehearse_epoch.UNTIL]
        for host in sorted(packet["hosts"]):
            arguments += ["--ledger", str(out / packet["hosts"][host]["ledger"])]
        result = subprocess.run(arguments, capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual([h["host"] for h in json.loads(result.stdout)["hosts"]], ["forge-fixture", "mac-fixture"])

    def test_replay_is_byte_stable_and_operator_inputs_are_preserved(self):
        first, second = self.root / "first", self.root / "second"
        packet = rehearse_epoch.rehearse(first)
        other = rehearse_epoch.rehearse(second)
        self.assertEqual(packet, other)
        note = first / "operator-note"
        note.write_text("preserve original operator file")
        before = {p.relative_to(first).as_posix(): (p.read_bytes(), p.stat().st_mtime_ns)
                  for p in first.rglob("*") if p.is_file()}
        with self.assertRaisesRegex(ValueError, "preserved"):
            rehearse_epoch.rehearse(first)
        after = {p.relative_to(first).as_posix(): (p.read_bytes(), p.stat().st_mtime_ns)
                 for p in first.rglob("*") if p.is_file()}
        self.assertEqual(before, after)

    def test_oracle_rejects_wrong_slot_join_and_unended_epoch(self):
        out = self.root / "replay"
        rehearse_epoch.rehearse(out)
        path = out / "mac-fixture" / ledger.LEDGER_NAME
        with self.assertRaisesRegex(ValueError, "ended"):
            epoch_oracle.inspect(path, utc(rehearse_epoch.EPOCH))
        view = ledger.checked(path)
        records = []
        for key, attempt in view["attempts"].items():
            records.append(attempt)
            if key in view["results"]:
                records.append(view["results"][key])
        # A rehashed but incorrectly joined fixture must fail independent arithmetic.
        records[0] = dict(records[0], slot=0)
        previous = None
        chunks = []
        for seq, record in enumerate([view["header"], *records]):
            chunk, previous = ledger._sealed(record, seq, previous)
            chunks.append(chunk)
        corrupt = self.root / "wrong-slot.ndjson"
        corrupt.write_bytes(b"".join(chunks))
        with self.assertRaisesRegex(ValueError, "slot join"):
            epoch_oracle.inspect(corrupt, utc(rehearse_epoch.UNTIL))
