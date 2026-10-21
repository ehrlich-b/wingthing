"""Finite, credential-free terminal work for the leave/return recording."""
import json
import os
from pathlib import Path
import select
import sys
import time


def command():
    if not select.select([sys.stdin], [], [], 30)[0]:
        raise TimeoutError('No instruction within 30 seconds')
    value = sys.stdin.readline().strip()
    if not value:
        raise RuntimeError('Terminal input closed')
    return value


print(f'JOB_PID={os.getpid()}', flush=True)
print('Ready: type start', flush=True)
if command() != 'start':
    raise ValueError('Expected start')
for step in range(1, 9):
    time.sleep(0.4)
    Path('progress.json').write_text(json.dumps({'pid': os.getpid(), 'step': step}) + '\n')
    print(f'Progress {step}/8', flush=True)
print('Ready to finish', flush=True)
if command() != 'status':
    raise ValueError('Expected status')
print(f'SAME_JOB_PID={os.getpid()} progress=8', flush=True)
if command() != 'finish':
    raise ValueError('Expected finish')
Path('result.txt').write_text('Synthetic work complete: 8/8\n')
print('JOB_DONE: 8/8', flush=True)
