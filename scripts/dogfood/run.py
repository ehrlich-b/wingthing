#!/usr/bin/env python3
"""Run the canary once; retain at most 24 receipts, each at most 256 KiB."""
import collections
import datetime
import fcntl
import json
import os
import pathlib
import signal
import subprocess
import sys
import threading
import time

REPO = pathlib.Path(__file__).resolve().parents[2]
LOG_LIMIT = 256 * 1024
LOG_COUNT = 24


def main():
    os.umask(0o077)
    logs = REPO / ".scratch/dogfood-logs"
    logs.mkdir(mode=0o700, parents=True, exist_ok=True)
    with (logs / "run.lock").open("a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            print(json.dumps({"check": "runner", "status": "skip", "latency_ms": 0,
                              "error": None, "reason": "previous canary is still running"}), flush=True)
            return 0
        stamp = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%S.%fZ")
        receipt = logs / (stamp + ".jsonl")
        for stale in sorted(logs.glob("*.jsonl"))[:-(LOG_COUNT - 1)]:
            stale.unlink()
        started = time.monotonic()
        proc = subprocess.Popen([str(REPO / "scripts/dogfood/canary.sh")] + sys.argv[1:],
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
        stderr = collections.deque(maxlen=32)
        def drain_errors():
            while True:
                chunk = proc.stderr.read(1024)
                if not chunk:
                    return
                stderr.append(chunk)
        reader = threading.Thread(target=drain_errors, daemon=True)
        reader.start()
        expired = threading.Event()
        stopped = threading.Event()
        def terminate():
            if proc.poll() is None:
                os.killpg(proc.pid, signal.SIGTERM)
        def watchdog():
            if stopped.wait(12 * 60):
                return
            expired.set()
            terminate()
            if not stopped.wait(60) and proc.poll() is None:
                os.killpg(proc.pid, signal.SIGKILL)
        timer = threading.Thread(target=watchdog, daemon=True)
        timer.start()
        def interrupted(signum, frame):
            terminate()
        signal.signal(signal.SIGTERM, interrupted)
        signal.signal(signal.SIGINT, interrupted)
        failed = False
        count = size = 0
        with receipt.open("w") as output:
            def emit(record):
                nonlocal size
                line = json.dumps(record, ensure_ascii=False) + "\n"
                size += len(line.encode())
                if size <= LOG_LIMIT:
                    output.write(line)
                    output.flush()
                print(line, end="", flush=True)
            try:
                while True:
                    line = proc.stdout.readline(16385)
                    if not line:
                        break
                    if len(line) > 16384 or size > LOG_LIMIT - 16384:
                        failed = True
                        terminate()
                        break
                    try:
                        record = json.loads(line)
                        if not isinstance(record, dict) or record.get("status") not in ("pass", "fail", "skip", "unsupported-on-linux"):
                            raise ValueError("invalid canary record")
                        require_keys = {"check", "status", "latency_ms", "error"}
                        if not require_keys <= record.keys():
                            raise ValueError("missing canary record fields")
                        failed |= record["status"] == "fail"
                        count += 1
                        emit(record)
                    except (ValueError, UnicodeError):
                        failed = True
                        terminate()
                        break
                code = proc.wait(timeout=60)
            except subprocess.TimeoutExpired:
                os.killpg(proc.pid, signal.SIGKILL)
                code = proc.wait(timeout=5)
                failed = True
            finally:
                stopped.set()
                reader.join(timeout=1)
                proc.stdout.close()
                proc.stderr.close()
            failed |= code != 0 or count != 17 or expired.is_set()
            emit({"check": "runner", "status": "fail" if failed else "pass",
                  "latency_ms": round((time.monotonic() - started) * 1000, 2),
                  "error": ("canary failed/timed out or emitted incomplete JSONL; " +
                            b"".join(stderr).decode(errors="replace")[-4096:]) if failed else None,
                  "exit_code": code, "checks": count, "receipt": str(receipt)})
        return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
