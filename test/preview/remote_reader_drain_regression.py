#!/usr/bin/env python3
"""Owned-child regression for remote_isolation.py's read-only reader drain.

A local child stands in for an attach --read-only reader: it echoes stdin
until EOF, then reports detach on stderr. No build, wt binary, SSH, provider,
browser, or auth.
"""

from pathlib import Path
import subprocess
import sys

CHILD = "import sys\nsys.stdout.write(sys.stdin.read())\nsys.stderr.write('detached from fixture\\n')\n"


def reader():
    child = subprocess.Popen([sys.executable, "-c", CHILD], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    child.stdin.write("reader-canary\n")
    return child


def drain(child, close_first):
    if close_first:
        child.stdin.close()
    try:
        out, err = child.communicate(timeout=15)
    except subprocess.TimeoutExpired:
        child.kill()
        out, err = child.communicate()
    return child.returncode, out, err


def fail(message):
    print(f"FAIL: {message}")
    sys.exit(1)


# Old sequence: close then communicate flushes the closed stream. Newer
# CPython swallows that ValueError inside communicate(); the WSL interpreter
# does not, so prove the unguarded flush directly as well.
old = reader()
try:
    drain(old, close_first=True)
    print(f"old drain: Python {sys.version.split()[0]} communicate() tolerates closed stdin")
except ValueError as e:
    if "closed file" not in str(e):
        fail(f"unexpected ValueError: {e}")
    print(f"old drain reproduces on Python {sys.version.split()[0]}: ValueError: {e}")
finally:
    if old.poll() is None:
        old.kill()
    old.wait()
try:
    old.stdin.flush()
    fail("flush of closed reader stdin did not raise")
except ValueError as e:
    if "closed file" not in str(e):
        fail(f"unexpected ValueError: {e}")
    print(f"old drain mechanism: unguarded communicate() flush raises ValueError: {e}")

# Fixed sequence: communicate closes stdin itself, the child sees EOF.
code, out, err = drain(reader(), close_first=False)
if code != 0 or "reader-canary" not in out or "detached from" not in err:
    fail(f"fixed drain: rc={code} stdout={out!r} stderr={err!r}")
print("fixed drain: rc=0, child saw EOF, output and detach collected")

fixture = (Path(__file__).parent / "remote_isolation.py").read_text()
if "reader.stdin.close()" in fixture:
    fail("remote_isolation.py still closes reader stdin before communicate()")
print("remote_isolation.py uses the fixed drain")
