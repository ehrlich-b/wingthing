#!/usr/bin/env python3
"""Verify a portable, offline source handoff; this is not a release approval.

The receipt lives outside the checkout. Paths are relative to that receipt and
must stay within its directory. Its SHA-256 must be exchanged independently;
this verifies integrity and coverage, not the authenticity of an unsigned file.
"""
import argparse
import copy
import functools
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile


class Invalid(ValueError):
    pass


def require(condition, message):
    if not condition:
        raise Invalid(message)


def git(repo, *args):
    result = subprocess.run(['git', '-C', str(repo), *args], capture_output=True)
    require(result.returncode == 0, result.stderr.decode(errors='replace').strip()
            or 'git ' + ' '.join(args) + ' failed (exit ' + str(result.returncode) + ')')
    return result.stdout


def digest(data):
    return hashlib.sha256(data).hexdigest()


@functools.lru_cache(maxsize=8)
def source_hashes(repo, base):
    entries = git(repo, 'ls-tree', '-rz', base).split(b'\0')
    objects = []
    for entry in entries:
        if entry:
            metadata, name = entry.split(b'\t', 1)
            mode, kind, oid = metadata.split()
            require(kind == b'blob', 'unsupported source entry: ' + name.decode())
            objects.append((oid, name.decode()))
    process = subprocess.run(['git', '-C', str(repo), 'cat-file', '--batch'],
                             input=b''.join(oid + b'\n' for oid, _ in objects), capture_output=True)
    require(process.returncode == 0, 'cannot read source objects')
    data, offset, result = process.stdout, 0, {}
    for oid, name in objects:
        end = data.index(b'\n', offset)
        header = data[offset:end].split()
        require(header[:2] == [oid, b'blob'], 'unexpected source object')
        size = int(header[2])
        offset = end + 1
        result[name] = digest(data[offset:offset + size])
        offset += size + 1
    return result


def full_commit(value):
    return isinstance(value, str) and re.fullmatch(r'[0-9a-f]{40}', value)


def relative(root, name):
    require(isinstance(name, str) and name and not Path(name).is_absolute(), 'invalid relative path')
    require('..' not in Path(name).parts, 'path traversal')
    path = root / name
    require(path.resolve().is_relative_to(root.resolve()), 'path escapes receipt')
    return path


REQUIRED = {
    'own-downloads-audit', 'own-downloads-migration', 'download-core-design',
    'charge-readiness', 'release-review', 'security-review', 'integration-review',
}
BUILD_INPUTS = {'go.mod', 'go.sum', 'Makefile', 'web/package.json', 'web/package-lock.json',
                'Dockerfile', 'mobile/android/toolchain.sh', 'mobile/android/build-bridge.sh'}
# Only this verifier and its tests are outside the original source assessment.
# Any application, build, configuration or documentation edit needs a new review.
TOOLS = {'scripts/pm/source_receipt.py', 'scripts/pm/test_source_receipt.py'}


def check_coverage(repo, base):
    result = subprocess.run(['git', '-C', str(repo), 'merge-base', '--is-ancestor', base, 'HEAD'],
                            capture_output=True)
    require(result.returncode == 0, 'required base is not an ancestor of HEAD')
    # Renames/copies must retain the source deletion or modification. Do not let
    # repository diff settings or external helpers hide an application change.
    changed = set(git(repo, 'diff', '--no-renames', '--no-ext-diff', '--no-textconv',
                      '--name-only', '-z', base, 'HEAD', '--').decode().split('\0')) - {''}
    require(changed <= TOOLS, 'application changes lack current review coverage: ' + ', '.join(sorted(changed - TOOLS)))
    return changed


def review_headers(text, key):
    lines = text.splitlines()
    verdict = re.fullmatch(r'Verdict: (PASS|FAIL)', lines[0]) if lines else None
    reviewed = re.fullmatch(r'Reviewed: ([0-9a-f]{40})', lines[1]) if len(lines) > 1 else None
    require(reviewed is not None and verdict is not None, 'unrecognized original review: ' + key)
    return reviewed.group(1), verdict.group(1)


