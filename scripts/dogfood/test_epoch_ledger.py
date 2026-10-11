"""Persistence/integration probes using immutable original receipt inputs."""
import datetime
import hashlib
import json
from pathlib import Path
import shutil
import socket
import unittest
from unittest import mock

from contracts import CHECKS, INTERVAL_SECONDS, read_receipt, timestamp, utc
import ledger
import test_forge_package as fixtures


def original_receipt(start, failed=False, binary="b" * 64, cleanup="pass", platform="darwin"):
    records = []
    for name in CHECKS:
        status = "skip" if name in ("real_codex", "remembered_ssh_connect") else "pass"
        if name == "cleanup":
            status = cleanup
        if failed and name == "wing_restart_mid_run":
            status = "fail"
        if platform == "linux" and name.startswith("wt_claude_"):
            status = "unsupported-on-linux"
        records.append(dict(check=name, status=status, latency_ms=0, error="SECRET-DIAGNOSTIC",
                            timestamp=start, binary_sha256=binary,
                            descendant_inventory="verified" if name == "cleanup" else None))
    records.append(dict(check="runner", status="fail" if failed or cleanup != "pass" else "pass",
                        latency_ms=0, error="SECRET-DIAGNOSTIC", started_at=start,
                        timestamp=timestamp(utc(start) + datetime.timedelta(seconds=1)),
                        platform=platform, checks=13, exit_code=1 if failed or cleanup != "pass" else 0,
                        source_commit="c" * 40, source_sha256="d" * 64, binary_sha256=binary))
    return "".join(json.dumps(record) + "\n" for record in records).encode()


def append_receipt(path, header, name, raw):
    records = [json.loads(line) for line in raw.splitlines()]
    runner = records[-1]
    attempt = ledger.begin_record(header, name.removesuffix(".jsonl"), name, runner["started_at"], runner)
    ledger.append_record(path, attempt, reserve_result=True)
    result = ledger.result_record(attempt, records, hashlib.sha256(raw).hexdigest())
    ledger.append_record(path, result)
    return attempt, result


