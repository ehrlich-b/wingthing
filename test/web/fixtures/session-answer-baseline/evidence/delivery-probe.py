#!/usr/bin/env python3
"""Exercise the decision audit with only files shipped in an isolated packet."""
import json
from pathlib import Path
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parent.parent
REPO = ROOT / 'repo'
INPUTS = json.loads((REPO / 'test/web/fixtures/session-answer-audit-inputs.json').read_text())
workspace = Path(tempfile.mkdtemp(prefix='delivery-packet-', dir=ROOT / 'scratch'))
print('Scratch packets:', workspace, flush=True)


def packet(name, published=True, canonical=None):
    root = workspace / name
    for name in ['test/web/session-answer-audit.py',
                 'test/web/fixtures/session-answer-audit-inputs.json',
                 'test/web/fixtures/session-answer-subject.json']:
        target = root / 'repo' / name
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(REPO / name, target)
    for name in [*INPUTS, 'evidence/diagnostic-results.json']:
        if name == 'evidence/REMEDIATION.json' and not published:
            continue
        target = root / name
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(ROOT / name, target)
    if canonical is not None:
        (root / 'REMEDIATION.json').write_bytes(canonical)
    return root


def probe(root, operation, expected, diagnostic):
    command = ['nice', '-n', '10', 'python3', 'test/web/session-answer-audit.py', operation, '..']
    result = subprocess.run(command, cwd=root / 'repo', text=True,
                            stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    print(json.dumps({'packet': root.name, 'command': command,
                      'exit_code': result.returncode}), flush=True)
    print(result.stdout, end='', flush=True)
    assert result.returncode == expected, result.stdout
    assert diagnostic in result.stdout, result.stdout


canonical = (ROOT / 'evidence' / 'REMEDIATION.json').read_bytes()
shipped = packet('published-only')
probe(shipped, 'plan', 0, 'Complete retry acceptance: R1, R2, R3, R4, R5, R6, R7, R8, R9, R10, R11, R12')
probe(shipped, 'classification', 0, 'builder-defect:')
probe(shipped, 'report', 1, 'FileNotFoundError')
probe(packet('both-identical', canonical=canonical), 'plan', 0, 'Published decision verified:')
probe(packet('published-missing', published=False, canonical=canonical), 'plan', 1, 'FileNotFoundError')
probe(packet('copies-disagree', canonical=b'{}\n'), 'plan', 1, 'decision copies differ')
corrupt = packet('published-corrupt')
(corrupt / 'evidence/REMEDIATION.json').write_text('{}\n')
probe(corrupt, 'plan', 1, 'AssertionError: evidence/REMEDIATION.json')
print('PASS: isolated decision delivery; missing, corrupt and divergent copies rejected.', flush=True)
