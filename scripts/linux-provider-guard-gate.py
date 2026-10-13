#!/usr/bin/env python3
"""Native Linux provider protection; any skip or missing test fails acceptance."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile

from importlib.util import spec_from_file_location, module_from_spec


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--go', required=True)
    args = parser.parse_args()
    repo = Path(__file__).resolve().parent.parent
    (repo / '.scratch').mkdir(exist_ok=True)
    root = Path(tempfile.mkdtemp(prefix='linux-provider-guard-', dir=repo / '.scratch'))
    log = root / 'native.jsonl'
    env = dict(os.environ, GOMAXPROCS='2')
    required = ('TestLinuxProviderGuardNative/bind', 'TestLinuxProviderGuardNative/overlay',
                'TestLinuxProviderGuardLandlockNative', 'TestLinuxProviderGuardScopedNative',
                'TestLinuxProviderGuardRejectedTargetsNative/missing',
                'TestLinuxProviderGuardRejectedTargetsNative/fifo',
                'TestLinuxProviderGuardRejectedTargetsNative/controller-hardlink',
                'TestLinuxProviderGuardRejectedTargetsNative/state-hardlink',
                'TestLinuxProviderGuardRejectedTargetsNative/external-symlink',
                'TestLinuxProviderGuardRejectedTargetsNative/dangling-symlink',
                'TestLinuxProviderGuardRejectedTargetsNative/socket-hardlink',
                'TestLinuxProviderGuardBindAliasesNative/state',
                'TestLinuxProviderGuardBindAliasesNative/controller')
    required += tuple(f'TestLinuxProviderGuardFilesystemsNative/{kind}/{stage}'
                      for kind in ('overlay-upper', 'overlay-lower', 'overlay-protected',
                                   'unknown-work', 'unknown-state', 'unknown-home',
                                   'unknown-temp', 'bind-work')
                      for stage in ('admission', 'wrapper'))
    source = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=repo, text=True).strip()
    diff = subprocess.check_output(['git', 'diff', 'HEAD'], cwd=repo)
    with log.open('xb') as output:
        proc = subprocess.run(['nice', '-n', '10', args.go, 'test', '-p', '2', '-tags',
                               'integration', '-json', '-count=1', '-timeout=2m',
                               './internal/sandbox', '-run', '^TestLinuxProviderGuard'],
                              cwd=repo, env=env, stdout=output, stderr=subprocess.STDOUT,
                              stdin=subprocess.DEVNULL, timeout=150, check=False)
    spec = spec_from_file_location('gate', repo / 'scripts/check-parent-lifecycle.py')
    gate = module_from_spec(spec)
    spec.loader.exec_module(gate)
    tests, results, output, problems = gate.validate(log, ['./internal/sandbox'], required)
    if proc.returncode:
        problems.append(f'native command exited {proc.returncode}')
    if subprocess.check_output(['git', 'diff', 'HEAD'], cwd=repo) != diff:
        problems.append('source changed during gate')
    receipt = dict(status='FAIL' if problems else 'PASS', source_head=source,
                   diff_sha256=hashlib.sha256(diff).hexdigest(), log_sha256=gate.digest(log),
                   tests=sorted(t for (_, t), state in tests.items() if state == 'pass'),
                   packages=results, problems=problems, log=str(log))
    (root / 'receipt.json').write_text(json.dumps(receipt, indent=2) + '\n')
    if problems:
        print(''.join(output))
    print(json.dumps(receipt, indent=2))
    return int(bool(problems))


if __name__ == '__main__':
    raise SystemExit(main())
