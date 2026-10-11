"""Independent arithmetic expectations for a finite epoch."""
import datetime
import unittest

from contracts import utc
from ledger import due_slots, epoch_header, lateness, plan, planned_at, slot_for


class EpochContractTests(unittest.TestCase):
    def setUp(self):
        self.contract = plan("2026-10-10T10:00:00Z", "2026-10-10T13:00:00Z")

    def test_four_half_open_slots_are_fixed_even_long_after_expiry(self):
        self.assertEqual(self.contract["slot_count"], 4)
        self.assertEqual([planned_at(self.contract, n).strftime("%H:%M") for n in range(4)],
                         ["10:00", "10:45", "11:30", "12:15"])
        self.assertEqual(due_slots(self.contract, utc("2027-01-01T00:00:00Z")), [0, 1, 2, 3])
        self.assertIsNone(slot_for(self.contract, "2026-10-10T13:00:00Z"))
        self.assertIsNone(slot_for(self.contract, "2026-10-10T09:59:59Z"))

    def test_late_attempt_cannot_backfill_a_missed_slot(self):
        self.assertEqual(slot_for(self.contract, "2026-10-10T11:40:00Z"), 2)
        self.assertEqual(lateness(self.contract, "2026-10-10T11:40:00Z"),
                         {"slot": 2, "delay_seconds": 600, "late": True})
        self.assertEqual(slot_for(self.contract, "2026-10-10T11:41:00Z"), 2)
        self.assertEqual(due_slots(self.contract, utc("2026-10-10T11:41:00Z")), [0, 1, 2])

    def test_first_slot_boot_allowance_and_subsequent_slack(self):
        for actual, late in (("10:03:00", False), ("10:03:00.000001", True),
                             ("10:46:00", False), ("10:46:00.000001", True)):
            self.assertEqual(lateness(self.contract, "2026-10-10T" + actual + "Z")["late"], late)
        self.assertEqual(due_slots(self.contract, utc("2026-10-10T10:03:00Z")), [])
        self.assertEqual(due_slots(self.contract, utc("2026-10-10T10:03:01Z")), [0])

    def test_short_final_slot_is_counted_once(self):
        for end, count in (("10:45:00Z", 1), ("10:45:00.000001Z", 2), ("10:00:00.000001Z", 1)):
            value = plan("2026-10-10T10:00:00Z", "2026-10-10T" + end)
            self.assertEqual(value["slot_count"], count)
            self.assertEqual(len(due_slots(value, utc("2026-10-11T00:00:00Z"))), count)

    def test_expiry_or_cadence_change_changes_identity(self):
        first = epoch_header("2026-10-10T10:00:00Z", "2026-10-10T13:00:00Z", "mac")
        second = epoch_header("2026-10-10T10:00:00Z", "2026-10-10T13:45:00Z", "mac")
        self.assertNotEqual(first["epoch_id"], second["epoch_id"])
        self.assertEqual(first, epoch_header(first["epoch"], first["until"], "mac"))

    def test_invalid_unbounded_or_civil_timestamps_are_refused(self):
        for end in ("2026-10-10T10:00:00Z", "2099-01-01T00:00:00Z", "2026-10-10T13:00:00-04:00"):
            with self.assertRaises(ValueError):
                plan("2026-10-10T10:00:00Z", end)
        with self.assertRaises(ValueError):
            planned_at(self.contract, 4)
        with self.assertRaises(ValueError):
            epoch_header(self.contract["epoch"], self.contract["until"], "x" * 129)