def verify(receipt, directory, repo, required_base):
    require(receipt.get('version') == 1, 'unsupported receipt version')
    base = receipt.get('base')
    require(full_commit(base), 'base must be a full commit')
    require(git(repo, 'rev-parse', required_base + '^{commit}').decode().strip() == base,
            'required base mismatch')
    require(git(repo, 'status', '--porcelain', '--untracked-files=all') == b'', 'checkout is dirty')
    changed = check_coverage(repo, base)

    bundle = relative(directory, receipt['bundle']['path'])
    require(bundle.is_file() and bundle.stat().st_mode & 0o222 == 0, 'bundle is missing or writable')
    require(digest(bundle.read_bytes()) == receipt['bundle']['sha256'], 'bundle hash mismatch')
    git(repo, 'bundle', 'verify', str(bundle.resolve()))
    heads = git(repo, 'bundle', 'list-heads', str(bundle.resolve())).decode().splitlines()
    require(any(line.split()[0] == base for line in heads), 'bundle does not advertise exact base')

    # Complete inventory: deleting an entry cannot hide a missing build input.
    actual = source_hashes(str(repo), base)
    names = sorted(actual)
    inventory = receipt['source']
    require(set(inventory) == set(names), 'source inventory is incomplete')
    require(BUILD_INPUTS <= set(names), 'required build inputs absent')
    for name in names:
        require(actual[name] == inventory[name], 'source hash mismatch: ' + name)
    optional = [n for n in names if 'storyboard' in n.lower() or n.lower() in ('state', 'state.md')]
    require(receipt.get('state_and_storyboards') == optional, 'state/storyboard inventory mismatch')

    artifacts = receipt['artifacts']
    require(REQUIRED <= set(artifacts), 'required source evidence missing')
    texts = {}
    for key, item in artifacts.items():
        path = relative(directory, item['path'])
        require(path.is_file(), 'missing evidence: ' + key)
        data = path.read_bytes()
        require(digest(data) == item['sha256'], 'evidence hash mismatch: ' + key)
        require(item.get('origin'), 'original evidence origin missing: ' + key)
        texts[key] = data.decode(errors='replace')

    # Dispositions are read from original reports, never inferred from a queue.
    require(set(receipt['reviews']) == {'integration-review', 'release-review', 'security-review'},
            'review coverage missing')
    dispositions = {}
    for key, review in receipt['reviews'].items():
        text = texts[key]
        reviewed, verdict = review_headers(text, key)
        require(reviewed == review['commit'], 'stale review: ' + key)
        require(verdict == review['verdict'], 'review disposition mismatch: ' + key)
        if key == 'integration-review':
            require(review['commit'] == base and verdict == 'PASS', 'base lacks a passing original assessment')
        else:
            require(review.get('scope') == 'historical-open', 'historical review cannot approve current release')
            require(verdict == 'FAIL', 'open review unexpectedly changed; reassess receipt')
        dispositions[key] = {'commit': review['commit'], 'verdict': verdict}
    # Known absent suppliers must stay explicit and must not count as approval.
    missing = receipt['missing']
    require(isinstance(missing, dict) and {'download-product-review', 'root-state',
            'native-provider-qualification', 'operator-release-qualification'} <= set(missing),
            'missing supplier inventory incomplete')
    require(all(isinstance(reason, str) and reason.strip() for reason in missing.values()), 'missing evidence needs reasons')
    require(receipt.get('release_approved') is False, 'receipt cannot retire open release/security findings')
    return {'base': base, 'candidate': git(repo, 'rev-parse', 'HEAD').decode().strip(),
            'source_files': len(names), 'artifacts': len(artifacts), 'reviews': dispositions,
            'unreviewed_tools': sorted(changed), 'missing': missing, 'release_approved': False}


