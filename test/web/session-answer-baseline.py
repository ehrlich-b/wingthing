#!/usr/bin/env python3
"""Replay the pinned session proof using explicit source, packet and output paths.

Example: session-answer-baseline.py --source /src/project --baseline /inputs/packet
         --work-dir /tmp/empty-replay --output /reports/replay
The workspace and reports must be empty and outside both input directories.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import sys

SUPPORT = ['session-answer-audit.py', 'session-answer-diagnostics.mjs',
           'fixtures/session-answer-subject.json', 'fixtures/session-answer-audit-inputs.json',
           'fixtures/session-answer-baseline-inputs.json',
           'fixtures/session-answer-historical-inputs.json', 'fixtures/session-answer-transport.go.txt']
REGRESSIONS = ['web/src/conversation-recovery.js', 'web/src/conversation-recovery.test.js',
               'internal/egg/session_prompt.go', 'internal/egg/session_answer_bridge_test.go']


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def inside(root, name):
    path = Path(name)
    if path.is_absolute() or not path.parts or any(p in {'.', '..'} for p in path.parts):
        raise ValueError('escaping input path: ' + name)
    target = root / path
    if not target.resolve().is_relative_to(root.resolve()):
        raise ValueError('escaping input path: ' + name)
    return target


def disjoint(paths):
    for i, path in enumerate(paths):
        for other in paths[i + 1:]:
            if path == other or path.is_relative_to(other) or other.is_relative_to(path):
                raise ValueError('source, baseline, workspace and output must be disjoint')


def packet_inputs(source, packet_format):
    if packet_format not in {'public', 'historical'}:
        raise ValueError('unknown packet format: ' + packet_format)
    fixtures = source / 'test/web/fixtures'
    audit = json.loads((fixtures / 'session-answer-audit-inputs.json').read_text())
    name = 'session-answer-' + ('baseline' if packet_format == 'public' else 'historical') + '-inputs.json'
    manifest = json.loads((fixtures / name).read_text())
    if packet_format == 'historical':
        audit = {name: manifest[name] for name in audit}
    return manifest, audit


def validate(source, baseline, work, output, packet_format='public'):
    """Validate every declared input before creating any workspace or report."""
    disjoint([source, baseline, work, output])
    for target in [work, output]:
        if target.exists() and (not target.is_dir() or any(target.iterdir())):
            raise ValueError('workspace and output must be empty: ' + str(target))
    for name in SUPPORT:
        if not inside(source, 'test/web/' + name).is_file():
            raise ValueError('missing source input: ' + name)
    manifest, audit = packet_inputs(source, packet_format)
    if set(manifest) != {*audit, 'evidence/delivery-probe.py', 'evidence/diagnostic-results.json'}:
        raise ValueError('baseline manifest must declare exactly the audit and replay inputs')
    for name, digest in manifest.items():
        path = inside(baseline, name)
        if not path.is_file():
            raise ValueError('missing baseline input: ' + name)
        if sha(path) != digest or (name in audit and digest != audit[name]):
            raise ValueError('wrong baseline hash: ' + name)
    subject = json.loads((source / 'test/web/fixtures/session-answer-subject.json').read_text())
    for name, digest in subject['files'].items():
        inside(source, name)
        content = subprocess.check_output(['git', 'show', subject['head'] + ':' + name], cwd=source)
        if hashlib.sha256(content).hexdigest() != digest:
            raise ValueError('wrong subject hash: ' + name)
    for name in REGRESSIONS:
        if not inside(source, name).is_file():
            raise ValueError('missing regression input: ' + name)
    return manifest, subject


def replay(source, baseline, work, output, packet_format='public'):
    manifest, subject = validate(source, baseline, work, output, packet_format)
    if os.geteuid() == 0 or socket.gethostname() == 'forge':
        raise ValueError('replay requires a non-root user on a suitable host other than forge')
    # The unchanged audit consumes an isolated packet with this internal layout.
    # No external path is discovered from the script location or its parents.
    (work / 'repo/test/web/fixtures').mkdir(parents=True, exist_ok=True)
    (work / 'scratch/tmp').mkdir(parents=True, exist_ok=True)
    output.mkdir(parents=True, exist_ok=True)
    for name in manifest:
        target = inside(work, name)
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(inside(baseline, name), target)
    for name in SUPPORT:
        shutil.copyfile(source / 'test/web' / name, work / 'repo/test/web' / name)
    # Bind the isolated audit to the explicitly selected bytes, never relabel
    # historical hashes as hashes of the sanitized public packet.
    _, audit = packet_inputs(source, packet_format)
    for name, inputs in [('baseline', manifest), ('audit', audit)]:
        (work / 'repo/test/web/fixtures' / ('session-answer-' + name + '-inputs.json')).write_text(
            json.dumps(inputs, indent=2) + '\n')
    commands = []

    def run(command, name, cwd=work / 'repo', expected=0, env=None):
        actual = ['nice', '-n', '10', *command]
        log = output / name
        with log.open('w') as stream:
            result = subprocess.run(actual, cwd=cwd, env=env, stdout=stream, stderr=subprocess.STDOUT)
        commands.append({'command': actual, 'exit_code': result.returncode,
                         'log': name, 'sha256': sha(log)})
        print(name, result.returncode, flush=True)
        if result.returncode != expected:
            raise RuntimeError(log.read_text()[-4000:])
        return log.read_text()

    run(['git', 'init'], 'replay-init.log')
    run(['git', 'fetch', str(source), subject['head']], 'replay-fetch.log')
    # Preserve the published decision audit and all delivery-probe assertions.
    run([sys.executable, '-B', 'test/web/session-answer-audit.py', 'plan', str(work)], 'a5-plan.log')
    run([sys.executable, '-B', str(work / 'evidence/delivery-probe.py')], 'a5-delivery.log')
    run([sys.executable, '-B', 'test/web/session-answer-audit.py', 'diagnostics',
         str(work / 'evidence')], 'diagnostics-rerun.log')
    results = json.loads((work / 'evidence/diagnostic-results.json').read_text())
    assert len(results['results']) == 10
    assert results['subject'] == subject['head']
    shutil.copyfile(work / 'evidence/diagnostic-results.json', output / 'rerun-results.json')
    shutil.copyfile(work / 'evidence/transport-observations.json', output / 'rerun-transport.json')
    env = dict(os.environ, SESSION_READER_SOURCE=(work / 'evidence/subject/web/src/conversation-recovery.js').as_uri())
    text = run(['node', '--test', '--test-name-pattern=waiting-answer failure input:',
                'web/src/conversation-recovery.test.js'], 'product-regressions.log',
               cwd=source, expected=1, env=env)
    assert 'tests 20' in text and 'fail 20' in text, text[-4000:]
    report = {'commands': commands, 'subject': subject['head'], 'packet_format': packet_format, 'diagnostic_scenarios': 10,
              'regressions_on_subject': {'tested': 20, 'failed': 20, 'existing_safeguards': 0},
              'input_hashes': {'baseline': manifest,
                              'source': {name: sha(source / name) for name in [
                                  *('test/web/' + name for name in SUPPORT), *REGRESSIONS]},
                              'subject': subject['files']},
              'verifier_refinement': 'delivery-probe.py reads the explicit evidence/REMEDIATION.json published decision'}
    (output / 'reproduction.json').write_text(json.dumps(report, indent=2) + '\n')
    print('Published A5 and all ten diagnostic scenarios replayed; 20 waiting-answer regressions fail the pinned subject.')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for flag in ['source', 'baseline', 'work-dir', 'output']:
        parser.add_argument('--' + flag, type=Path, required=True)
    parser.add_argument('--packet-format', choices=['public', 'historical'], default='public')
    args = parser.parse_args()
    replay(*(path.resolve() for path in [args.source, args.baseline, args.work_dir, args.output]),
           packet_format=args.packet_format)


if __name__ == '__main__':
    main()
