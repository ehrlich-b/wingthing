#!/usr/bin/env python3
"""Read bounded local receipts. Exit 2: no complete run; 1: gaps/failure/unknown; 0: limited fixture coverage."""
import argparse
import datetime
import fcntl
import hashlib
import json
from pathlib import Path
import re
import sys

from contracts import (CHECKS, INTERVAL_SECONDS, LOG_COUNT, MAILBOX, OPTIONAL,
                       SLACK_SECONDS, STATUSES, read_json, read_receipt, timestamp, utc)
from ledger import LEDGER_NAME, consume, read_ledger

SHA256 = re.compile(r"^[0-9a-f]{64}$")


def complete(records):
    if not records or records[-1].get("check") != "runner":
        return False
    checks = [record.get("check") for record in records[:-1]]
    runner = records[-1]
    return (all(isinstance(check, str) for check in checks) and
            len(checks) == len(CHECKS) and set(checks) == set(CHECKS) and
            runner.get("checks") == len(CHECKS) and runner.get("status") in ("pass", "fail") and
            all(record.get("status") in STATUSES and
                {"check", "status", "latency_ms", "error"} <= record.keys() for record in records))


def gaps(starts, now, epoch=None, until=None):
    end = min(now, until) if until else now
    starts = sorted(set(value for value in starts if value <= end and (not epoch or value >= epoch)))
    result = []
    if epoch and end > epoch + datetime.timedelta(seconds=120 + SLACK_SECONDS):
        first = starts[0] if starts else end
        elapsed = (first - epoch).total_seconds() - 120
        if elapsed > SLACK_SECONDS:
            result.append({"after": timestamp(epoch), "before": timestamp(first), "kind": "initial",
                           "missing_expected_runs": max(1, int((elapsed - SLACK_SECONDS) // INTERVAL_SECONDS) + 1)})
    for earlier, later in zip(starts, starts[1:]):
        elapsed = (later - earlier).total_seconds()
        if elapsed > INTERVAL_SECONDS + SLACK_SECONDS:
            result.append({"after": timestamp(earlier), "before": timestamp(later), "kind": "between",
                           "missing_expected_runs": max(1, int((elapsed - SLACK_SECONDS) // INTERVAL_SECONDS))})
    if starts:
        elapsed = (end - starts[-1]).total_seconds()
        if elapsed > INTERVAL_SECONDS + SLACK_SECONDS:
            result.append({"after": timestamp(starts[-1]), "before": timestamp(end), "kind": "latest",
                           "missing_expected_runs": max(1, int((elapsed - SLACK_SECONDS) // INTERVAL_SECONDS))})
    return result


def report(logs, now):
    diagnostics = []
    runs = []
    starts = []
    cleanup = None
    paths = sorted(logs.glob("*.jsonl")) if logs.is_dir() else []
    if len(paths) > LOG_COUNT:
        diagnostics.append("receipt count exceeds the 24-file contract; newest 24 inspected")
    paths = paths[-LOG_COUNT:]
    for path in paths:
        try:
            records = read_receipt(path)
            cleanup = {"status": "unknown", "receipt": str(path), "reason": "latest attempt has no cleanup receipt"}
            for record in records:
                if record.get("check") == "cleanup":
                    cleanup = dict(record, receipt=str(path))
            if not complete(records):
                diagnostics.append("incomplete or invalid receipt: " + str(path))
                continue
            runner = records[-1]
            start = runner.get("started_at") or records[0].get("timestamp")
            finish = runner.get("timestamp") or records[-2].get("timestamp")
            if not start or not finish:
                raise ValueError("missing start or completion timestamp")
            start_time, finish_time = utc(start), utc(finish)
            if start_time > finish_time or finish_time > now:
                raise ValueError("reversed or future run timestamps")
            starts.append(start_time)
            runs.append((path, records, start_time, finish_time))
        except (OSError, ValueError, KeyError, TypeError) as error:
            diagnostics.append(str(path) + ": " + str(error))
    state = {}
    state_path = logs / "runner-state.json"
    if state_path.exists():
        try:
            state = read_json(state_path)
            if not isinstance(state, dict) or state.get("schema") != 1:
                raise ValueError("invalid runner state")
        except (OSError, ValueError) as error:
            diagnostics.append("unreadable runner state: " + str(error))
            state = {}
    locked = False
    if (logs / "run.lock").exists():
        try:
            with (logs / "run.lock").open("r") as lock:
                fcntl.flock(lock, fcntl.LOCK_SH | fcntl.LOCK_NB)
        except BlockingIOError:
            locked = True
        except OSError as error:
            diagnostics.append("lock status unavailable: " + str(error))
    if state.get("phase") == "running":
        diagnostics.append("runner active" if locked else "stale interrupted runner; cleanup is unknown")
        cleanup = {"status": "unknown", "receipt": state.get("receipt"), "reason": diagnostics[-1]}
    if state.get("previous_failure"):
        diagnostics.append("runner state retains a previous failure")
    latest = runs[-1] if runs else None
    runner = latest[1][-1] if latest else {}
    epoch = until = None
    try:
        epoch_value = state.get("epoch") or runner.get("epoch")
        until_value = state.get("until") or runner.get("until")
        if bool(epoch_value) != bool(until_value):
            raise ValueError("incomplete finite epoch")
        if epoch_value:
            epoch, until = utc(epoch_value), utc(until_value)
            if epoch >= until:
                raise ValueError("reversed finite epoch")
    except (ValueError, TypeError) as error:
        diagnostics.append(str(error))
    gap_report = gaps(starts, now, epoch, until)
    epoch_evidence = None
    ledger_path = logs / LEDGER_NAME
    if ledger_path.exists() or ledger_path.is_symlink():
        view = read_ledger(ledger_path)
        epoch_evidence = consume(view, now)
        if epoch_evidence["epoch"]:
            frozen = epoch_evidence["epoch"]
            if epoch and (timestamp(epoch) != frozen["epoch"] or timestamp(until) != frozen["until"]):
                diagnostics.append("runner state and immutable ledger disagree on epoch")
            epoch, until = utc(frozen["epoch"]), utc(frozen["until"])
            gap_report = [{"kind": "missing-slot", "slot": slot["slot"], "after": slot["planned_at"],
                           "missing_expected_runs": 1} for slot in epoch_evidence["slots"] if slot["status"] == "missing"]
        if epoch_evidence["attention_required"]:
            diagnostics.append("whole finite epoch has failure, missing, unavailable or unknown evidence")
        diagnostics.extend(epoch_evidence["diagnostics"])
        receipts = {attempt["receipt"]: view["results"].get(key) for key, attempt in view["attempts"].items()}
        for path in paths:
            metadata = receipts.get(path.name)
            if metadata is None:
                diagnostics.append("retained raw receipt has no complete ledger result: " + path.name)
            elif not path.is_symlink() and path.stat().st_size <= 256 * 1024:
                if metadata["receipt_sha256"] != hashlib.sha256(path.read_bytes()).hexdigest():
                    diagnostics.append("retained raw receipt differs from ledger metadata: " + path.name)
    platform = runner.get("platform")
    if not isinstance(platform, str):
        platform = None
        if latest:
            diagnostics.append("run platform is unknown")
    checks = {}
    observed = {record["check"]: record for record in latest[1][:-1]} if latest else {}
    required_unavailable = []
    optional_skips = []
    unsupported = []
    for name in CHECKS:
        requirement = "optional" if name in OPTIONAL else "required"
        if name in MAILBOX and platform and platform.startswith("linux"):
            requirement = "unsupported"
        record = observed.get(name, {})
        status = record.get("status", "unknown")
        checks[name] = {"requirement": requirement, "status": status,
                        "reason": record.get("reason"), "error": record.get("error")}
        if requirement == "required" and status != "pass":
            required_unavailable.append(name)
        if requirement == "optional" and status != "pass":
            optional_skips.append(name)
        if requirement == "unsupported":
            unsupported.append(name)
            if status != "unsupported-on-linux":
                diagnostics.append("unexpected Linux unsupported-check status: " + name)
    identity = {key: runner.get(key) for key in ("binary_sha256", "source_commit", "source_sha256")}
    hashes = {str(record.get("binary_sha256")) for record in observed.values() if record.get("binary_sha256")}
    if hashes and hashes != {identity["binary_sha256"]}:
        diagnostics.append("inconsistent binary identity within the last complete run")
    if latest and (not SHA256.fullmatch(str(identity["binary_sha256"])) or
                   not SHA256.fullmatch(str(identity["source_sha256"])) or not identity["source_commit"]):
        diagnostics.append("binary or tracked-source identity is unknown")
    if cleanup and str(cleanup.get("descendant_inventory", "")).startswith("unavailable"):
        diagnostics.append(cleanup["descendant_inventory"])
    if latest and (runner.get("status") != "pass" or runner.get("exit_code") != 0 or
                   any(record["status"] == "fail" for record in latest[1][:-1])):
        diagnostics.append("last complete run failed")
    if cleanup is None or cleanup.get("status") != "pass":
        diagnostics.append("last cleanup did not pass or is unknown")
    last_complete = None if not latest else {"receipt": str(latest[0]), "started_at": timestamp(latest[2]),
                                           "completed_at": timestamp(latest[3]), "status": runner["status"],
                                           "platform": platform, "checks": runner["checks"], **identity}
    code = 2 if not latest else (1 if diagnostics or gap_report or required_unavailable else 0)
    overall = "missing-runs" if not latest else ("attention-required" if code else "fixture-pass-limited-acceptance")
    return {"schema": 1, "overall": overall, "all_green": False, "exit_code": code,
            "now": timestamp(now), "logs": str(logs), "receipts_inspected": len(paths),
            "complete_runs": len(runs), "last_complete_run": last_complete,
            "last_attempt": str(paths[-1]) if paths else None, "lock_held": locked,
            "schedule": {"interval_seconds": INTERVAL_SECONDS, "slack_seconds": SLACK_SECONDS,
                         "epoch": timestamp(epoch) if epoch else None, "until": timestamp(until) if until else None,
                         "expired": bool(until and now >= until), "gaps": gap_report,
                         "history_limit": ("Whole immutable epoch metadata and newest 24 raw receipts; pre-ledger gaps remain unknown."
                                           if epoch_evidence else "Only retained receipts and the persisted epoch are inspected.")},
            "checks": checks, "required_unavailable": required_unavailable,
            "optional_unavailable": optional_skips, "unsupported": unsupported,
            "last_cleanup": cleanup, "diagnostics": diagnostics,
            "epoch_evidence": epoch_evidence,
            "source_identity_scope": "tracked checkout bytes at run start; binary identity is independent",
            "required_unrun": ["real Linux/systemd acceptance", "real provider acceptance"]}, code


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--logs", type=Path, required=True)
    parser.add_argument("--now", required=True)
    parser.add_argument("--json", action="store_true")
    args = parser.parse_args()
    try:
        result, code = report(args.logs, utc(args.now))
    except (ValueError, OSError) as error:
        parser.exit(2, str(error) + "\n")
    if args.json:
        print(json.dumps(result, indent=2, ensure_ascii=False))
    else:
        print(result["overall"])
        print("Last complete run: " + str(result["last_complete_run"]))
        print("Gaps: " + str(result["schedule"]["gaps"]))
        if result["epoch_evidence"]:
            print("Whole epoch: " + str(result["epoch_evidence"]["counts"]))
        print("Required unavailable: " + ", ".join(result["required_unavailable"]))
        print("Optional unavailable: " + ", ".join(result["optional_unavailable"]))
        print("Unsupported: " + ", ".join(result["unsupported"]))
        for diagnostic in result["diagnostics"]:
            print(diagnostic)
    return code


if __name__ == "__main__":
    sys.exit(main())
