#!/usr/bin/env python3
"""Run once under a lock and watchdog; retain 24 receipts of at most 256 KiB."""
import collections
import datetime
import fcntl
import hashlib
import json
import os
import pathlib
import queue
import signal
import socket
import subprocess
import sys
import threading
import tempfile
import time

from contracts import CHECKS, LOG_COUNT, LOG_LIMIT, STATUSES, read_json, read_receipt, source_identity, timestamp, utc
from ledger import (LEDGER_NAME, append_record, begin_record, checked, cleanup_hold,
                    ensure_epoch, result_record, rotation_ready, _sync_directory)

REPO = pathlib.Path(__file__).resolve().parents[2]


def save_state(logs, state):
    descriptor, name = tempfile.mkstemp(prefix="runner-state-", dir=logs)
    temporary = pathlib.Path(name)
    try:
        with os.fdopen(descriptor, "w") as output:
            json.dump(state, output)
            output.write("\n")
            output.flush()
            os.fsync(output.fileno())
        temporary.replace(logs / "runner-state.json")
        _sync_directory(logs)
    finally:
        temporary.unlink(missing_ok=True)


def history(logs):
    """Recover legacy evidence too; a lock never supersedes a completed failure."""
    ledger_path = logs / LEDGER_NAME
    if ledger_path.exists() or ledger_path.is_symlink():
        hold = cleanup_hold(checked(ledger_path))
        if hold:
            return hold
    state_file = logs / "runner-state.json"
    if state_file.exists():
        state = read_json(state_file)
        if (not isinstance(state, dict) or state.get("schema") != 1 or
                state.get("phase") not in ("running", "complete") or
                state.get("cleanup_status") not in ("pass", "fail", "unknown")):
            raise ValueError("invalid runner state; preserve and inspect it")
        return state
    receipts = sorted(logs.glob("*.jsonl"))
    if not receipts:
        return {}
    records = read_receipt(receipts[-1])
    runner = records[-1] if records and records[-1].get("check") == "runner" else {}
    cleanup = next((record for record in reversed(records) if record.get("check") == "cleanup"), {})
    return {"previous_failure": runner.get("status") != "pass", "phase": "complete" if runner else "running",
            "cleanup_status": cleanup.get("status", "unknown"), "receipt": str(receipts[-1])}


