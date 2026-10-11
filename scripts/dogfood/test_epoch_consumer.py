"""Hand-computed sparse epoch and whole-history status integration."""
import datetime
import hashlib
import json
import unittest

from contracts import timestamp, utc
import ledger
import status
import test_epoch_ledger as samples
import test_forge_package as fixtures


class LedgerConsumerTests(fixtures.ForgeFixture):
    def setUp(self):
        super().setUp()
        self.header = ledger.epoch_header("2026-10-10T10:00:00Z", "2026-10-10T16:00:00Z", "mac")
        self.path = self.logs / ledger.LEDGER_NAME
        ledger.create_ledger(self.path, self.header)
        self.now = utc(self.header["until"])

    def add(self, slot, suffix="", delay=0, failed=False, cleanup="pass", binary="b" * 64, skip=None):
        start = timestamp(utc(self.header["epoch"]) + datetime.timedelta(seconds=slot * 2700 + delay))
        name = utc(start).strftime("%Y%m%dT%H%M%S") + suffix + ".jsonl"
        raw = samples.original_receipt(start, failed=failed, cleanup=cleanup, binary=binary)
        if skip:
            records = [json.loads(line) for line in raw.splitlines()]
            next(r for r in records if r["check"] == skip)["status"] = "skip"
            raw = "".join(json.dumps(r) + "\n" for r in records).encode()
        (self.logs / name).write_bytes(raw)
        return samples.append_receipt(self.path, self.header, name, raw)

    def sparse(self):
        self.add(1)
        self.add(3, failed=True)
        self.add(3, "retry", delay=60)
        self.add(4, skip="wt_claude_scoped_mailbox")
        self.add(5, delay=70)
        start = timestamp(ledger.planned_at(self.header, 6))
        attempt = ledger.begin_record(self.header, "interrupted", "interrupted.jsonl", start,
                                     {"binary_sha256": "b" * 64, "source_commit": "c" * 40, "source_sha256": "d" * 64})
        ledger.append_record(self.path, attempt, reserve_result=True)

    def test_literal_sparse_oracle_sees_first_middle_final_and_sleep_gaps(self):
        self.sparse()
        result = ledger.consume(ledger.checked(self.path), self.now)
        expected = {"planned_slots": 8, "due_slots": 8, "attempted": 6, "unique_slots": 5,
                    "completed_attempts": 5, "completed_slots": 4, "fixture_pass_slots": 2,
                    "failed_slots": 1, "skipped_slots": 1, "missing_slots": 3, "unknown_slots": 1,
                    "pending_slots": 0, "late_slots": 1, "duplicate_slots": 1, "duplicate_attempts": 1,
                    "duplicate_receipts": 0, "unassigned_attempts": 0}
        self.assertEqual(result["counts"], expected)
        self.assertEqual([slot["status"] for slot in result["slots"]],
                         ["missing", "completed", "missing", "failed", "skipped", "completed", "unknown", "missing"])
        self.assertEqual(result["actual_coverage"]["fraction_of_due_slots"], 0.25)
        self.assertIsNone(result["actual_coverage"]["healthy_interval_seconds"])
        self.assertFalse(result["all_green"])
        self.assertTrue(result["attention_required"])
        self.assertEqual(result["judgment_scope"]["outside_host_acceptance"], "REQUIRED-UNRUN")

    def test_status_latest_green_does_not_hide_whole_epoch_failure(self):
        self.sparse()
        result, code = status.report(self.logs, self.now)
        self.assertEqual(code, 1)
        self.assertEqual(result["last_complete_run"]["status"], "pass")
        self.assertEqual(result["epoch_evidence"]["counts"]["failed_slots"], 1)
        self.assertEqual([gap["slot"] for gap in result["schedule"]["gaps"]], [0, 2, 7])
        self.assertFalse(result["all_green"])

    def test_new_good_build_does_not_hide_bad_build(self):
        self.add(0, failed=True, binary="a" * 64)
        self.add(1, binary="b" * 64)
        result = ledger.consume(ledger.checked(self.path), self.now)
        self.assertEqual(len(result["cohorts"]), 2)
        self.assertEqual([cohort["failed"] for cohort in result["cohorts"]], [1, 0])
        self.assertEqual([cohort["slots"] for cohort in result["cohorts"]], [[0], [1]])
        self.assertEqual(result["counts"]["failed_slots"], 1)

    def test_pending_slots_do_not_claim_missing_before_their_grace(self):
        self.add(0)
        result = ledger.consume(ledger.checked(self.path), utc("2026-10-10T10:45:30Z"))
        self.assertEqual(result["counts"]["missing_slots"], 0)
        self.assertEqual(result["counts"]["pending_slots"], 7)
        self.assertEqual(result["counts"]["due_slots"], 1)
        self.assertFalse(result["attention_required"])

    def test_outcome_after_expiry_joins_original_slot(self):
        start = "2026-10-10T15:59:59Z"
        name = "final.jsonl"
        records = [json.loads(line) for line in samples.original_receipt(start).splitlines()]
        records[-1]["timestamp"] = "2026-10-10T16:01:00Z"
        raw = "".join(json.dumps(r) + "\n" for r in records).encode()
        samples.append_receipt(self.path, self.header, name, raw)
        before = ledger.consume(ledger.checked(self.path), self.now)
        after = ledger.consume(ledger.checked(self.path), utc("2026-10-10T16:01:00Z"))
        self.assertEqual(before["counts"]["completed_slots"], 0)
        self.assertEqual(after["counts"]["completed_slots"], 1)
        self.assertEqual(after["counts"]["planned_slots"], 8)
        self.assertEqual(after["slots"][-1]["attempts"][0]["slot"], 7)

    def test_native_fixture_and_live_judgments_are_distinct(self):
        self.add(0)
        result = ledger.consume(ledger.checked(self.path), self.now)
        judgment = result["slots"][0]["attempts"][0]["judgments"]
        self.assertEqual(judgment["fixture"], "pass")
        self.assertTrue(all(v == "pass" for v in judgment["native_checks"].values()))
        self.assertEqual(judgment["live_checks"], {"real_codex": "skip", "remembered_ssh_connect": "skip"})
        self.assertEqual(result["judgment_scope"]["outside_host_acceptance"], "REQUIRED-UNRUN")

    def test_edited_raw_receipt_cannot_borrow_original_metadata(self):
        for slot in range(8):
            self.add(slot)
        report, code = status.report(self.logs, self.now)
        self.assertEqual(code, 0)
        before = self.path.read_bytes()
        latest = sorted(self.logs.glob("*.jsonl"))[-1]
        raw = latest.read_bytes()
        latest.write_bytes(raw.replace(b"SECRET-DIAGNOSTIC", b"EDITED-DIAGNOSTIC"))
        report, code = status.report(self.logs, self.now)
        self.assertEqual(code, 1)
        self.assertTrue(any("differs from ledger metadata" in value for value in report["diagnostics"]))
        self.assertEqual(self.path.read_bytes(), before)
