#!/usr/bin/env python3
"""Outside-worker native confinement/loopback gate with disposable state."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--go', required=True)
    args = parser.parse_args()
    repo = Path(__file__).resolve().parent.parent
    root = Path(tempfile.mkdtemp(prefix='launch-native-', dir=repo / '.scratch'))
    env = dict(os.environ)
    for name in ('home', 'tmp', 'state'):
        (root / name).mkdir(mode=0o700)
    env.update(HOME=str(root / 'home'), TMPDIR=str(root / 'tmp'),
               GOTMPDIR=str(root / 'tmp'), WINGTHING_DIR=str(root / 'state'),
               WINGTHING_PREVIEW_DIR=str(root / 'state'), GOMAXPROCS='2')
    # Both backends exercise actual filesystem denial, plus disposable TCP proxy
    # decisions. No example.com/curl network test or installed provider probe.
    selection = ('^(TestJail_(WriteInsideMount|WriteOutsideMount|DenyPathBlocked)'
                 '|TestProxy(RecordsEgressEvents|ObserveModeDoesNotBlock))$')
    log = root / 'native.jsonl'
    with log.open('xb') as output:
        proc = subprocess.run([args.go, 'test', '-p', '2', '-tags', 'integration',
                               '-json', '-count=1', '-timeout=2m',
                               './internal/sandbox', '-run', selection], cwd=repo,
                              env=env, stdout=output, stderr=subprocess.STDOUT,
                              stdin=subprocess.DEVNULL, timeout=150, check=False)
    counts = {'pass': 0, 'fail': 0, 'skip': 0}
    for line in log.read_text().splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if 'Test' in event and event.get('Action') in counts:
            counts[event['Action']] += 1
    if proc.returncode == 0 and counts['pass'] >= 5 and counts['fail'] == 0 and counts['skip'] == 0:
        status = 'PASS'
    elif counts['fail'] or (proc.returncode and counts['pass']):
        status = 'FAIL'
    else:
        status = 'REQUIRED-UNRUN'
    receipt = {'status': status, 'counts': counts, 'host': os.uname().sysname,
               'log': str(log), 'scope': 'native sandbox and disposable TCP proxy; no provider'}
    (root / 'receipt.json').write_text(json.dumps(receipt, indent=2) + '\n')
    print(json.dumps(receipt, indent=2))
    if status != 'PASS':
        raise SystemExit(1 if status == 'FAIL' else 2)


if __name__ == '__main__':
    main()