def negatives(receipt, directory, repo, base):
    """Mutate real inputs and exercise actual verification, including rehashed stale reviews."""
    cases = []
    def reject(name, change):
        value = copy.deepcopy(receipt)
        change(value)
        try:
            verify(value, directory, repo, base)
        except (Invalid, KeyError, OSError) as error:
            cases.append({'case': name, 'rejected': True, 'reason': str(error)})
            return
        raise Invalid('negative fixture accepted: ' + name)
    reject('wrong-base', lambda r: r.update(base='0' * 40))
    reject('tampered-bundle', lambda r: r['bundle'].update(sha256='0' * 64))
    reject('missing-source-input', lambda r: r['source'].pop('go.sum'))
    reject('mismatched-source-input', lambda r: r['source'].update({'go.mod': '0' * 64}))
    reject('missing-original-review', lambda r: r['artifacts'].pop('security-review'))
    reject('missing-evidence-file', lambda r: r['artifacts']['charge-readiness'].update(path='absent-original.md'))
    reject('mismatched-original-review', lambda r: r['artifacts']['integration-review'].update(sha256='0' * 64))
    reject('stale-review', lambda r: r['reviews']['integration-review'].update(commit='0' * 40))
    reject('queue-promoted-to-pass', lambda r: r['reviews']['security-review'].update(verdict='PASS'))
    reject('missing-coverage', lambda r: r['reviews'].pop('security-review'))
    reject('missing-storyboards', lambda r: r.update(state_and_storyboards=[]))
    reject('path-escape', lambda r: r['artifacts']['charge-readiness'].update(path='../outside.md'))
    reject('open-findings-retired', lambda r: r.update(release_approved=True))
    # Fresh repository fixtures test git state rather than a manifest assertion.
    with tempfile.TemporaryDirectory(prefix='source-receipt-', dir=os.environ.get('TMPDIR')) as tmp:
        clone = Path(tmp) / 'repo'
        result = subprocess.run(['git', 'clone', '--quiet', '--no-hardlinks', str(repo), str(clone)], capture_output=True)
        require(result.returncode == 0, 'negative clone failed')
        candidate = git(clone, 'rev-parse', 'HEAD').decode().strip()
        (clone / 'go.mod').write_text('dirty fixture\n')
        try:
            verify(receipt, directory, clone, base)
        except Invalid as error:
            require('dirty' in str(error), 'dirty fixture failed for unrelated reason')
            cases.append({'case': 'dirty-checkout', 'rejected': True, 'reason': str(error)})
        else:
            raise Invalid('dirty checkout accepted')
        git(clone, '-c', 'user.name=Bryan Ehrlich', '-c', 'user.email=ehrlich.bryan@gmail.com',
            'commit', '-am', 'Exercise an unsupported source change.')
        try:
            verify(receipt, directory, clone, base)
        except Invalid as error:
            require('coverage' in str(error), 'changed source failed for unrelated reason')
            cases.append({'case': 'unreviewed-application-change', 'rejected': True, 'reason': str(error)})
        else:
            raise Invalid('unreviewed application change accepted')
        git(clone, 'reset', '--hard', candidate)
        git(clone, 'mv', '-f', 'internal/relay/access.go', 'scripts/pm/test_source_receipt.py')
        git(clone, '-c', 'user.name=Bryan Ehrlich', '-c', 'user.email=ehrlich.bryan@gmail.com',
            'commit', '-am', 'Exercise a source rename onto a verification tool.')
        try:
            verify(receipt, directory, clone, base)
        except Invalid as error:
            require('coverage' in str(error) and 'internal/relay/access.go' in str(error),
                    'rename fixture failed for unrelated reason')
            cases.append({'case': 'application-renamed-to-tool', 'rejected': True, 'reason': str(error)})
        else:
            raise Invalid('application renamed to tool accepted')
        inputs = Path(tmp) / 'inputs'
        inputs.mkdir()
        # Hard links preserve the real immutable bytes without a synthetic report.
        for item in [receipt['bundle'], *receipt['artifacts'].values()]:
            target = inputs / item['path']
            target.parent.mkdir(parents=True, exist_ok=True)
            if not target.exists():
                os.link(relative(directory, item['path']), target)
        value = copy.deepcopy(receipt)
        stale = '1' * 40
        original = value['artifacts']['integration-review']
        text = relative(directory, original['path']).read_text()
        text = text.replace(value['base'], stale)
        (inputs / 'stale-assessment.md').write_text(text)
        original.update(path='stale-assessment.md', sha256=digest(text.encode()))
        value['reviews']['integration-review']['commit'] = stale
        try:
            verify(value, inputs, repo, base)
        except Invalid as error:
            require('base lacks' in str(error), 'rehashed stale report failed for unrelated reason')
            cases.append({'case': 'rehashed-stale-original', 'rejected': True, 'reason': str(error)})
        else:
            raise Invalid('rehashed stale original accepted')
    return cases


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--verify', type=Path, required=True)
    parser.add_argument('--require-base', required=True)
    parser.add_argument('--negative-fixtures', action='store_true')
    args = parser.parse_args()
    try:
        path = args.verify.resolve()
        receipt = json.loads(path.read_text())
        repo = Path(git(Path.cwd(), 'rev-parse', '--show-toplevel').decode().strip())
        report = verify(receipt, path.parent, repo, args.require_base)
        if args.negative_fixtures:
            report['negative_fixtures'] = negatives(receipt, path.parent, repo, args.require_base)
        print(json.dumps(report, indent=2))
    except (Invalid, KeyError, TypeError, ValueError, OSError) as error:
        print('source receipt FAIL: ' + str(error), file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
