#!/usr/bin/env python3
"""Outside-worker built-CLI acceptance. No model turn, login or credential copy."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys


def inventory(root):
    result = {}
    for path in sorted(root.rglob('*')):
        info = path.lstat()
        relative = str(path.relative_to(root))
        content = None
        if path.is_symlink():
            content = os.readlink(path)
        elif path.is_file():
            content = hashlib.sha256(path.read_bytes()).hexdigest()
        result[relative] = [info.st_mode, info.st_size, info.st_mtime_ns, content]
    return result


def run_capture(argv, env, root, label):
    output = root / (label + '.stdout')
    errors = root / (label + '.stderr')
    with output.open('xb') as out, errors.open('xb') as err:
        proc = subprocess.run(argv, env=env, stdin=subprocess.DEVNULL,
                              stdout=out, stderr=err, timeout=15, check=False)
    if output.stat().st_size > 64 * 1024 or errors.stat().st_size > 64 * 1024:
        raise RuntimeError('capture exceeds 64 KiB; inspect private files locally')
    if proc.returncode:
        raise RuntimeError(label + ' failed; inspect private stderr locally')
    return output.read_text()


def executable_digest(path):
    digest = hashlib.sha256()
    size = 0
    with path.open('rb') as stream:
        while block := stream.read(1024 * 1024):
            size += len(block)
            if size > 512 * 1024 * 1024:
                raise RuntimeError('provider executable exceeds 512 MiB')
            digest.update(block)
    return digest.hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--wt', type=Path, required=True)
    parser.add_argument('--root', type=Path, required=True)
    parser.add_argument('--provider', type=Path)
    args = parser.parse_args()
    repo = Path(__file__).resolve().parent.parent
    scratch = repo / '.scratch'
    if not args.root.is_absolute() or not args.wt.is_absolute():
        parser.error('wt and root must be absolute')
    root = args.root.resolve()
    binary = args.wt.resolve(strict=True)
    if not root.is_relative_to(scratch) or root == scratch:
        parser.error('root must be a new child of this clone .scratch')
    if not binary.is_relative_to(repo):
        parser.error('wt must be built inside this clone')
    # No re-use: original evidence and previous receipts stay immutable.
    root.mkdir(mode=0o700)
    inputs = root / 'inputs'
    captures = root / 'captures'
    inputs.mkdir(mode=0o700)
    captures.mkdir(mode=0o700)
    for name in ('home', 'state', 'tmp', 'work'):
        (inputs / name).mkdir(mode=0o700)
    env = {'PATH': '/usr/bin:/bin:/usr/sbin:/sbin',
           'HOME': str(inputs / 'home'), 'TMPDIR': str(inputs / 'tmp'),
           'WINGTHING_DIR': str(inputs / 'state'),
           'WINGTHING_PREVIEW_DIR': str(inputs / 'state')}
    # A plan that accidentally invokes its selected executable leaves evidence.
    sentinel = inputs / 'sentinel-provider'
    sentinel.write_text('#!/bin/sh\nprintf called > "' + str(inputs / 'unexpected-launch') + '"\nexit 77\n')
    sentinel.chmod(0o700)
    command = [str(binary), 'doctor', 'launch-plan', '--route', 'agent_start',
               '--agent', 'codex', '--unattended', '--cwd', str(inputs / 'work'),
               '--home', str(inputs / 'home'), '--allowed-home-root', str(inputs),
               '--executable', str(sentinel)]
    before = inventory(inputs)
    help_text = run_capture([str(binary), 'doctor', 'launch-plan', '--help'], env, captures, 'help')
    for flag in ('--route', '--facts', '--home', '--provider-evidence', '--json'):
        if flag not in help_text:
            raise RuntimeError('built command help is missing ' + flag)
    wire = run_capture(command + ['--json'], env, captures, 'plan-json')
    report = json.loads(wire)
    human = run_capture(command, env, captures, 'plan-human')
    if report['read_only'] is not True or report['readiness'] != 'unverified':
        raise RuntimeError('missing native evidence became readiness')
    if 'REQUIRED-UNRUN' not in human or report['provider_argv'][0] != str(sentinel):
        raise RuntimeError('built command report contract failed')
    if inventory(inputs) != before:
        raise RuntimeError('preflight mutated captured input/state inventory')
    receipt = {'built_cli': 'PASS', 'read_only_inventory': 'PASS',
               'native_confinement': 'REQUIRED-UNRUN',
               'provider_tool_call': 'REQUIRED-UNRUN',
               'provider_version_help': 'REQUIRED-UNRUN',
               'wt_sha256': executable_digest(binary), 'root': str(root)}
    if args.provider:
        if not args.provider.is_absolute():
            parser.error('provider must be an explicit absolute executable')
        provider = args.provider.resolve(strict=True)
        digest = executable_digest(provider)
        version = run_capture([str(provider), '--version'], env, captures, 'provider-version')
        help_output = run_capture([str(provider), '--help'], env, captures, 'provider-help')
        evidence = {'binary_sha256': digest, 'version_output': version,
                    'help_output': help_output, 'complete_help': True}
        evidence_path = inputs / 'provider-evidence.json'
        evidence_path.write_text(json.dumps(evidence, indent=2) + '\n')
        updated = command[:]
        updated[updated.index('--executable') + 1] = str(provider)
        updated += ['--provider-evidence', str(evidence_path), '--json']
        before = inventory(inputs)
        run_capture(updated, env, captures, 'provider-plan-json')
        if inventory(inputs) != before:
            raise RuntimeError('provider-evidence consumption mutated inputs/state')
        receipt['provider_version_help'] = 'PASS: saved version/help, no model call'
    (root / 'receipt.json').write_text(json.dumps(receipt, indent=2) + '\n')
    print(json.dumps(receipt, indent=2))


if __name__ == '__main__':
    main()
