#!/usr/bin/env python3
"""Generate a bounded, synthetic 14-day metadata-only two-host replay packet.

Never runs a canary, sleeps, contacts a host/provider, or changes existing output.
"""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import shutil
import tempfile

from contracts import CHECKS, MAILBOX, OPTIONAL, timestamp, utc
from ledger import (LEDGER_NAME, RECORD_LIMIT, TOTAL_LIMIT, begin_record, canonical, checked,
                    consume, create_ledger, epoch_header, planned_at)

EPOCH = "2026-10-10T00:00:00Z"
UNTIL = "2026-10-24T00:00:00Z"


def fixture_records(header):
    """Explicit model inputs, not substitutes for absent external receipts."""
    mac = header["host"] == "mac-fixture"
    missing = {0, 111, 447} if mac else {2, 222}
    for slot in range(448):
        if slot in missing:
            continue
        interrupted = mac and slot == 220
        failed = (mac and slot == 37) or (not mac and slot == 447)
        required_skip = slot == (91 if mac else 85)
        late = slot == (300 if mac else 150)
        for retry in range(2 if mac and slot == 37 else 1):
            start = timestamp(planned_at(header, slot) + datetime.timedelta(seconds=(70 if late else 0) + 30 * retry))
            name = "%s-%03d-%d" % (header["host"], slot, retry)
            binary = ("a" if slot < 224 else "b") * 64 if mac else "e" * 64
            identity = {"binary_sha256": binary, "source_commit": ("c" if slot < 224 else "f") * 40,
                        "source_sha256": None if not mac and slot == 144 else "d" * 64}
            attempt = begin_record(header, name, name + ".jsonl", start, identity)
            yield attempt
            if interrupted:
                continue
            checks = {key: "skip" if key in OPTIONAL else "unsupported-on-linux" if not mac and key in MAILBOX
                      else "pass" for key in CHECKS}
            this_failed = failed and retry == 0
            if this_failed:
                checks["wing_restart_mid_run" if mac else "cleanup"] = "fail"
            if required_skip:
                checks["local_only_daemon_boot"] = "skip"
            outcome = {"kind": "result", "attempt_id": name,
                       "finished_at": timestamp(utc(start) + datetime.timedelta(seconds=1)),
                       "runner_status": "fail" if this_failed else "pass", "exit_code": 1 if this_failed else 0,
                       "cleanup_status": checks["cleanup"], "checks": checks, "receipt_complete": True,
                       "binary_sha256": binary, "binary_consistent": True, "platform": "darwin" if mac else "linux",
                       "descendant_inventory": "verified"}
            outcome["receipt_sha256"] = hashlib.sha256(canonical({"synthetic_fixture_only": True, **outcome})).hexdigest()
            yield outcome


def rehearse(out):
    if out.exists() or out.is_symlink():
        raise ValueError("existing output and operator edits preserved; choose a new output directory")
    if not out.parent.is_dir():
        raise ValueError("output parent must already exist")
    temporary = Path(tempfile.mkdtemp(prefix="epoch-replay-", dir=out.parent))
    try:
        packet = {"schema": 1, "synthetic_fixture_only": True, "all_green": False,
                  "scope": "448 slots per host, 896 host slots; no native or live execution",
                  "epoch": EPOCH, "until": UNTIL, "limits": {"record_bytes": RECORD_LIMIT, "ledger_bytes": TOTAL_LIMIT},
                  "hosts": {}, "required_unrun": ["native built-binary canary and descendant inventory",
                       "actual finite job disconnect/recovery/Stop receipts", "genuine unattended use"]}
        for host in ("forge-fixture", "mac-fixture"):
            directory = temporary / host
            directory.mkdir(mode=0o700)
            header = epoch_header(EPOCH, UNTIL, host)
            path = directory / LEDGER_NAME
            create_ledger(path, header, fixture_records(header))
            view = checked(path)
            report = consume(view, utc(UNTIL))
            (directory / "report.json").write_text(json.dumps(report, indent=2, sort_keys=True) + "\n")
            packet["hosts"][host] = {"ledger": host + "/" + LEDGER_NAME,
                                    "sha256": hashlib.sha256(path.read_bytes()).hexdigest(),
                                    "bytes": path.stat().st_size, "counts": report["counts"],
                                    "cohorts": report["cohorts"], "attention_required": report["attention_required"]}
        (temporary / "packet.json").write_text(json.dumps(packet, indent=2, sort_keys=True) + "\n")
        # Destination was preflighted; rename publishes the whole new packet.
        temporary.rename(out)
        return packet
    finally:
        if temporary.exists():
            shutil.rmtree(temporary)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()
    os.umask(0o077)
    try:
        packet = rehearse(args.out)
    except (OSError, ValueError) as error:
        parser.exit(2, str(error) + "\n")
    print(json.dumps({"packet": str(args.out / "packet.json"), "synthetic_fixture_only": True,
                      "host_slots": sum(host["counts"]["planned_slots"] for host in packet["hosts"].values())}))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
