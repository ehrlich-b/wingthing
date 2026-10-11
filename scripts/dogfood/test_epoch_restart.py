"""Restart, corruption, identity and hard-cap probes without native wt."""
import datetime
import hashlib
import json
import signal
import socket
import subprocess
import sys
import unittest

from contracts import CHECKS, timestamp, utc
import ledger
import test_epoch_ledger as samples
import test_forge_package as fixtures


class LedgerRestartTests(fixtures.ForgeFixture):
    def setUp(self):
        super().setUp()
        now = datetime.datetime.now(datetime.timezone.utc)
        self.header = ledger.epoch_header(timestamp(now - datetime.timedelta(minutes=1)),
                                         timestamp(now + datetime.timedelta(hours=2)), socket.gethostname())
        self.environment = {"WT_DOGFOOD_EPOCH": self.header["epoch"], "WT_DOGFOOD_UNTIL": self.header["until"]}
        self.path = self.logs / ledger.LEDGER_NAME

    def begin_only(self):
        ledger.create_ledger(self.path, self.header)
        attempt = ledger.begin_record(self.header, "interrupted", "interrupted.jsonl", self.header["epoch"], {})
        ledger.append_record(self.path, attempt, reserve_result=True)

    def assert_refused_unchanged(self):
        before = self.path.read_bytes()
        result = self.run_wrapper(environment=self.environment)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual(self.path.read_bytes(), before)
        self.assertFalse((self.repo / "invocations").exists())
        return result

    def test_durable_start_without_runner_state_is_an_interruption_hold(self):
        self.begin_only()
        self.assert_refused_unchanged()
        self.assertIsNotNone(ledger.cleanup_hold(ledger.checked(self.path)))
        self.assertEqual(list(self.logs.glob("*.jsonl")), [])

    def test_partial_json_tail_preserves_failed_cleanup_prefix(self):
        ledger.create_ledger(self.path, self.header)
        samples.append_receipt(self.path, self.header, "failed.jsonl",
                               samples.original_receipt(self.header["epoch"], cleanup="fail"))
        with self.path.open("ab") as stream:
            stream.write(b'{"kind":"result"')
        result = self.assert_refused_unchanged()
        self.assertIn("interrupted JSON tail", result.stdout)
        report = ledger.consume(ledger.read_ledger(self.path), utc(self.header["until"]))
        self.assertEqual(report["counts"]["failed_slots"], 1)
        self.assertEqual(report["ledger"]["integrity"], "FAIL")
        self.assertEqual(report["slots"][0]["attempts"][0]["result"]["cleanup_status"], "fail")

    def test_edited_complete_record_is_detected_without_repair(self):
        self.begin_only()
        before = self.path.read_bytes()
        self.path.write_bytes(before.replace(b'"host":', b'"host_changed":', 1))
        self.assertIn("digest mismatch", self.assert_refused_unchanged().stdout)

    def test_duplicate_attempt_and_duplicate_result_cannot_overwrite(self):
        ledger.create_ledger(self.path, self.header)
        attempt, result = samples.append_receipt(self.path, self.header, "one.jsonl",
                                                samples.original_receipt(self.header["epoch"]))
        before = self.path.read_bytes()
        for record in (attempt, result):
            with self.assertRaisesRegex(ValueError, "duplicate"):
                ledger.append_record(self.path, record)
            self.assertEqual(self.path.read_bytes(), before)

    def test_duplicate_raw_content_remains_one_unique_slot(self):
        ledger.create_ledger(self.path, self.header)
        raw = samples.original_receipt(self.header["epoch"])
        samples.append_receipt(self.path, self.header, "one.jsonl", raw)
        samples.append_receipt(self.path, self.header, "duplicate.jsonl", raw)
        report = ledger.consume(ledger.checked(self.path), utc(self.header["until"]))
        self.assertEqual(report["counts"]["attempted"], 2)
        self.assertEqual(report["counts"]["unique_slots"], 1)
        self.assertEqual(report["counts"]["duplicate_receipts"], 1)
        self.assertEqual(report["counts"]["fixture_pass_slots"], 1)
        self.assertTrue(report["attention_required"])

    def test_changed_binary_is_recorded_in_separate_cohorts(self):
        fake = self.scripts / "canary.sh"
        text = fake.read_text().replace('**extra)), flush=True)',
             'binary_sha256=__import__("hashlib").sha256((root / "wt").read_bytes()).hexdigest(), **extra)), flush=True)')
        self.assertNotEqual(text, fake.read_text())
        fake.write_text(text)
        binary = self.repo / "wt"
        binary.write_bytes(b"first offline build fixture")
        self.assertEqual(self.run_wrapper(environment=self.environment).returncode, 0)
        binary.write_bytes(b"second offline build fixture")
        self.assertEqual(self.run_wrapper(environment=self.environment).returncode, 0)
        view = ledger.checked(self.path)
        hashes = {r["binary_sha256"] for r in view["results"].values()}
        self.assertEqual(hashes, {hashlib.sha256(v).hexdigest() for v in
                                  (b"first offline build fixture", b"second offline build fixture")})
        self.assertTrue(all(r["binary_consistent"] for r in view["results"].values()))
        report = ledger.consume(view, utc(self.header["until"]))
        self.assertEqual(len(report["cohorts"]), 2)
        self.assertEqual(report["counts"]["duplicate_slots"], 1)

    def test_observed_binary_mismatch_is_unknown_even_with_green_checks(self):
        ledger.create_ledger(self.path, self.header)
        raw = samples.original_receipt(self.header["epoch"])
        records = [json.loads(line) for line in raw.splitlines()]
        attempt = ledger.begin_record(self.header, "mismatch", "mismatch.jsonl", self.header["epoch"],
                                      dict(records[-1], binary_sha256="a" * 64))
        ledger.append_record(self.path, attempt)
        ledger.append_record(self.path, ledger.result_record(attempt, records, hashlib.sha256(raw).hexdigest()))
        report = ledger.consume(ledger.checked(self.path), utc(self.header["until"]))
        self.assertEqual(report["counts"]["fixture_pass_slots"], 0)
        self.assertEqual(report["counts"]["unknown_slots"], 1)
        self.assertEqual(report["cohorts"][0]["started_binary_sha256"], "a" * 64)
        self.assertEqual(report["cohorts"][0]["observed_binary_sha256"], "b" * 64)

    def test_real_declared_total_cap_refuses_without_rotation_or_canary(self):
        # Thousands of metadata pairs only; never thousands of process launches.
        chunks, previous = ledger._sealed(self.header, 0, None)
        size = len(chunks)
        records = []
        for number in range(10000):
            name = "capacity-%d.jsonl" % number
            raw = samples.original_receipt(self.header["epoch"])
            parsed = [json.loads(line) for line in raw.splitlines()]
            attempt = ledger.begin_record(self.header, name.removesuffix(".jsonl"), name, self.header["epoch"], parsed[-1])
            result = ledger.result_record(attempt, parsed, hashlib.sha256(name.encode()).hexdigest())
            first, next_hash = ledger._sealed(attempt, len(records) + 1, previous)
            second, final_hash = ledger._sealed(result, len(records) + 2, next_hash)
            if size + len(first) + len(second) > ledger.TOTAL_LIMIT:
                break
            records.extend([attempt, result])
            size += len(first) + len(second)
            previous = final_hash
        self.assertGreater(size, ledger.TOTAL_LIMIT - ledger.RECORD_LIMIT)
        ledger.create_ledger(self.path, self.header, records)
        self.assertEqual(self.path.stat().st_size, size)
        before = hashlib.sha256(self.path.read_bytes()).hexdigest()
        run = self.run_wrapper(environment=self.environment)
        self.assertEqual(run.returncode, 1, run.stderr)
        self.assertIn("total cap", run.stdout)
        self.assertFalse((self.repo / "invocations").exists())
        self.assertEqual(hashlib.sha256(self.path.read_bytes()).hexdigest(), before)

    def test_oversize_file_and_record_are_detected(self):
        self.path.write_bytes(b"x" * (ledger.TOTAL_LIMIT + 1))
        self.assertIn("oversized ledger", self.assert_refused_unchanged().stdout)
        with self.assertRaisesRegex(ValueError, "record exceeds"):
            ledger._sealed({"payload": "x" * ledger.RECORD_LIMIT}, 0, None)
