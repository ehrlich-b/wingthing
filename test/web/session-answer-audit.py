#!/usr/bin/env python3
"""Reproduce a pinned session-answer failure with finite offline fixtures."""
import hashlib
import io
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tarfile

REPO = Path(__file__).resolve().parents[2]
SUBJECT = json.loads((REPO / 'test/web/fixtures/session-answer-subject.json').read_text())
INPUTS = json.loads((REPO / 'test/web/fixtures/session-answer-audit-inputs.json').read_text())


def run(command, log=None, cwd=REPO, env=None):
    if log:
        with log.open('w') as stream:
            subprocess.run(['nice', '-n', '10', *command], cwd=cwd, env=env,
                           stdout=stream, stderr=subprocess.STDOUT, check=True)
        print(log.read_text(), end='')
    else:
        subprocess.run(['nice', '-n', '10', *command], cwd=cwd, env=env, check=True)


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def host():
    name = socket.gethostname()
    assert os.geteuid() != 0
    print(json.dumps({'hostname': name, 'uid': os.geteuid(), 'is_forge': name == 'forge',
                      'ordinary_tests_permitted': name != 'forge', 'privileged_operations': []}))
    # No operation in this diagnostic needs a kernel module or block device.


def history(folder):
    assert sha(folder / 'manifest.json') == INPUTS['evidence/history/manifest.json']
    manifest = json.loads((folder / 'manifest.json').read_text())
    assert len(manifest) == 3
    for attempt in manifest:
        count = 0
        for item in attempt['files']:
            source = Path(item['path'])
            if item.get('missing'):
                assert not source.exists(), source
                print('Documented absent input:', source)
                continue
            copied = folder.parent.parent / item['copy']
            assert sha(source) == sha(copied) == item['sha256'], source
            assert source.stat().st_size == item['bytes']
            # Decode every complete file, including each complete execution log.
            source.read_text()
            count += 1
        root = folder / attempt['attempt']
        receipt = json.loads((root / 'READINESS.json').read_text())
        assert all(c['exit_code'] == 0 for c in receipt['checks'])
        for c in receipt['checks']:
            assert sha(root / c['log']['path']) == c['log']['sha256']
        print(attempt['attempt'], 'read and hashed', count, 'files;', attempt['log_count'], 'logs; final declared checks all passed')
    print('Read oldest first and newest last; source directories unchanged; absent blocked reports are recorded, not fabricated.')


def diagnostics(folder):
    folder.mkdir(parents=True, exist_ok=True)
    scratch = folder.parent / 'scratch'
    scratch.mkdir(exist_ok=True)
    temporary = scratch / 'tmp'
    temporary.mkdir(exist_ok=True)
    # Extract immutable objects; no fixture ever edits the production modules.
    for destination, paths in [(folder / 'subject', list(SUBJECT['files'])),
                               (scratch / 'final-repo', [])]:
        destination.mkdir(parents=True, exist_ok=True)
        data = subprocess.check_output(['git', 'archive', SUBJECT['head'], *paths], cwd=REPO)
        with tarfile.open(fileobj=io.BytesIO(data)) as archive:
            archive.extractall(destination, filter='data')
    for file, digest in SUBJECT['files'].items():
        assert sha(folder / 'subject' / file) == digest, file
    source = scratch / 'final-repo'
    overlay = folder / 'transport-overlay.json'
    overlay.write_text(json.dumps({'Replace': {
        str(source / 'internal/egg/session_answer_diagnostic_test.go'):
        str(REPO / 'test/web/fixtures/session-answer-transport.go.txt')
    }}, indent=2) + '\n')
    env = dict(os.environ, GOPROXY='off', GOSUMDB='off', TMPDIR=str(temporary),
               SESSION_ANSWER_OBSERVATION=str(folder / 'transport-observations.json'))
    run(['go', 'test', '-p', '2', '-overlay', str(overlay), './internal/egg',
         '-run', '^TestSessionAnswerThreeSecondDiagnostic$', '-count=1', '-v', '-timeout=30s'],
        folder / 'transport-probe.log', cwd=source, env=env)
    run(['node', '--test', str(folder / 'subject/web/src/conversation-recovery.test.js'),
         str(folder / 'subject/web/test/conversation-response.test.js')], folder / 'prior-reader-tests.log')
    run(['go', 'test', '-p', '2', './internal/egg', '-run',
         '^TestSessionPrompt(WaitingAnswerProductionReader|ConcurrentRetrySendsAtMostOnceAndKeepsLease|CrashGapIsReservedAndNeverResent|ZeroBytesErrorDoesNotProveNotSent)$',
         '-count=1', '-v', '-timeout=30s'], folder / 'prior-transport-tests.log', cwd=source, env=env)
    run(['node', 'test/web/session-answer-diagnostics.mjs', str(folder / 'subject'),
         str(folder / 'transport-observations.json'), str(folder / 'diagnostic-results.json')],
        folder / 'diagnostic-run.log')


