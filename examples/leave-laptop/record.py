#!/usr/bin/env python3
"""Record real wt terminal detach/return/exit in a new isolated workspace."""
import argparse
import errno
import hashlib
import json
import os
from pathlib import Path
import pty
import re
import select
import shutil
import struct
import subprocess
import sys
import tempfile
import termios
import time
import fcntl


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def run(binary, parent):
    root = Path(tempfile.mkdtemp(prefix='j.', dir=parent)).resolve()
    print(f'Retaining recording workspace: {root}', file=sys.stderr)
    for directory in ('home', 'tmp', 'work'):
        (root / directory).mkdir(mode=0o700)
    job = Path(__file__).with_name('job.py').resolve()
    shutil.copyfile(job, root / 'work/job.py')
    env = {'PATH': os.environ['PATH'], 'HOME': str(root / 'home'),
           'TMPDIR': str(root / 'tmp'), 'TERM': 'xterm-256color', 'LANG': 'C',
           'SHELL': '/bin/sh', 'WINGTHING_DIR': str(root / 's'),
           'WINGTHING_PREVIEW_DIR': str(root / 's')}
    started = time.monotonic()
    session = None
    viewer = None
    master = None
    commands = []
    phases = []
    with (root / 'journey.cast').open('x') as cast, (root / 'terminal.txt').open('xb') as transcript:
        cast.write(json.dumps({'version': 2, 'width': 100, 'height': 30,
                               'timestamp': int(time.time()), 'title': 'Leave and return to a local synthetic job',
                               'env': {'TERM': env['TERM']}}) + '\n')

        def output(data):
            cast.write(json.dumps([round(time.monotonic() - started, 6), 'o', data.decode('utf-8', 'replace')]) + '\n')
            cast.flush()
            transcript.write(data)
            transcript.flush()

        def phase(name):
            phases.append({'phase': name, 'seconds': round(time.monotonic() - started, 3)})
            output(f'\r\n\x1b[1;36m{name}\x1b[0m\r\n'.encode())

        def cli(*args, check=True):
            result = subprocess.run([str(binary), *args], env=env, cwd=root / 'work',
                                    capture_output=True, timeout=25)
            commands.append({'argv': list(args), 'exit_code': result.returncode,
                             'stdout': result.stdout.decode('utf-8', 'replace'),
                             'stderr': result.stderr.decode('utf-8', 'replace')})
            if check and result.returncode:
                raise RuntimeError(f'wt {args}: {result.stderr.decode(errors="replace")}')
            return result

        def inventory(name):
            rows = json.loads(cli('session', 'ps', '--json').stdout)
            (root / name).write_text(json.dumps(rows, indent=2) + '\n')
            return rows

        def attach():
            nonlocal viewer, master
            master, slave = pty.openpty()
            fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack('HHHH', 30, 100, 0, 0))
            viewer = subprocess.Popen([str(binary), 'attach', session], env=env,
                                      cwd=root / 'work', stdin=slave, stdout=slave, stderr=slave)
            os.close(slave)

        def read():
            if select.select([master], [], [], 0.1)[0]:
                try:
                    data = os.read(master, 65536)
                except OSError as error:
                    if error.errno != errno.EIO:
                        raise
                    return b''
                output(data)
                return data
            return b''

        def expect(pattern):
            data = b''
            deadline = time.monotonic() + 15
            while time.monotonic() < deadline:
                data += read()
                match = re.search(pattern, data)
                if match:
                    return match
                if viewer.poll() is not None:
                    break
            raise AssertionError(f'Missing {pattern!r}; observed {data!r}')

        def close_viewer():
            nonlocal viewer, master
            viewer.wait(timeout=10)
            while select.select([master], [], [], 0)[0]:
                if not read():
                    break
            code = viewer.returncode
            os.close(master)
            master = None
            viewer = None
            if code:
                raise AssertionError(f'Viewer exited with {code}')

        try:
            phase('Start a synthetic job on this computer')
            launch = json.loads(cli('terminal', '--unsandboxed', '--json', '--name',
                                    'synthetic-work', '--cwd', str(root / 'work'), '--',
                                    sys.executable, '-u', str(root / 'work/job.py')).stdout)
            session = launch['session']
            assert launch['isolation'] == 'outer-boundary', launch
            (root / 'launch.json').write_text(json.dumps(launch, indent=2) + '\n')
            attach()
            pid = int(expect(rb'JOB_PID=(\d+)').group(1))
            # The initial snapshot can contain both markers in one read.
            os.write(master, b'start\n')
            expect(rb'Progress 1/8')
            phase('Leave: Ctrl+B then q closes the viewer')
            os.write(master, b'\x02q')
            expect(rb'\[detached from ')
            close_viewer()
            before = json.loads((root / 'work/progress.json').read_text())
            assert before['pid'] == pid and before['step'] < 8, before
            assert session in {row['id'] for row in inventory('away.json')}
            cli('session', 'wait', session, '--contains', 'Ready to finish', '--timeout', '15s')
            after = json.loads((root / 'work/progress.json').read_text())
            assert after == {'pid': pid, 'step': 8}, after
            os.kill(pid, 0)
            phase('Return: open the same session; work advanced while away')
            attach()
            expect(rb'Ready to finish')
            os.write(master, b'status\n')
            returned_pid = int(expect(rb'SAME_JOB_PID=(\d+) progress=8').group(1))
            assert returned_pid == pid
            phase('Finish: observe job output and confirmed terminal exit')
            os.write(master, b'finish\n')
            expect(rb'JOB_DONE: 8/8')
            close_viewer()
            # Discovery may briefly race the egg's socket shutdown.
            deadline = time.monotonic() + 10
            while session in {row['id'] for row in inventory('ended.json')}:
                if time.monotonic() >= deadline:
                    raise AssertionError('Ended session remains active')
                time.sleep(0.1)
            assert (root / 'work/result.txt').read_text() == 'Synthetic work complete: 8/8\n'
            ended_read = cli('session', 'read', session, '--json', check=False)
            assert ended_read.returncode != 0, 'This fixture expects active-only terminal reads'
            phase('Ended: result retained; active terminal read is unavailable')
            receipt = {'scope': 'local synthetic terminal; no provider, network, laptop sleep or sandbox enforcement proof',
                       'session_id': session, 'job_pid': pid, 'returned_job_pid': returned_pid,
                       'isolation': launch['isolation'], 'away_progress': [before, after],
                       'viewer_detach_exit': 0, 'completion_attach_exit': 0,
                       'ended_read_exit': ended_read.returncode, 'phases': phases,
                       'binary_sha256': sha(binary), 'job_sha256': sha(job),
                       'recording_sha256': sha(root / 'journey.cast'),
                       'result_sha256': sha(root / 'work/result.txt')}
            (root / 'receipt.json').write_text(json.dumps(receipt, indent=2) + '\n')
            print(f'PASS: detached work advanced; same session {session} and PID {pid}; confirmed completion')
            print(f'Evidence: {root}')
        finally:
            if viewer is not None:
                viewer.kill()
                viewer.wait(timeout=10)
            if master is not None:
                os.close(master)
            # Stop only this invocation's exact session on failure. Keep all files.
            if session:
                cli('session', 'kill', session, check=False)
            (root / 'commands.json').write_text(json.dumps(commands, indent=2) + '\n')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--wt', type=Path, required=True, help='explicit locally built wt binary')
    parser.add_argument('--output-parent', type=Path, required=True, help='existing directory for retained private evidence')
    args = parser.parse_args()
    binary = args.wt.resolve(strict=True)
    parent = args.output_parent.resolve(strict=True)
    if not parent.is_dir() or not os.access(binary, os.X_OK):
        parser.error('Require an existing output directory and executable wt')
    run(binary, parent)
