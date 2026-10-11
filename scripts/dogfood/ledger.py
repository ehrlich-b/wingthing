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
