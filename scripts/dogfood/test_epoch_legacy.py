"""Conservative imports preserve unknown history and original/operator bytes."""
import datetime
import hashlib
import json
import shutil
import socket
import subprocess
import sys
import unittest

from contracts import timestamp, utc
import epoch_oracle
import ledger
import test_epoch_ledger as samples
import test_forge_package as fixtures

EPOCH = "2026-10-10T00:00:00Z"
UNTIL = "2026-10-24T00:00:00Z"


def complete_records(header, slots):
    for slot in slots:
        start = timestamp(ledger.planned_at(header, slot))
        name = "receipt-%03d.jsonl" % slot
        raw = samples.original_receipt(start)
        parsed = [json.loads(line) for line in raw.splitlines()]
        attempt = ledger.begin_record(header, name.removesuffix(".jsonl"), name, start, parsed[-1])
        yield attempt
        yield ledger.result_record(attempt, parsed, hashlib.sha256(raw).hexdigest())


class LegacyEpochTests(fixtures.ForgeFixture):
    def setUp(self):
        super().setUp()
        self.input = self.root / "immutable-legacy-input"
        self.input.mkdir()
        self.output = self.root / "import-output"
        self.output.mkdir()
        self.header = ledger.epoch_header(EPOCH, UNTIL, "legacy-mac-fixture", "legacy-import")

    def snapshot(self):
        return {p.name: (p.read_bytes(), p.stat().st_mtime_ns) for p in self.input.iterdir()}

    def import_cli(self, out=None, until=UNTIL):
        return subprocess.run([sys.executable, "-B", str(fixtures.HERE / "ledger.py"), "import-legacy",
                               "--logs", str(self.input), "--out", str(out or self.output / ledger.LEDGER_NAME),
                               "--epoch", EPOCH, "--until", until, "--host", "legacy-mac-fixture"],
                              capture_output=True, text=True, timeout=5)

    def put(self, slot):
        raw = samples.original_receipt(timestamp(ledger.planned_at(self.header, slot)))
        path = self.input / ("receipt-%03d.jsonl" % slot)
        path.write_bytes(raw)
        return path

    def test_24_retained_receipts_leave_424_slots_unknown(self):
        for slot in range(424, 448):
            self.put(slot)
        before = self.snapshot()
        result = self.import_cli()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.snapshot(), before)
        path = self.output / ledger.LEDGER_NAME
        report = ledger.consume(ledger.checked(path), utc(UNTIL))
        oracle = epoch_oracle.inspect(path, utc(UNTIL))
        self.assertEqual(report["counts"], oracle["counts"])
        self.assertEqual(report["counts"]["planned_slots"], 448)
        self.assertEqual(report["counts"]["attempted"], 24)
        self.assertEqual(report["counts"]["fixture_pass_slots"], 24)
        self.assertEqual(report["counts"]["unknown_slots"], 424)
        self.assertEqual(report["counts"]["missing_slots"], 0)
        self.assertTrue(report["attention_required"])
        self.assertFalse(report["all_green"])
        full_header = ledger.epoch_header(EPOCH, UNTIL, "legacy-mac-fixture")
        full_path = self.logs / ledger.LEDGER_NAME
        ledger.create_ledger(full_path, full_header, complete_records(full_header, range(448)))
        complete = ledger.consume(ledger.checked(full_path), utc(UNTIL))
        self.assertEqual(complete["counts"], epoch_oracle.inspect(full_path, utc(UNTIL))["counts"])
        self.assertEqual(complete["counts"]["fixture_pass_slots"], 448)
        self.assertEqual(complete["counts"]["unknown_slots"], 0)
        self.assertNotEqual(report["epoch"]["epoch_id"], complete["epoch"]["epoch_id"])
        self.assertEqual(report["actual_coverage"]["fraction_of_due_slots"], 24 / 448)
        self.assertEqual(self.snapshot(), before)

    def test_duplicate_legacy_receipt_never_adds_unique_coverage(self):
        original = self.put(447)
        shutil.copyfile(original, self.input / "duplicate.jsonl")
        self.assertEqual(self.import_cli().returncode, 0)
        report = ledger.consume(ledger.checked(self.output / ledger.LEDGER_NAME), utc(UNTIL))
        self.assertEqual(report["counts"]["attempted"], 2)
        self.assertEqual(report["counts"]["unique_slots"], 1)
        self.assertEqual(report["counts"]["duplicate_receipts"], 1)
        self.assertEqual(report["counts"]["unknown_slots"], 447)

    def test_partial_legacy_receipt_is_unknown_and_cleanup_hold(self):
        path = self.put(447)
        lines = path.read_bytes().splitlines(keepends=True)
        path.write_bytes(b"".join(lines[:3]))
        before = self.snapshot()
        self.assertEqual(self.import_cli().returncode, 0)
        view = ledger.checked(self.output / ledger.LEDGER_NAME)
        report = ledger.consume(view, utc(UNTIL))
        self.assertEqual(report["counts"]["completed_slots"], 0)
        self.assertEqual(report["counts"]["unknown_slots"], 448)
        self.assertIsNotNone(ledger.cleanup_hold(view))
        self.assertEqual(self.snapshot(), before)

    def test_missing_timestamp_corrupt_tail_and_25_inputs_refuse_without_overwrite(self):
        for mode in ("missing-start", "partial-json", "too-many"):
            with self.subTest(mode=mode):
                for p in self.input.iterdir():
                    p.unlink()
                path = self.put(447)
                if mode == "missing-start":
                    path.write_bytes(b'{"check":"cleanup","status":"pass"}\n')
                elif mode == "partial-json":
                    with path.open("ab") as stream:
                        stream.write(b'{"check":')
                else:
                    for slot in range(423, 447):
                        self.put(slot)
                before = self.snapshot()
                output = self.output / (mode + ".ndjson")
                result = self.import_cli(output)
                self.assertEqual(result.returncode, 2, result.stderr)
                self.assertFalse(output.exists())
                self.assertEqual(self.snapshot(), before)

    def test_existing_output_deadline_and_operator_files_remain_intact(self):
        self.put(447)
        self.assertEqual(self.import_cli().returncode, 0)
        note = self.output / "operator-note"
        note.write_text("preserve this operator input")
        path = self.output / ledger.LEDGER_NAME
        before = path.read_bytes(), path.stat().st_mtime_ns
        result = self.import_cli(until="2026-10-25T00:00:00Z")
        self.assertEqual(result.returncode, 2)
        self.assertEqual((path.read_bytes(), path.stat().st_mtime_ns), before)
        self.assertEqual(note.read_text(), "preserve this operator input")

    def test_runner_bootstrap_freezes_prior_finite_state_even_without_raw_history(self):
        now = datetime.datetime.now(datetime.timezone.utc)
        epoch = timestamp(now - datetime.timedelta(hours=1))
        until = timestamp(now + datetime.timedelta(hours=1))
        state = {"schema": 1, "phase": "complete", "cleanup_status": "pass", "previous_failure": False,
                 "epoch": epoch, "until": until}
        state_path = self.logs / "runner-state.json"
        state_path.write_text(json.dumps(state))
        before = state_path.read_bytes()
        bad_environment = {"WT_DOGFOOD_EPOCH": epoch, "WT_DOGFOOD_UNTIL": timestamp(now + datetime.timedelta(hours=2))}
        result = self.run_wrapper(environment=bad_environment)
        self.assertEqual(result.returncode, 1)
        self.assertIn("immutable", result.stdout)
        self.assertEqual(state_path.read_bytes(), before)
        self.assertFalse((self.logs / ledger.LEDGER_NAME).exists())
        self.assertFalse((self.repo / "invocations").exists())
        view = ledger.ensure_epoch(self.logs, epoch, until, socket.gethostname())
        self.assertEqual(view["header"]["origin"], "legacy-import")
        report = ledger.consume(view, utc(until))
        self.assertEqual(report["counts"]["unknown_slots"], 3)
        self.assertEqual(report["counts"]["missing_slots"], 0)

    def test_expired_ledger_keeps_failure_after_a_later_green_result(self):
        now = datetime.datetime.now(datetime.timezone.utc)
        header = ledger.epoch_header(timestamp(now - datetime.timedelta(hours=3)),
                                     timestamp(now - datetime.timedelta(hours=1)), socket.gethostname())
        path = self.logs / ledger.LEDGER_NAME
        ledger.create_ledger(path, header)
        samples.append_receipt(path, header, "failed.jsonl", samples.original_receipt(header["epoch"], failed=True))
        samples.append_receipt(path, header, "recovered.jsonl", samples.original_receipt(timestamp(ledger.planned_at(header, 1))))
        before = path.read_bytes()
        result = self.run_wrapper(environment={"WT_DOGFOOD_EPOCH": header["epoch"], "WT_DOGFOOD_UNTIL": header["until"]})
        self.assertEqual(result.returncode, 1)
        self.assertTrue(json.loads(result.stdout)["previous_failure"])
        self.assertEqual(path.read_bytes(), before)
        self.assertFalse((self.repo / "invocations").exists())