def run_once(repo=REPO, arguments=None, timeout_seconds=12 * 60, cleanup_seconds=60):
    """Fixture API can shorten time; the CLI always retains the 12-minute bound."""
    os.umask(0o077)
    arguments = sys.argv[1:] if arguments is None else arguments
    logs = repo / ".scratch/dogfood-logs"
    logs.mkdir(mode=0o700, parents=True, exist_ok=True)

    def refuse(reason, code=1, **detail):
        print(json.dumps({"check": "runner", "status": "fail" if code == 1 else "skip",
                          "latency_ms": 0, "error": reason if code == 1 else None,
                          "reason": reason, "timestamp": timestamp(), **detail}), flush=True)
        return code

    with (logs / "run.lock").open("a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            try:
                previous = history(logs)
                failed = previous.get("previous_failure", False)
            except (OSError, ValueError):
                failed = True
            return refuse("previous canary is still running", 1 if failed else 0,
                          previous_failure=failed)
        try:
            previous = history(logs)
        except (OSError, ValueError) as error:
            return refuse("unreadable prior evidence; inspect before continuing: " + str(error))
        unresolved = bool(previous) and (previous.get("phase") == "running" or previous.get("cleanup_status") != "pass")
        epoch = os.environ.get("WT_DOGFOOD_EPOCH")
        until = os.environ.get("WT_DOGFOOD_UNTIL")
        now = datetime.datetime.now(datetime.timezone.utc)
        try:
            if bool(epoch) != bool(until) or (os.environ.get("WT_DOGFOOD_REPO") and not until):
                raise ValueError("finite scheduling requires both WT_DOGFOOD_EPOCH and WT_DOGFOOD_UNTIL")
            if epoch and (utc(epoch) >= utc(until) or now < utc(epoch)):
                raise ValueError("invalid or future finite epoch")
            expired = bool(until and now >= utc(until))
        except ValueError as error:
            return refuse(str(error))
        if unresolved:
            return refuse("unresolved previous cleanup or interrupted runner; inspect retained state before continuing",
                          previous_receipt=previous.get("receipt"), expired=expired)
        ledger_view = None
        ledger_path = logs / LEDGER_NAME
        try:
            if ledger_path.exists() and not epoch:
                raise ValueError("existing finite ledger requires its original epoch environment")
            if epoch and (not expired or ledger_path.exists()):
                ledger_view = ensure_epoch(logs, epoch, until, socket.gethostname())
                if cleanup_hold(ledger_view):
                    raise ValueError("unresolved previous cleanup in imported ledger")
        except (OSError, ValueError, TypeError) as error:
            return refuse(str(error))
        if expired:
            return refuse("finite epoch expired; no new canary started", 1 if previous.get("previous_failure") else 3,
                          epoch=epoch, until=until, previous_failure=previous.get("previous_failure", False))

        stamp = now.strftime("%Y%m%dT%H%M%S.%fZ")
        receipt = logs / (stamp + ".jsonl")
        identity = source_identity(repo)
        attempt = None
        stale_receipts = sorted(logs.glob("*.jsonl"))[:-(LOG_COUNT - 1)]
        try:
            if ledger_view:
                rotation_ready(ledger_path, stale_receipts)
                binary = repo / "wt"
                for index, argument in enumerate(arguments):
                    if argument == "--wt" and index + 1 < len(arguments):
                        binary = pathlib.Path(arguments[index + 1])
                    elif argument.startswith("--wt="):
                        binary = pathlib.Path(argument.split("=", 1)[1])
                binary = binary.resolve()
                binary_hash = None
                if binary.is_file() and binary.is_relative_to(repo.resolve()):
                    with binary.open("rb") as stream:
                        digest = hashlib.sha256()
                        for chunk in iter(lambda: stream.read(65536), b""):
                            digest.update(chunk)
                        binary_hash = digest.hexdigest()
                attempt = begin_record(ledger_view["header"], stamp, receipt.name, timestamp(now),
                                       dict(identity, binary_sha256=binary_hash))
                append_record(ledger_path, attempt, reserve_result=True)
            for stale in stale_receipts:
                stale.unlink()
        except (OSError, ValueError) as error:
            return refuse(str(error))
        state = {"schema": 1, "phase": "running", "started_at": timestamp(now), "receipt": str(receipt),
                 "epoch": epoch, "until": until, "previous_failure": previous.get("previous_failure", False),
                 "cleanup_status": "unknown", **identity}
        save_state(logs, state)
        started = time.monotonic()
        stderr = collections.deque(maxlen=32)
        events = queue.Queue(maxsize=16)
        done = threading.Event()
        seen = set()
        failed = False
        reason = None
        cleanup = "unknown"
        binary_hashes = set()
        interrupted_signal = None
        proc = None
        count = size = 0

        with receipt.open("w") as output:
            def emit(record):
                nonlocal size
                line = json.dumps(record, ensure_ascii=False) + "\n"
                encoded_size = len(line.encode())
                if size + encoded_size > LOG_LIMIT:
                    raise ValueError("receipt budget exceeded")
                size += encoded_size
                output.write(line)
                output.flush()
                print(line, end="", flush=True)

            def terminate(force=False):
                if proc is not None and proc.poll() is None:
                    try:
                        os.killpg(proc.pid, signal.SIGKILL if force else signal.SIGTERM)
                    except ProcessLookupError:
                        pass

            def interrupted(signum, frame):
                nonlocal interrupted_signal
                interrupted_signal = signum
                terminate()
                try:
                    events.put_nowait(("interrupted", signum))
                except queue.Full:
                    pass

            old_handlers = {sig: signal.signal(sig, interrupted) for sig in (signal.SIGTERM, signal.SIGINT)}
            threads = []
            code = None
            try:
                proc = subprocess.Popen([str(repo / "scripts/dogfood/canary.sh")] + arguments,
                                        stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                        start_new_session=True, bufsize=0)

                def drain_errors():
                    try:
                        while not done.is_set():
                            chunk = proc.stderr.read(1024)
                            if not chunk:
                                return
                            stderr.append(chunk)
                    except (OSError, ValueError):
                        return

                def drain_records():
                    try:
                        while not done.is_set():
                            line = proc.stdout.readline(16385)
                            while not done.is_set():
                                try:
                                    events.put(("line", line), timeout=1)
                                    break
                                except queue.Full:
                                    continue
                            if not line or len(line) > 16384:
                                return
                    except (OSError, ValueError):
                        return

                for action in (drain_errors, drain_records):
                    thread = threading.Thread(target=action, daemon=True)
                    thread.start()
                    threads.append(thread)
                deadline = started + timeout_seconds
                stopping = False
                while True:
                    try:
                        kind, line = events.get(timeout=max(0, deadline - time.monotonic()))
                    except queue.Empty:
                        if stopping:
                            terminate(force=True)
                            break
                        failed = stopping = True
                        reason = "12-minute watchdog expired"
                        terminate()
                        deadline = time.monotonic() + cleanup_seconds
                        continue
                    if kind == "interrupted":
                        failed = True
                        terminate()
                        reason = "runner interrupted by signal " + str(line)
                        if not stopping:
                            stopping = True
                            deadline = time.monotonic() + cleanup_seconds
                        continue
                    if not line:
                        break
                    try:
                        if len(line) > 16384 or size > LOG_LIMIT - 16384:
                            raise ValueError("oversized canary output")
                        record = json.loads(line)
                        if not isinstance(record, dict) or record.get("status") not in STATUSES:
                            raise ValueError("invalid canary record")
                        if not {"check", "status", "latency_ms", "error"} <= record.keys():
                            raise ValueError("missing canary record fields")
                        check = record["check"]
                        if check not in CHECKS or check in seen:
                            raise ValueError("unexpected or duplicate canary check: " + str(check))
                        seen.add(check)
                        failed |= record["status"] == "fail"
                        count += 1
                        if check == "cleanup":
                            cleanup = record["status"]
                        if record.get("binary_sha256"):
                            binary_hashes.add(record["binary_sha256"])
                        emit(record)
                    except (ValueError, UnicodeError) as error:
                        failed = True
                        reason = str(error)
                        terminate()
                        break
                try:
                    code = proc.wait(timeout=cleanup_seconds)
                except subprocess.TimeoutExpired:
                    terminate(force=True)
                    code = proc.wait(timeout=5)
                    failed = True
                    reason = reason or "canary did not exit within cleanup grace"
            except (OSError, ValueError) as error:
                failed = True
                reason = str(error)
            finally:
                done.set()
                for sig, handler in old_handlers.items():
                    signal.signal(sig, handler)
                for thread in threads:
                    thread.join(timeout=0.2)
                if proc is not None:
                    proc.stdout.close()
                    proc.stderr.close()
            failed |= (code != 0 or seen != set(CHECKS) or cleanup != "pass" or
                       len(binary_hashes) > 1 or interrupted_signal is not None)
            if interrupted_signal is not None:
                reason = reason or "runner interrupted by signal " + str(interrupted_signal)
            finished = timestamp()
            runner = {"check": "runner", "status": "fail" if failed else "pass",
                      "latency_ms": round((time.monotonic() - started) * 1000, 2),
                      "error": ((reason or "canary failed or emitted incomplete JSONL") + "; " +
                                b"".join(stderr).decode(errors="replace"))[-4096:] if failed else None,
                      "timestamp": finished, "started_at": state["started_at"], "platform": sys.platform,
                      "exit_code": code, "checks": count, "receipt": str(receipt), "cleanup_status": cleanup,
                      "epoch": epoch, "until": until, "binary_sha256": next(iter(binary_hashes), None), **identity}
            emit(runner)
            output.flush()
            os.fsync(output.fileno())
        if attempt:
            try:
                append_record(ledger_path, result_record(attempt, read_receipt(receipt),
                              hashlib.sha256(receipt.read_bytes()).hexdigest(), finished))
            except (OSError, ValueError, TypeError) as error:
                return refuse("result persistence failed; running state retained: " + str(error))
        state.update(phase="complete", finished_at=finished, previous_failure=failed,
                     cleanup_status=cleanup, runner_status=runner["status"])
        save_state(logs, state)
        return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(run_once())
