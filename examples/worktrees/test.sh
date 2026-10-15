#!/usr/bin/env bash
# Offline integration check: use the real built wt and harmless local shells.
set -euo pipefail
example_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
[[ -f "$example_dir/demo.sh" ]] || { echo 'FAIL: runnable worktree example is missing' >&2; exit 1; }
export WT_BIN=${WT_BIN:-"$example_dir/../../wt"}
python3 - "$example_dir/demo.sh" <<'PY'
import json
import os
from pathlib import Path
import pty
import re
import select
import subprocess
import sys
import tempfile
import time

script = sys.argv[1]

def demo(*args, check=True, env=None):
    result = subprocess.run(['bash', script, *map(str, args)],
                            capture_output=True, text=True, timeout=30, env=env)
    if check and result.returncode:
        raise AssertionError(f'demo {args}: {result.stdout}\n{result.stderr}')
    return result

with tempfile.TemporaryDirectory(prefix='wt-test-', dir=os.environ.get('TMPDIR')) as tmp:
    parent = Path(tmp)
    # Caller-owned Git work, files, state and a live terminal must survive.
    caller = parent / 'caller work'
    caller.mkdir()
    subprocess.run(['git', 'init', '-q', str(caller)], check=True)
    sentinel = caller / 'untracked.txt'
    sentinel.write_text('keep caller work\n')
    foreign_env = dict(os.environ, WINGTHING_DIR=str(parent / 'caller-state'),
                       WINGTHING_PREVIEW_DIR=str(parent / 'caller-state'))
    foreign_env.pop('WT_MCP_CLIENT', None)
    wt = str(Path(os.environ['WT_BIN']).resolve())
    foreign = subprocess.run([wt, 'terminal', '--unsandboxed', '--json', '--cwd', str(caller),
                              '--', '/bin/sh'], check=True, env=foreign_env,
                             capture_output=True, text=True, timeout=20)
    foreign_id = json.loads(foreign.stdout)['session']
    roots = []
    try:
        # Refuse cleanup of arbitrary directories, even a parent of demo state.
        assert demo('cleanup', caller, check=False).returncode != 0
        assert demo('cleanup', parent, check=False).returncode != 0
        for _ in range(2):
            result = demo('start', parent, env=foreign_env)
            root = Path(result.stdout.strip())
            roots.append(root)
            assert root.parent == parent.resolve(), result.stdout
        first, second = roots
        assert first != second
        manifests = [json.loads(demo('status', root).stdout) for root in roots]
        print('Session evidence: ' + json.dumps(manifests, sort_keys=True))
        ids = [item['id'] for manifest in manifests for item in manifest]
        assert len(ids) == len(set(ids)) == 4
        print('PASS: two independent invocations and four distinct real sessions')
        for root, manifest in zip(roots, manifests):
            assert {item['cwd'] for item in manifest} == {str(root / 'left'), str(root / 'right')}
            worktrees = subprocess.check_output(['git', '-C', str(root / 'repo'),
                                                 'worktree', 'list', '--porcelain'], text=True)
            assert worktrees.count('worktree ') == 3, worktrees
            for side in ('left', 'right'):
                work = root / side
                assert (work / '.git').is_file()
                assert (work / 'result.txt').read_text() == side + '\n'
                assert (work / 'cwd.txt').read_text() == str(work) + '\n'
                assert subprocess.check_output(['git', '-C', str(work), 'branch', '--show-current'],
                                               text=True).strip() == side
            print(f'PASS: {root.name}: exact CWD, separate branches and distinct files')

        def attach(root, side, token):
            master, slave = pty.openpty()
            proc = subprocess.Popen(['bash', script, 'attach', str(root), side],
                                    stdin=slave, stdout=slave, stderr=slave, close_fds=True)
            os.close(slave)
            output = b''
            def expect(pattern):
                nonlocal output
                deadline = time.monotonic() + 15
                while time.monotonic() < deadline:
                    match = re.search(pattern, output)
                    if match:
                        return match
                    if select.select([master], [], [], 0.1)[0]:
                        try:
                            output += os.read(master, 65536)
                        except OSError:
                            break
                raise AssertionError(f'attach missing {pattern!r}: {output!r}')
            try:
                expect(rb'\[attached to ')
                # Format pieces keep the full expected response out of echoed input.
                command = f"printf '%s pid=%s\\n' '{token}' \"$$\"\n"
                os.write(master, command.encode())
                pid = int(expect(re.escape(token.encode()) + rb' pid=([0-9]+)\r?\n').group(1))
                os.write(master, b'\x02q')
                proc.wait(timeout=10)
                expect(rb'\[detached from ')
                assert proc.returncode == 0
                os.kill(pid, 0)
                return pid
            finally:
                if proc.poll() is None:
                    proc.kill()
                    proc.wait()
                os.close(master)

        shell_pids = []
        for side in ('left', 'right'):
            pid = attach(first, side, f'{side}-first')
            shell_pids.append(pid)
            assert attach(first, side, f'{side}-again') == pid
            print(f'PASS: {side}: attach, Ctrl+B q detach, reattach preserves shell PID {pid}')
        assert len(set(shell_pids)) == 2
        (first / 'left' / 'result.txt').write_text('left changed\n')
        assert (first / 'right' / 'result.txt').read_text() == 'right\n'
        link = parent / 'demo-link'
        link.symlink_to(first, target_is_directory=True)
        assert demo('cleanup', link, check=False).returncode != 0
        link.unlink()
        # Failed stop must keep the state needed to retry cleanup.
        import shlex
        wrapper = parent / 'fail-stop'
        wrapper.write_text('#!/bin/sh\nif [ "$1 $2" = "session kill" ]; then exit 42; fi\nexec '
                           + shlex.quote(wt) + ' "$@"\n')
        wrapper.chmod(0o700)
        (first / 'wt-bin').write_text(str(wrapper) + '\n')
        assert demo('cleanup', first, check=False).returncode != 0
        assert first.exists()
        assert len(json.loads(demo('status', first).stdout)) == 2
        (first / 'wt-bin').write_text(wt + '\n')
        print('PASS: cleanup rejects a symlink and retains state when session stop fails')
        demo('cleanup', first)
        assert not first.exists()
        for pid in shell_pids:
            deadline = time.monotonic() + 5
            while True:
                try:
                    os.kill(pid, 0)
                except ProcessLookupError:
                    break
                assert time.monotonic() < deadline, f'shell {pid} survived cleanup'
                time.sleep(0.05)
        assert len(json.loads(demo('status', second).stdout)) == 2
        assert sentinel.read_text() == 'keep caller work\n'
        caller_sessions = json.loads(subprocess.check_output([wt, 'session', 'ps', '--json'], env=foreign_env))
        assert foreign_id in {item['id'] for item in caller_sessions}, caller_sessions
        # Fail the second launch after a real first shell has started.
        partial = parent / 'fail-second'
        partial.write_text('#!/bin/sh\nif [ "$1" = terminal ]; then\n'
                           '  for arg in "$@"; do [ "$arg" != right ] || exit 42; done\nfi\n'
                           'exec ' + shlex.quote(wt) + ' "$@"\n')
        partial.chmod(0o700)
        children = set(parent.iterdir())
        failed = demo('start', parent, check=False, env=dict(foreign_env, WT_BIN=str(partial)))
        assert failed.returncode != 0, failed.stdout
        assert set(parent.iterdir()) == children, failed.stderr
        assert len(json.loads(demo('status', second).stdout)) == 2
        print('PASS: partial start cleans its first real session and generated worktrees')
        demo('cleanup', second)
        assert not second.exists()
        print('PASS: scoped cleanup preserves caller repository, files, session and other demo')
    finally:
        for root in roots:
            if root.exists():
                (root / 'wt-bin').write_text(wt + '\n')
                demo('cleanup', root, check=False)
        subprocess.run([wt, 'session', 'kill', foreign_id], env=foreign_env, check=False,
                       capture_output=True, timeout=15)
PY
