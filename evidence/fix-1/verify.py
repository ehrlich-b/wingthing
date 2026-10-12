#!/usr/bin/env python3
"""Collect a fresh three-run comparison without replacing baseline evidence."""
from pathlib import Path
import hashlib
import json
import os
import re
import shutil
import subprocess

ROOT = Path(__file__).resolve().parents[2]
DEST = Path(__file__).resolve().parent
baseline = json.loads((ROOT / 'evidence/summary.json').read_text())
expected = {c['test'] for c in baseline['cards'] if c['test']}
env = dict(os.environ, GOFLAGS='-p=4',
           GOCACHE=str(ROOT / '.scratch/northstar/go-build'),
           GOMODCACHE=str(ROOT / '.scratch/northstar/go-mod'))
original_hashes = json.loads((ROOT / 'evidence/test-source-hashes.json').read_text())
for name, digest in original_hashes.items():
    assert hashlib.sha256((ROOT / name).read_bytes()).hexdigest() == digest, name
results = []
for n in range(1, 4):
    dest = DEST / f'run-{n}'
    dest.mkdir()  # Refuse to overwrite earlier evidence.
    events = []
    args = ['nice', '-n', '15', 'go', 'test', '-json', '-count=1',
            '-run', '^TestStoryboard', '-timeout', '20m', './cmd/wt']
    with (dest / 'go-test.jsonl').open('w') as log:
        proc = subprocess.Popen(args, cwd=ROOT, env=env, stdout=subprocess.PIPE,
                                stderr=subprocess.STDOUT, text=True)
        for line in proc.stdout:
            log.write(line)
            log.flush()
            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                continue
            events.append(event)
            if event.get('Action') in ['pass', 'fail', 'skip'] and event.get('Test', '').startswith('TestStoryboard') and '/' not in event['Test']:
                print(f'run {n}: {event["Test"]}: {event["Action"]}', flush=True)
        code = proc.wait()
    statuses = {e['Test']: e['Action'].upper() for e in events
                if e.get('Test', '').startswith('TestStoryboard') and e.get('Action') in ['pass', 'fail', 'skip']}
    parents = {k: v for k, v in statuses.items() if '/' not in k}
    assert set(parents) == expected and 'SKIP' not in statuses.values(), parents
    cards = dest / 'cards'
    cards.mkdir()
    for name in expected:
        (cards / f'{name}.txt').write_text(''.join(e.get('Output', '') for e in events if e.get('Test', '').split('/')[0] == name))
    for e in events:
        match = re.search(r'PTY screen transcript: (.+)\.txt', e.get('Output', ''))
        if match:
            for suffix in ['.txt', '.ansi']:
                src = Path(match[1] + suffix)
                dst = dest / 'transcripts' / src.relative_to(ROOT / '.scratch')
                dst.parent.mkdir(parents=True, exist_ok=True)
                shutil.copy2(src, dst)
    for directory in ['northstar/northstar-evidence', 'evidence/09/pty', 'storyboard-evidence']:
        src = ROOT / '.scratch' / directory
        if src.exists():
            shutil.copytree(src, dest / 'journey-receipts' / directory, dirs_exist_ok=True)
            shutil.rmtree(src)
    record = {'run': n, 'command': args, 'exit_code': code, 'statuses': parents,
              'all_test_statuses': statuses}
    (dest / 'results.json').write_text(json.dumps(record, indent=2) + '\n')
    results.append(record)
all_names = set().union(*(r['all_test_statuses'] for r in results))
flakes = {name: [r['all_test_statuses'].get(name, 'NOT-RUN') for r in results]
          for name in sorted(all_names)
          if len({r['all_test_statuses'].get(name, 'NOT-RUN') for r in results}) > 1}
before = {c['test']: c['status'] for c in baseline['cards'] if c['test']}
summary = {'base_commit': '580d4d9', 'runs': results, 'flaky_tests': flakes,
           'before': before, 'after': results[0]['statuses'],
           'fixed': [name for name in sorted(expected) if before[name] == 'FAIL' and all(r['statuses'][name] == 'PASS' for r in results)],
           'regressions': [name for name in sorted(expected) if before[name] == 'PASS' and any(r['statuses'][name] != 'PASS' for r in results)],
           'test_source_hashes': original_hashes}
(DEST / 'summary.json').write_text(json.dumps(summary, indent=2) + '\n')
print(json.dumps({k: summary[k] for k in ['fixed', 'regressions', 'flaky_tests']}), flush=True)
