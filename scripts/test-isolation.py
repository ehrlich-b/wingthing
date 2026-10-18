#!/usr/bin/env python3
"""Run finite native canaries and retain a source-bound, fail-closed report."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import shlex
import subprocess
import sys
import tempfile
import time

EXPECTED = {
    'TestIsolationWriteBoundary', 'TestIsolationSecretPaths',
    'TestIsolationCredentials', 'TestIsolationResourceFD',
    'TestIsolationResourceCPU', 'TestIsolationResourceMemory',
    'TestIsolationResourcePIDs', 'TestIsolationEgress',
    'TestIsolationEgress/network_none',
    'TestIsolationEgress/domain_filter_and_bypass',
}


def source_identity():
    head = subprocess.check_output(['git', 'rev-parse', 'HEAD'], text=True).strip()
    names = subprocess.check_output(
        ['git', 'ls-files', '-z', '--cached', '--others', '--exclude-standard'])
    digest = hashlib.sha256()
    for name in sorted(set(names.split(b'\0')) - {b''}):
        path = Path(os.fsdecode(name))
        digest.update(name + b'\0')
        if path.is_symlink():
            digest.update(os.fsencode(os.readlink(path)))
        elif path.is_file():
            digest.update(path.read_bytes())
        digest.update(b'\0')
    return {'head': head, 'tree_sha256': digest.hexdigest()}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, default=Path('.scratch/isolation'))
    parser.add_argument('--go', default='go')
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    run = Path(tempfile.mkdtemp(prefix='run-', dir=args.output)).resolve()
    before = source_identity()
    command = shlex.split(args.go) + [
        'test', '-p', '2', '-tags', 'isolation', './internal/egg',
        '-run', '^TestIsolation', '-count=1', '-json', '-timeout=3m']
    started = time.time()
    states, observations, package_pass = {}, [], False
    with (run / 'go-test.jsonl').open('w') as log:
        process = subprocess.Popen(command, stdout=subprocess.PIPE,
                                   stderr=subprocess.STDOUT, text=True)
        for line in process.stdout:
            log.write(line)
            print(line, end='', flush=True)
            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                continue  # Compiler diagnostics remain in the retained log.
            name, action = event.get('Test'), event.get('Action')
            if name and action in {'pass', 'fail', 'skip'}:
                states[name] = action
            if not name and action == 'pass':
                package_pass = True
            if 'BASELINE:' in event.get('Output', '') or 'UNRUN:' in event.get('Output', ''):
                observations.append(event['Output'].strip())
        exit_code = process.wait()
    after = source_identity()
    missing = sorted(EXPECTED - states.keys())
    incomplete = sorted(name for name, state in states.items() if state != 'pass')
    passed = (exit_code == 0 and package_pass and not missing and
              not incomplete and before == after and platform.system() == 'Linux')
    report = {
        'schema': 1, 'passed': passed, 'source': before,
        'source_unchanged': before == after, 'command': command,
        'platform': platform.platform(), 'machine': platform.machine(),
        'uid': os.getuid(), 'go_version': subprocess.check_output(
            shlex.split(args.go) + ['version'], text=True).strip(),
        'elapsed_seconds': round(time.time() - started, 3),
        'go_exit_code': exit_code, 'tests': states, 'missing': missing,
        'incomplete': incomplete, 'observations': observations,
        'scope': 'Wingthing native sandbox, synthetic fixtures, no installed provider or model',
    }
    (run / 'report.json').write_text(json.dumps(report, indent=2) + '\n')
    print(f"{'PASS' if passed else 'INCOMPLETE/FAIL'}: {run / 'report.json'}")
    return 0 if passed else 1


if __name__ == '__main__':
    sys.exit(main())
