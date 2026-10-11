"""UTC microsecond and repeated civil-hour probes with independent oracle."""
import datetime
import unittest
from zoneinfo import ZoneInfo

from contracts import timestamp, utc
import epoch_oracle
import ledger
import test_epoch_ledger as samples
import test_forge_package as fixtures


class EpochBoundaryTests(fixtures.ForgeFixture):
    def test_repeated_civil_hour_keeps_four_distinct_utc_slots(self):
        header = ledger.epoch_header("2026-11-01T05:00:00Z", "2026-11-01T08:00:00Z", "dst-mac-fixture")
        path = self.logs / ledger.LEDGER_NAME
        ledger.create_ledger(path, header)
        zone = ZoneInfo("America/New_York")
        civil = []
        for slot in range(4):
            actual = ledger.planned_at(header, slot)
            local = actual.astimezone(zone)
            civil.append((local.strftime("%H:%M%z"), local.fold))
            samples.append_receipt(path, header, "dst-%d.jsonl" % slot,
                                   samples.original_receipt(timestamp(actual)))
        self.assertEqual(civil, [("01:00-0400", 0), ("01:45-0400", 0), ("01:30-0500", 1), ("02:15-0500", 0)])
        report = ledger.consume(ledger.checked(path), utc(header["until"]))
        oracle = epoch_oracle.inspect(path, utc(header["until"]))
        self.assertEqual(report["counts"], oracle["counts"])
        self.assertEqual(report["counts"]["planned_slots"], 4)
        self.assertEqual(report["counts"]["fixture_pass_slots"], 4)
        self.assertEqual(len({slot["planned_at"] for slot in report["slots"]}), 4)
        self.assertEqual(ledger.consume(ledger.checked(path), utc("2026-11-02T08:00:00Z"))["counts"], report["counts"])

    def test_microsecond_start_cadence_and_expiry_boundaries(self):
        header = ledger.epoch_header("2026-11-01T05:00:00Z", "2026-11-01T08:00:00Z", "dst-mac-fixture")
        cases = [("04:59:59.999999Z", None), ("05:00:00Z", 0), ("05:44:59.999999Z", 0),
                 ("05:45:00Z", 1), ("06:29:59.999999Z", 1), ("06:30:00Z", 2),
                 ("07:59:59.999999Z", 3), ("08:00:00Z", None), ("08:00:00.000001Z", None)]
        for text, expected in cases:
            self.assertEqual(ledger.slot_for(header, "2026-11-01T" + text), expected)
        self.assertEqual(ledger.due_slots(header, utc(header["until"])), [0, 1, 2, 3])

    def test_utc_spelling_canonicalizes_but_local_offset_is_rejected(self):
        first = ledger.epoch_header("2026-11-01T05:00:00Z", "2026-11-01T08:00:00Z", "mac")
        second = ledger.epoch_header("2026-11-01T05:00:00+00:00", "2026-11-01T08:00:00+00:00", "mac")
        self.assertEqual(first, second)
        for local in ("2026-11-01T01:00:00-04:00", "2026-11-01T01:00:00-05:00", "2026-11-01T01:00:00"):
            with self.assertRaises(ValueError):
                ledger.slot_for(first, local)

    def test_short_epoch_final_slot_becomes_due_without_increasing_schedule(self):
        header = ledger.epoch_header("2026-11-01T05:00:00Z", "2026-11-01T05:00:00.000001Z", "mac")
        self.assertEqual(header["slot_count"], 1)
        self.assertEqual(ledger.due_slots(header, utc(header["epoch"])), [])
        self.assertEqual(ledger.due_slots(header, utc(header["until"])), [0])
        self.assertIsNone(ledger.slot_for(header, header["until"]))

    def test_completion_before_start_refuses_without_mutation(self):
        header = ledger.epoch_header("2026-11-01T05:00:00Z", "2026-11-01T08:00:00Z", "mac")
        path = self.logs / ledger.LEDGER_NAME
        ledger.create_ledger(path, header)
        attempt, result = samples.append_receipt(path, header, "valid.jsonl", samples.original_receipt(header["epoch"]))
        before = path.read_bytes()
        result = dict(result, attempt_id="second", finished_at="2026-11-01T04:59:59Z")
        second = dict(attempt, attempt_id="second", receipt="second.jsonl")
        ledger.append_record(path, second)
        after_begin = path.read_bytes()
        with self.assertRaisesRegex(ValueError, "completion precedes"):
            ledger.append_record(path, result)
        self.assertEqual(path.read_bytes(), after_begin)
        self.assertTrue(after_begin.startswith(before))
