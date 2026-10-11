"""Finite metadata evidence, independent of raw-receipt retention.

Writers must hold the existing run.lock. Readers never repair evidence.
"""
import datetime
import hashlib
import json
import re

from contracts import INTERVAL_SECONDS, SLACK_SECONDS, timestamp, utc

LEDGER_NAME = "epoch-ledger.ndjson"
RECORD_LIMIT = 4096
TOTAL_LIMIT = 4 * 1024 * 1024
SLOT_LIMIT = 4096
INITIAL_DELAY_SECONDS = 120
SHA256 = re.compile(r"^[0-9a-f]{64}$")


def plan(epoch, until):
    start, end = utc(epoch), utc(until)
    duration = end - start
    if duration <= datetime.timedelta(0):
        raise ValueError("finite epoch must end after it starts")
    cadence = datetime.timedelta(seconds=INTERVAL_SECONDS)
    count = duration // cadence + bool(duration % cadence)
    if not 1 <= count <= SLOT_LIMIT:
        raise ValueError("finite epoch exceeds the 4096-slot evidence contract")
    return {"epoch": timestamp(start), "until": timestamp(end),
            "interval_seconds": INTERVAL_SECONDS, "slack_seconds": SLACK_SECONDS,
            "initial_delay_seconds": INITIAL_DELAY_SECONDS, "slot_count": count,
            "record_limit_bytes": RECORD_LIMIT, "total_limit_bytes": TOTAL_LIMIT}


