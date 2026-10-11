#!/usr/bin/env python3
"""Independent, read-only ended-epoch arithmetic oracle for offline packets.

No imports from the runner, ledger or status consumer. This validates synthetic
and legacy metadata arithmetic, not genuine unattended or provider acceptance.
"""
import argparse
import datetime as dt
import hashlib
import json
from pathlib import Path
import sys

NAMES = {"local_only_start", "local_only_daemon_boot", "terminal_start_send_stop",
         "mcp_stdio_agent_run_wait_result", "mcp_client_disconnect", "wing_restart_mid_run",
         "mcp_tasks_get_result_list_cancel", "remembered_ssh_fixture",
         "wt_claude_refuses_provider_writable_state", "wt_claude_scoped_mailbox",
         "remembered_ssh_connect", "real_codex", "cleanup"}
OPTIONAL = {"remembered_ssh_connect", "real_codex"}
MAILBOX = {"wt_claude_refuses_provider_writable_state", "wt_claude_scoped_mailbox"}


def instant(text):
    value = dt.datetime.fromisoformat(text.replace("Z", "+00:00"))
    if value.tzinfo is None or value.utcoffset() != dt.timedelta(0):
        raise ValueError("oracle requires UTC")
    return value


def inspect(path, now):
    if path.is_symlink() or path.stat().st_size > 4194304:
        raise ValueError("oracle input is symlinked or exceeds 4 MiB")
    records = []
    previous = None
    with path.open("rb") as stream:
        for number, line in enumerate(stream):
            if len(line) > 4096 or not line.endswith(b"\n"):
                raise ValueError("oracle record cap or partial tail")
            item = json.loads(line)
            digest = item.pop("sha256")
            canonical = json.dumps(item, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode()
            if digest != hashlib.sha256(canonical).hexdigest() or item.pop("seq") != number:
                raise ValueError("oracle digest or sequence mismatch")
            if item.pop("previous_sha256") != previous:
                raise ValueError("oracle chain mismatch")
            previous = digest
            records.append(item)
    header = records[0]
    identity = dict(header)
    epoch_id = identity.pop("epoch_id")
    canonical = json.dumps(identity, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode()
    if epoch_id != hashlib.sha256(canonical).hexdigest():
        raise ValueError("oracle epoch identity mismatch")
    start, end = instant(header["epoch"]), instant(header["until"])
    if now < end or end <= start:
        raise ValueError("oracle only inspects an ended finite epoch")
    cadence = dt.timedelta(seconds=2700)
    count = (end - start) // cadence + (1 if (end - start) % cadence else 0)
    if count != header["slot_count"] or header["interval_seconds"] != 2700 or not 1 <= count <= 4096:
        raise ValueError("oracle frozen plan mismatch")
    attempts = {item["attempt_id"]: item for item in records if item["kind"] == "attempt"}
    outcomes = {item["attempt_id"]: item for item in records if item["kind"] == "result"}
    if len(attempts) + len(outcomes) != len(records) - 1:
        raise ValueError("oracle duplicate or unknown metadata")
    groups = {}
    hashes = {}
    for result in outcomes.values():
        digest = result["receipt_sha256"]
        hashes[digest] = hashes.get(digest, 0) + 1
    unassigned = 0
    for key, attempt in attempts.items():
        actual = instant(attempt["started_at"])
        if not start <= actual < end:
            if attempt["slot"] is not None:
                raise ValueError("oracle unassigned join mismatch")
            unassigned += 1
            continue
        slot = (actual - start) // cadence
        if slot != attempt["slot"]:
            raise ValueError("oracle slot join mismatch")
        result = outcomes.get(key)
        finished = result is not None and result["receipt_complete"] and instant(result["finished_at"]) <= now
        checks = result["checks"] if result else {}
        required = NAMES - OPTIONAL - (MAILBOX if result and result["platform"] == "linux" else set())
        failed = bool(result and (result["runner_status"] == "fail" or result["exit_code"] not in (None, 0)
                                 or result["cleanup_status"] == "fail" or "fail" in checks.values()))
        skipped = any(checks.get(name) in ("skip", "unsupported-on-linux") for name in required)
        healthy_sample = bool(result and finished and not failed and result["runner_status"] == "pass" and
                    result["exit_code"] == 0 and all(checks.get(name) == "pass" for name in required) and
                    attempt["source_commit"] and attempt["source_sha256"] and result["binary_sha256"] and
                    result["binary_consistent"] and result["descendant_inventory"] == "verified" and
                    result["platform"] != "unknown" and
                    (result["platform"] != "linux" or all(checks.get(name) == "unsupported-on-linux" for name in MAILBOX)))
        unknown = not failed and not skipped and not healthy_sample
        delay = (actual - (start + slot * cadence)).total_seconds()
        late = delay > (180 if slot == 0 else 60)
        groups.setdefault(slot, []).append((finished, failed, skipped, unknown, healthy_sample, late))
    completed = failures = skipped_slots = unknown_slots = pass_slots = late_slots = duplicates = 0
    completed_attempts = 0
    for values in groups.values():
        completed_attempts += sum(row[0] for row in values)
        completed += any(row[0] for row in values)
        failures += any(row[1] for row in values)
        skipped_slots += any(row[2] for row in values)
        unknown_slots += not any(row[1] for row in values) and any(row[3] for row in values)
        pass_slots += all(row[4] for row in values)
        late_slots += any(row[5] for row in values)
        duplicates += len(values) > 1
    absent = count - len(groups)
    legacy = header["origin"] == "legacy-import"
    return {"host": header["host"], "epoch_id": header["epoch_id"], "origin": header["origin"], "all_green": False,
            "counts": {"planned_slots": count, "due_slots": count, "attempted": len(attempts) - unassigned,
                       "unique_slots": len(groups), "completed_attempts": completed_attempts,
                       "completed_slots": completed, "failed_slots": failures, "skipped_slots": skipped_slots,
                       "unknown_slots": unknown_slots + (absent if legacy else 0),
                       "missing_slots": 0 if legacy else absent, "pending_slots": 0,
                       "fixture_pass_slots": pass_slots, "late_slots": late_slots, "duplicate_slots": duplicates,
                       "duplicate_attempts": sum(len(v) - 1 for v in groups.values()),
                       "duplicate_receipts": sum(n - 1 for n in hashes.values()), "unassigned_attempts": unassigned},
            "bytes": path.stat().st_size, "sha256": hashlib.sha256(path.read_bytes()).hexdigest()}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--ledger", type=Path, action="append", required=True)
    parser.add_argument("--now", required=True)
    args = parser.parse_args()
    try:
        reports = [inspect(path, instant(args.now)) for path in args.ledger]
        if len({(report["host"], report["epoch_id"]) for report in reports}) != len(reports):
            raise ValueError("oracle expects distinct host/epoch identities")
        print(json.dumps({"schema": 1, "scope": "offline metadata arithmetic only", "all_green": False,
                          "hosts": sorted(reports, key=lambda value: (value["host"], value["epoch_id"]))}, indent=2, sort_keys=True))
        return 0
    except (ValueError, KeyError, OSError, TypeError) as error:
        print(str(error), file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