def decision(folder, mode):
    for file, digest in INPUTS.items():
        assert sha(folder / file) == digest, file
    # Publish the decision with the evidence so an isolated consumer gets it.
    published = folder / 'evidence/REMEDIATION.json'
    canonical = folder / 'REMEDIATION.json'
    if mode == 'report' or canonical.exists():
        assert canonical.read_bytes() == published.read_bytes(), 'decision copies differ'
    report = json.loads(published.read_text())
    results = json.loads((folder / 'evidence/diagnostic-results.json').read_text())
    assert report['class'] == 'builder-defect' and report['repeated_findings'] is True
    assert results['subject'] == SUBJECT['head']
    assert len(results['results']) >= 10
    deadlocks = [r['deadlock'] for r in results['results'] if 'deadlock' in r]
    assert len(deadlocks) == 2 and all(r['elapsed'] == 30000 and r['pending'] == 'unconfirmed' and not r['composerEditable'] for r in deadlocks)
    timeout = next(r for r in results['results'] if 'expired' in r)
    assert timeout['at15s']['pending'] == 'unconfirmed'
    assert timeout['expired']['elapsed'] == 15000 + timeout['responseDelayMs']
    assert (folder / 'evidence/investigation.md').stat().st_size > 2000
    if mode == 'classification':
        print('builder-defect: repeated waiting-answer evidence/composer coupling, delayed enqueue clock, and stale restoration; independently reproduced on exact final source.')
        return
    assert report['verdict'] == 'retry-with-plan' and report['needed'] == []
    acceptance = report['acceptance']
    assert len(acceptance) >= 12
    assert all(isinstance(a, dict) and {'id', 'requirement', 'command', 'fixtures'} <= a.keys() for a in acceptance)
    assert len({a['id'] for a in acceptance}) == len(acceptance)
    assert all(a['command'] and a['fixtures'] for a in acceptance)
    if mode == 'plan':
        print('Published decision verified: evidence/REMEDIATION.json')
        print('Complete retry acceptance:', ', '.join(a['id'] for a in acceptance))
        print('Offline inputs and bounded commands are available; receipt-only recovery and independent composer state are specified.')
        return
    final = (folder / 'final.md').read_text().splitlines()
    assert final[0] == 'Remediation: retry-with-plan' and len(final) <= 12
    head = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=REPO, text=True).strip()
    assert head in '\n'.join(final)
    assert all(any(identifier in line for line in final) for identifier in ['A1', 'A2', 'A3', 'A4', 'A5', 'A6', 'G1'])
    identity = subprocess.check_output(['git', 'log', '-1', '--format=%an|%ae|%cn|%ce'], cwd=REPO, text=True).strip()
    assert identity == 'Bryan Ehrlich|ehrlich.bryan@gmail.com|Bryan Ehrlich|ehrlich.bryan@gmail.com'
    keep = (folder / 'KEEP').read_text().splitlines()
    assert {'REMEDIATION.json', 'readiness-plan.json', 'evidence/'} <= set(keep)
    print('Decision schema, concise final report, committed identity and retention declarations verified for', head)


if __name__ == '__main__':
    operation = sys.argv[1]
    if operation == 'host':
        host()
    elif operation == 'history':
        history(Path(sys.argv[2]).resolve())
    elif operation == 'diagnostics':
        diagnostics(Path(sys.argv[2]).resolve())
    else:
        decision(Path(sys.argv[2]).resolve(), operation)