class LedgerPersistenceTests(fixtures.ForgeFixture):
    def finite_environment(self):
        now = datetime.datetime.now(datetime.timezone.utc)
        return {"WT_DOGFOOD_EPOCH": timestamp(now - datetime.timedelta(minutes=1)),
                "WT_DOGFOOD_UNTIL": timestamp(now + datetime.timedelta(days=2))}

    def test_runner_records_exact_metadata_without_error_text(self):
        environment = self.finite_environment()
        run = self.run_wrapper("nonzero", environment=environment)
        self.assertEqual(run.returncode, 1, run.stderr)
        path = self.logs / ledger.LEDGER_NAME
        view = ledger.checked(path)
        self.assertEqual(len(view["attempts"]), 1)
        result = next(iter(view["results"].values()))
        raw = next(self.logs.glob("*.jsonl"))
        self.assertEqual(result["receipt_sha256"], hashlib.sha256(raw.read_bytes()).hexdigest())
        self.assertEqual(result["exit_code"], 7)
        self.assertEqual(result["runner_status"], "fail")
        self.assertEqual(result["cleanup_status"], "pass")
        self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        self.assertNotIn("error", result)
        self.assertEqual(view["header"]["until"], environment["WT_DOGFOOD_UNTIL"])

    def test_same_epoch_reopens_and_extension_never_invokes_canary(self):
        environment = self.finite_environment()
        self.assertEqual(self.run_wrapper(environment=environment).returncode, 0)
        path = self.logs / ledger.LEDGER_NAME
        header = ledger.checked(path)["header"]
        self.assertEqual(self.run_wrapper(environment=environment).returncode, 0)
        self.assertEqual(ledger.checked(path)["header"], header)
        original = path.read_bytes()
        changed = dict(environment, WT_DOGFOOD_UNTIL=timestamp(utc(environment["WT_DOGFOOD_UNTIL"]) + datetime.timedelta(hours=1)))
        result = self.run_wrapper(environment=changed)
        self.assertEqual(result.returncode, 1)
        self.assertIn("immutable", result.stdout)
        self.assertEqual(path.read_bytes(), original)
        self.assertEqual((self.repo / "invocations").read_text().splitlines(), ["success", "success"])
        self.assertEqual(self.run_wrapper().returncode, 1)  # unset environment cannot bypass ledger

    def test_rotated_receipt_keeps_exact_failure_and_identity(self):
        now = datetime.datetime.now(datetime.timezone.utc)
        epoch = now - datetime.timedelta(seconds=25 * INTERVAL_SECONDS)
        header = ledger.epoch_header(timestamp(epoch), timestamp(now + datetime.timedelta(hours=1)), socket.gethostname())
        path = self.logs / ledger.LEDGER_NAME
        ledger.create_ledger(path, header)
        originals = self.root / "original-receipts"
        originals.mkdir()
        expected = None
        for slot in range(25):
            start = timestamp(epoch + datetime.timedelta(seconds=slot * INTERVAL_SECONDS))
            name = utc(start).strftime("%Y%m%dT%H%M%S.%fZ.jsonl")
            raw = original_receipt(start, failed=slot == 0)
            (originals / name).write_bytes(raw)
            shutil.copyfile(originals / name, self.logs / name)
            attempt, result = append_receipt(path, header, name, raw)
            if slot == 0:
                expected = (attempt, result, name, raw)
        environment = {"WT_DOGFOOD_EPOCH": header["epoch"], "WT_DOGFOOD_UNTIL": header["until"]}
        self.assertEqual(self.run_wrapper(environment=environment).returncode, 0)
        self.assertEqual(len(list(self.logs.glob("*.jsonl"))), 24)
        attempt, result, name, raw = expected
        self.assertFalse((self.logs / name).exists())
        self.assertEqual((originals / name).read_bytes(), raw)
        view = ledger.checked(path)
        self.assertEqual(view["attempts"][attempt["attempt_id"]], attempt)
        self.assertEqual(view["results"][attempt["attempt_id"]], result)
        self.assertEqual(result["runner_status"], "fail")
        self.assertEqual(result["binary_sha256"], "b" * 64)
        self.assertNotIn(b"SECRET-DIAGNOSTIC", path.read_bytes())

    def test_cleanup_hold_survives_missing_raw_receipt_and_state(self):
        environment = self.finite_environment()
        self.assertEqual(self.run_wrapper("cleanup-fail", environment=environment).returncode, 1)
        path = self.logs / ledger.LEDGER_NAME
        original = path.read_bytes()
        for receipt in self.logs.glob("*.jsonl"):
            receipt.unlink()
        (self.logs / "runner-state.json").unlink()
        result = self.run_wrapper(environment=environment)
        self.assertEqual(result.returncode, 1)
        self.assertIn("unresolved previous cleanup", result.stdout)
        self.assertEqual(path.read_bytes(), original)
        self.assertTrue((self.repo / "retained-state").exists())
        self.assertEqual((self.repo / "invocations").read_text().splitlines(), ["cleanup-fail"])

    def test_reserved_capacity_refuses_before_attempt_or_rotation(self):
        environment = self.finite_environment()
        header = ledger.epoch_header(environment["WT_DOGFOOD_EPOCH"], environment["WT_DOGFOOD_UNTIL"], socket.gethostname())
        path = self.logs / ledger.LEDGER_NAME
        ledger.create_ledger(path, header)
        before = path.read_bytes()
        attempt = ledger.begin_record(header, "probe", "probe.jsonl", header["epoch"], {})
        view = ledger.checked(path)
        with mock.patch.object(ledger, "TOTAL_LIMIT", len(before) + ledger.RECORD_LIMIT), mock.patch.object(
                ledger, "checked", return_value=view):
            with self.assertRaisesRegex(ValueError, "total cap"):
                ledger.append_record(path, attempt, reserve_result=True)
        self.assertEqual(path.read_bytes(), before)

    def test_closed_schema_and_atomic_creation_preserve_operator_input(self):
        header = ledger.epoch_header("2026-10-10T10:00:00Z", "2026-10-10T11:00:00Z", "mac")
        path = self.logs / ledger.LEDGER_NAME
        path.write_bytes(b"operator-input\n")
        with self.assertRaises(FileExistsError):
            ledger.create_ledger(path, header)
        self.assertEqual(path.read_bytes(), b"operator-input\n")
        path.unlink()
        ledger.create_ledger(path, header)
        attempt = ledger.begin_record(header, "probe", "probe.jsonl", header["epoch"], {})
        attempt["prompt"] = "SECRET"
        with self.assertRaisesRegex(ValueError, "metadata keys"):
            ledger.append_record(path, attempt)