def slot_for(contract, started_at):
    start, actual, end = utc(contract["epoch"]), utc(started_at), utc(contract["until"])
    if actual < start or actual >= end:
        return None
    return int((actual - start).total_seconds() // contract["interval_seconds"])


def planned_at(contract, slot):
    if type(slot) is not int or not 0 <= slot < contract["slot_count"]:
        raise ValueError("slot is outside the frozen epoch")
    return utc(contract["epoch"]) + datetime.timedelta(seconds=slot * contract["interval_seconds"])


def due_slots(contract, now):
    # At expiry even a short final slot is due. Future slots are never fabricated.
    now = utc(timestamp(now))
    ended = now >= utc(contract["until"])
    return [slot for slot in range(contract["slot_count"])
            if ended or now > planned_at(contract, slot) + datetime.timedelta(
                seconds=contract["slack_seconds"] + (INITIAL_DELAY_SECONDS if slot == 0 else 0))]


def lateness(contract, started_at):
    slot = slot_for(contract, started_at)
    if slot is None:
        return None
    delay = (utc(started_at) - planned_at(contract, slot)).total_seconds()
    allowance = contract["slack_seconds"] + (INITIAL_DELAY_SECONDS if slot == 0 else 0)
    return {"slot": slot, "delay_seconds": delay, "late": delay > allowance}


def epoch_header(epoch, until, host, origin="new"):
    if not isinstance(host, str) or not host or len(host.encode()) > 128 or any(ord(c) < 32 for c in host):
        raise ValueError("host must be a bounded metadata identifier")
    if origin not in ("new", "legacy-import"):
        raise ValueError("invalid coverage origin")
    header = {"kind": "epoch", "schema": 1, "host": host, "origin": origin, **plan(epoch, until)}
    header["epoch_id"] = hashlib.sha256(canonical(header)).hexdigest()
    return header


def canonical(record):
    return json.dumps(record, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode()

# Metadata keys are closed. No free-text error/reason field can enter the ledger.
import os
from pathlib import Path
import tempfile

from contracts import CHECKS, LOG_COUNT, STATUSES, read_receipt, receipt_complete

IDENTITY_KEYS = {"source_commit", "source_sha256", "binary_sha256"}
ATTEMPT_KEYS = {"kind", "attempt_id", "receipt", "started_at", "slot"} | IDENTITY_KEYS
RESULT_KEYS = {"kind", "attempt_id", "finished_at", "runner_status", "exit_code", "cleanup_status",
               "checks", "receipt_complete", "receipt_sha256", "binary_sha256", "platform",
               "binary_consistent", "descendant_inventory"}
TOKEN = re.compile(r"^[A-Za-z0-9_.-]{1,128}$")


def _digest_or_unknown(value, commit=False):
    return value is None or (isinstance(value, str) and bool(re.fullmatch(
        r"[0-9a-f]{40}|[0-9a-f]{64}" if commit else r"[0-9a-f]{64}", value)))


def validate_record(record, header, attempts, results):
    if record.get("kind") == "attempt":
        if set(record) != ATTEMPT_KEYS:
            raise ValueError("invalid attempt metadata keys")
        if (not isinstance(record["attempt_id"], str) or not TOKEN.fullmatch(record["attempt_id"]) or
                record["attempt_id"] in attempts or not isinstance(record["receipt"], str) or
                not TOKEN.fullmatch(record["receipt"]) or not record["receipt"].endswith(".jsonl")):
            raise ValueError("invalid or duplicate attempt identity")
        actual_slot = slot_for(header, record["started_at"])
        if record["slot"] != actual_slot or (actual_slot is None and header["origin"] != "legacy-import"):
            raise ValueError("attempt does not join its immutable slot")
        if type(record["slot"]) is not int and record["slot"] is not None:
            raise ValueError("slot must be an integer")
        if not all(_digest_or_unknown(record[key], key == "source_commit") for key in IDENTITY_KEYS):
            raise ValueError("invalid attempt identity hash")
    elif record.get("kind") == "result":
        if set(record) != RESULT_KEYS or record["attempt_id"] not in attempts or record["attempt_id"] in results:
            raise ValueError("orphan or duplicate result metadata")
        if utc(record["finished_at"]) < utc(attempts[record["attempt_id"]]["started_at"]):
            raise ValueError("completion precedes attempt")
        if (record["runner_status"] not in ("pass", "fail", "unknown") or
                record["cleanup_status"] not in STATUSES | {"unknown"} or
                type(record["receipt_complete"]) is not bool or type(record["binary_consistent"]) is not bool or
                (record["exit_code"] is not None and
                 (type(record["exit_code"]) is not int or abs(record["exit_code"]) > 2 ** 32))):
            raise ValueError("invalid result outcomes")
        checks = record["checks"]
        if not isinstance(checks, dict) or not set(checks) <= set(CHECKS) or any(v not in STATUSES for v in checks.values()):
            raise ValueError("invalid result check statuses")
        if (not _digest_or_unknown(record["binary_sha256"]) or
                not isinstance(record["receipt_sha256"], str) or not SHA256.fullmatch(record["receipt_sha256"]) or
                record["platform"] not in ("darwin", "linux", "win32", "unknown") or
                record["descendant_inventory"] not in ("verified", "unavailable", "unknown")):
            raise ValueError("invalid result provenance")
        if record["receipt_complete"] and set(checks) != set(CHECKS):
            raise ValueError("complete result is missing checks")
    else:
        raise ValueError("invalid ledger record kind")


def _sealed(record, seq, previous):
    body = dict(record, seq=seq, previous_sha256=previous)
    digest = hashlib.sha256(canonical(body)).hexdigest()
    encoded = canonical(dict(body, sha256=digest)) + b"\n"
    if len(encoded) > RECORD_LIMIT:
        raise ValueError("ledger record exceeds 4096-byte cap")
    return encoded, digest


def _strict_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate JSON key")
        result[key] = value
    return result


def read_ledger(path):
    """Return a verified prefix and diagnostics; never alter a corrupt tail."""
    view = {"header": None, "attempts": {}, "results": {}, "errors": [], "bytes": 0,
            "records": 0, "last_sha256": None}
    try:
        if path.is_symlink() or path.stat().st_size > TOTAL_LIMIT:
            raise ValueError("symlinked or oversized ledger")
        with path.open("rb") as stream:
            while True:
                line = stream.readline(RECORD_LIMIT + 1)
                if not line:
                    break
                view["bytes"] += len(line)
                if view["bytes"] > TOTAL_LIMIT or len(line) > RECORD_LIMIT or not line.endswith(b"\n"):
                    raise ValueError("oversized record or interrupted JSON tail")
                record = json.loads(line, object_pairs_hook=_strict_object)
                if not isinstance(record, dict):
                    raise ValueError("ledger records must be objects")
                digest = record.pop("sha256", None)
                if digest != hashlib.sha256(canonical(record)).hexdigest():
                    raise ValueError("ledger record digest mismatch")
                if (type(record.get("seq")) is not int or record.pop("seq") != view["records"] or
                        record.pop("previous_sha256", None) != view["last_sha256"]):
                    raise ValueError("ledger sequence or digest chain mismatch")
                if view["header"] is None:
                    expected = epoch_header(record["epoch"], record["until"], record["host"], record["origin"])
                    if record != expected:
                        raise ValueError("invalid immutable epoch header")
                    view["header"] = record
                else:
                    validate_record(record, view["header"], view["attempts"], view["results"])
                    table = view["attempts"] if record["kind"] == "attempt" else view["results"]
                    table[record["attempt_id"]] = record
                view["records"] += 1
                view["last_sha256"] = digest
        if view["header"] is None:
            raise ValueError("empty ledger")
    except (OSError, ValueError, KeyError, TypeError, UnicodeError) as error:
        view["errors"].append(str(error))
    return view


def checked(path):
    view = read_ledger(path)
    if view["errors"]:
        raise ValueError("ledger preserved; refusing unreadable evidence: " + "; ".join(view["errors"]))
    return view


def _sync_directory(directory):
    descriptor = os.open(directory, os.O_RDONLY)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def create_ledger(path, header, records=()):
    """Atomic, no-clobber publication. Caller owns run.lock, including imports."""
    attempts, results = {}, {}
    encoded, previous = _sealed(header, 0, None)
    chunks = [encoded]
    for seq, record in enumerate(records, 1):
        validate_record(record, header, attempts, results)
        encoded, previous = _sealed(record, seq, previous)
        chunks.append(encoded)
        (attempts if record["kind"] == "attempt" else results)[record["attempt_id"]] = record
    data = b"".join(chunks)
    if len(data) > TOTAL_LIMIT:
        raise ValueError("ledger exceeds 4-MiB total cap")
    descriptor, name = tempfile.mkstemp(prefix="epoch-ledger-", dir=path.parent)
    temporary = Path(name)
    try:
        with os.fdopen(descriptor, "wb") as output:
            output.write(data)
            output.flush()
            os.fsync(output.fileno())
        os.link(temporary, path)  # fails rather than overwriting any existing input
        _sync_directory(path.parent)
    finally:
        temporary.unlink(missing_ok=True)


def append_record(path, record, reserve_result=False):
    view = checked(path)
    validate_record(record, view["header"], view["attempts"], view["results"])
    encoded, _ = _sealed(record, view["records"], view["last_sha256"])
    reserved = RECORD_LIMIT if reserve_result else 0
    if view["bytes"] + len(encoded) + reserved > TOTAL_LIMIT:
        raise ValueError("ledger total cap reached; no rotation or new attempt permitted")
    with path.open("ab") as output:
        output.write(encoded)
        output.flush()
        os.fsync(output.fileno())


def begin_record(header, attempt_id, receipt, started_at, identity):
    return {"kind": "attempt", "attempt_id": attempt_id, "receipt": receipt,
            "started_at": timestamp(utc(started_at)), "slot": slot_for(header, started_at),
            **{key: identity.get(key) if _digest_or_unknown(identity.get(key), key == "source_commit") else None
               for key in IDENTITY_KEYS}}


def result_record(attempt, records, raw_digest, finished_at=None):
    runner = records[-1] if records and records[-1].get("check") == "runner" else {}
    checks = {r["check"]: r["status"] for r in records
              if r.get("check") in CHECKS and r.get("status") in STATUSES}
    cleanup = next((r for r in reversed(records) if r.get("check") == "cleanup"), {})
    observed = {r.get("binary_sha256") for r in records if r.get("binary_sha256")}
    binary = next(iter(observed), None) if len(observed) <= 1 else None
    finish = finished_at or runner.get("timestamp") or attempt["started_at"]
    inventory = cleanup.get("descendant_inventory", "unknown")
    return {"kind": "result", "attempt_id": attempt["attempt_id"], "finished_at": timestamp(utc(finish)),
            "runner_status": runner.get("status") if runner.get("status") in ("pass", "fail") else "unknown",
            "exit_code": runner.get("exit_code"), "cleanup_status": cleanup.get("status", "unknown"),
            "checks": checks, "receipt_complete": receipt_complete(records), "receipt_sha256": raw_digest,
            "binary_sha256": binary if _digest_or_unknown(binary) else None,
            "binary_consistent": bool(binary and len(observed) == 1 and
                                      (not attempt["binary_sha256"] or attempt["binary_sha256"] == binary)),
            "platform": runner.get("platform") if runner.get("platform") in ("darwin", "linux", "win32") else "unknown",
            "descendant_inventory": "unavailable" if str(inventory).startswith("unavailable") else
                                    "verified" if inventory == "verified" else "unknown"}


def retained_records(logs, header):
    """Conservative legacy projection. Inputs are read, never repaired or removed."""
    paths = sorted(logs.glob("*.jsonl"))
    if len(paths) > LOG_COUNT:
        raise ValueError("legacy import accepts at most the retained 24 receipts")
    projected = []
    for path in paths:
        records = read_receipt(path)
        runner = records[-1] if records and records[-1].get("check") == "runner" else {}
        start = runner.get("started_at") or (records[0].get("timestamp") if records else None)
        if not start:
            raise ValueError("legacy receipt has no attributable start: " + path.name)
        attempt_id = "legacy-" + hashlib.sha256(path.name.encode()).hexdigest()
        attempt = begin_record(header, attempt_id, path.name, start, runner)
        projected.extend([attempt, result_record(attempt, records, hashlib.sha256(path.read_bytes()).hexdigest())])
    return projected


def ensure_epoch(logs, epoch, until, host):
    path = logs / LEDGER_NAME
    if path.exists() or path.is_symlink():
        view = checked(path)
        if view["header"] != epoch_header(epoch, until, host, view["header"]["origin"]):
            raise ValueError("existing epoch is immutable; cannot change host, start, end or cadence")
        return view
    legacy = bool(list(logs.glob("*.jsonl")))
    header = epoch_header(epoch, until, host, "legacy-import" if legacy else "new")
    create_ledger(path, header, retained_records(logs, header) if legacy else [])
    return checked(path)


def cleanup_hold(view):
    for attempt_id, attempt in view["attempts"].items():
        result = view["results"].get(attempt_id)
        if not result or result["cleanup_status"] != "pass":
            return {"schema": 1, "phase": "running" if not result else "complete",
                    "cleanup_status": result["cleanup_status"] if result else "unknown",
                    "previous_failure": True, "receipt": attempt["receipt"]}
    return None


def rotation_ready(path, candidates):
    view = checked(path)
    by_name = {attempt["receipt"]: view["results"].get(key) for key, attempt in view["attempts"].items()}
    for receipt in candidates:
        result = by_name.get(receipt.name)
        if not result or result["receipt_sha256"] != hashlib.sha256(receipt.read_bytes()).hexdigest():
            raise ValueError("raw receipt lacks exact durable metadata; refusing rotation: " + receipt.name)

from collections import Counter
from contracts import MAILBOX, OPTIONAL

NATIVE_CHECKS = ("mcp_stdio_agent_run_wait_result", "mcp_client_disconnect", "wing_restart_mid_run",
                 "mcp_tasks_get_result_list_cancel", "terminal_start_send_stop")


def attempt_judgment(attempt, result, now):
    if result is None:
        return {"fixture": "unknown", "completed": False, "failed": False, "skipped": False,
                "native_checks": {key: "unknown" for key in NATIVE_CHECKS},
                "live_checks": {key: "unknown" for key in sorted(OPTIONAL)}}
    checks = result["checks"]
    completed = result["receipt_complete"] and utc(result["finished_at"]) <= now
    required = set(CHECKS) - OPTIONAL
    if result["platform"] == "linux":
        required -= MAILBOX
    failed = (result["runner_status"] == "fail" or result["cleanup_status"] == "fail" or
              result["exit_code"] not in (0, None) or any(v == "fail" for v in checks.values()))
    skipped = any(checks.get(key) in ("skip", "unsupported-on-linux") for key in required)
    identities_known = (all(attempt.get(key) for key in ("source_commit", "source_sha256")) and
                        result["binary_sha256"] and result["binary_consistent"])
    unsupported_correct = (result["platform"] != "linux" or
                           all(checks.get(key) == "unsupported-on-linux" for key in MAILBOX))
    good = (completed and result["runner_status"] == "pass" and result["exit_code"] == 0 and
            all(checks.get(key) == "pass" for key in required) and identities_known and unsupported_correct and
            result["descendant_inventory"] == "verified" and result["platform"] != "unknown")
    fixture = "fail" if failed else "pass" if good else "unavailable" if skipped else "unknown"
    return {"fixture": fixture, "completed": completed, "failed": failed, "skipped": skipped,
            "native_checks": {key: checks.get(key, "unknown") for key in NATIVE_CHECKS},
            "live_checks": {key: checks.get(key, "unknown") for key in sorted(OPTIONAL)}}


def consume(view, now):
    """Point coverage of the whole finite epoch; no interpolation of green runs."""
    header = view["header"]
    if header is None:
        return {"schema": 1, "all_green": False, "attention_required": True,
                "diagnostics": view["errors"], "epoch": None, "slots": [], "counts": {}, "cohorts": []}
    due = set(due_slots(header, now))
    grouped = {}
    unassigned = []
    all_attempts = []
    diagnostics = list(view["errors"])
    for key, attempt in sorted(view["attempts"].items(), key=lambda item: (utc(item[1]["started_at"]), item[0])):
        result = view["results"].get(key)
        judgment = attempt_judgment(attempt, result, now)
        if utc(attempt["started_at"]) > now:
            judgment.update(fixture="unknown", completed=False)
            diagnostics.append("attempt timestamp is in the future: " + key)
        row = dict(attempt, result=result, judgments=judgment,
                   timing=lateness(header, attempt["started_at"]))
        all_attempts.append(row)
        if attempt["slot"] is None:
            unassigned.append(row)
        else:
            grouped.setdefault(attempt["slot"], []).append(row)
    receipt_hashes = Counter(result["receipt_sha256"] for result in view["results"].values())
    duplicates = sorted(digest for digest, count in receipt_hashes.items() if count > 1)
    if duplicates:
        diagnostics.append("duplicate raw receipt content retained; no extra slot coverage")
    slots = []
    for slot in range(header["slot_count"]):
        attempts = grouped.get(slot, [])
        judgments = [item["judgments"] for item in attempts]
        failed = any(item["failed"] for item in judgments)
        completed = any(item["completed"] for item in judgments)
        skipped = any(item["skipped"] for item in judgments)
        unknown = any(item["fixture"] == "unknown" for item in judgments)
        fixture_pass = bool(judgments) and all(item["fixture"] == "pass" for item in judgments)
        if attempts:
            status = "failed" if failed else "unknown" if unknown else "skipped" if skipped else "completed"
        elif slot not in due:
            status = "pending"
        else:
            status = "unknown" if header["origin"] == "legacy-import" or view["errors"] else "missing"
        slots.append({"slot": slot, "planned_at": timestamp(planned_at(header, slot)), "due": slot in due,
                      "status": status, "attempts": attempts, "completed": completed,
                      "fixture_pass": fixture_pass, "failed": failed, "skipped": skipped,
                      "late": any(item["timing"]["late"] for item in attempts),
                      "duplicate_attempts": max(0, len(attempts) - 1)})
    assigned = [item for slot in slots for item in slot["attempts"]]
    counts = {"planned_slots": header["slot_count"], "due_slots": len(due),
              "attempted": len(assigned), "unique_slots": len(grouped),
              "completed_attempts": sum(item["judgments"]["completed"] for item in assigned),
              "completed_slots": sum(slot["completed"] for slot in slots),
              "fixture_pass_slots": sum(slot["fixture_pass"] for slot in slots),
              "failed_slots": sum(slot["failed"] for slot in slots),
              "skipped_slots": sum(slot["skipped"] for slot in slots),
              "missing_slots": sum(slot["status"] == "missing" for slot in slots),
              "unknown_slots": sum(slot["status"] == "unknown" for slot in slots),
              "pending_slots": sum(slot["status"] == "pending" for slot in slots),
              "late_slots": sum(slot["late"] for slot in slots),
              "duplicate_slots": sum(slot["duplicate_attempts"] > 0 for slot in slots),
              "duplicate_attempts": sum(slot["duplicate_attempts"] for slot in slots),
              "duplicate_receipts": sum(count - 1 for count in receipt_hashes.values()),
              "unassigned_attempts": len(unassigned)}
    # Only repeated hashes are duplicates; singleton receipts contribute zero.
    counts["duplicate_receipts"] = sum(count - 1 for count in receipt_hashes.values() if count > 1)
    cohorts = {}
    for item in all_attempts:
        result = item["result"] or {}
        key = (header["host"], item["binary_sha256"], result.get("binary_sha256"),
               item["source_commit"], item["source_sha256"])
        cohort = cohorts.setdefault(key, {"host": key[0], "started_binary_sha256": key[1],
                     "observed_binary_sha256": key[2], "source_commit": key[3], "source_sha256": key[4],
                     "attempted": 0, "completed": 0, "failed": 0, "unknown": 0, "fixture_pass": 0,
                     "slots": set()})
        cohort["attempted"] += 1
        cohort["completed"] += item["judgments"]["completed"]
        cohort["failed"] += item["judgments"]["failed"]
        cohort["unknown"] += item["judgments"]["fixture"] == "unknown"
        cohort["fixture_pass"] += item["judgments"]["fixture"] == "pass"
        if item["slot"] is not None:
            cohort["slots"].add(item["slot"])
    sorted_cohorts = []
    for key in sorted(cohorts, key=lambda value: tuple(str(part or "") for part in value)):
        cohort = cohorts[key]
        cohort["slots"] = sorted(cohort["slots"])
        sorted_cohorts.append(cohort)
    covered_due = sum(slot["fixture_pass"] for slot in slots if slot["due"])
    attention = bool(diagnostics or unassigned or counts["failed_slots"] or counts["skipped_slots"] or
                     counts["missing_slots"] or counts["unknown_slots"] or counts["late_slots"])
    return {"schema": 1, "all_green": False, "epoch": header, "now": timestamp(now),
            "ended": now >= utc(header["until"]), "counts": counts, "slots": slots,
            "cohorts": sorted_cohorts, "unassigned": unassigned, "duplicate_receipt_hashes": duplicates,
            "attention_required": attention, "diagnostics": diagnostics,
            "actual_coverage": {"sampled_slots": counts["unique_slots"], "fixture_pass_slots": counts["fixture_pass_slots"],
                                "fixture_pass_due_slots": covered_due, "due_slots": len(due),
                                "fraction_of_due_slots": covered_due / len(due) if due else None,
                                "healthy_interval_seconds": None,
                                "scope": "point observations; no interpolation between attempts"},
            "judgment_scope": {"fixture": "check outcomes in metadata receipts",
                               "native": "native completion check outcomes inside the canary fixture; not fresh host acceptance",
                               "live": "optional check statuses per attempt; skips are unavailable",
                               "outside_host_acceptance": "REQUIRED-UNRUN"},
            "ledger": {"bytes": view["bytes"], "records": view["records"], "last_sha256": view["last_sha256"],
                       "integrity": "FAIL" if view["errors"] else "PASS"}}


def main():
    import argparse
    import fcntl
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="action", required=True)
    reader = subparsers.add_parser("report")
    reader.add_argument("--logs", type=Path, required=True)
    reader.add_argument("--now", required=True)
    importer = subparsers.add_parser("import-legacy")
    importer.add_argument("--logs", type=Path, required=True, help="immutable 24-receipt snapshot")
    importer.add_argument("--out", type=Path, required=True, help="new ledger, preferably in a separate directory")
    importer.add_argument("--epoch", required=True)
    importer.add_argument("--until", required=True)
    importer.add_argument("--host", required=True)
    args = parser.parse_args()
    try:
        if args.action == "report":
            result = consume(read_ledger(args.logs / LEDGER_NAME), utc(args.now))
            print(json.dumps(result, indent=2, sort_keys=True))
            return 1 if result["attention_required"] else 0
        header = epoch_header(args.epoch, args.until, args.host, "legacy-import")
        if not args.out.parent.is_dir():
            raise ValueError("output parent must already be a private directory")
        with (args.out.parent / "run.lock").open("a") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            create_ledger(args.out, header, retained_records(args.logs, header))
        print(json.dumps({"ledger": str(args.out), "origin": "legacy-import", "all_green": False}))
        return 0
    except (OSError, ValueError, TypeError) as error:
        parser.exit(2, str(error) + "\n")


if __name__ == "__main__":
    raise SystemExit(main())
