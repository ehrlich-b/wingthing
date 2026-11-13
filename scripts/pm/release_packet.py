#!/usr/bin/env python3
"""Stage an immutable, source-bound operator packet; never contact a publisher."""
import argparse
import copy
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile

PLATFORMS = ('linux-amd64', 'linux-arm64', 'darwin-amd64', 'darwin-arm64')
REQUIRED_TESTS = ('make-check', 'android', 'browser-join', 'race', 'source-tests',
                  'hygiene', 'prepublish')
QUALIFICATIONS = ('native-mac', 'real-provider', 'hosted', 'owned-origin',
                  'installer-recovery', 'durable-journey', 'quickstart')
FEATURES = ['encrypted terminal output', 'loopback local-only listener',
            'legacy sandbox capability reports',
            'opt-in Claude native policy', 'opt-in Codex native policy',
            'visible session search counts']
BUILD_LIMITS = {'cgo_enabled': False}


def git(*args):
    return subprocess.check_output(['git', *args], text=True).strip()


def load(path):
    def unique(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise ValueError('duplicate JSON key: ' + key)
            result[key] = value
        return result
    return json.loads(Path(path).read_text(), object_pairs_hook=unique)


def digest(path):
    path = Path(path)
    if path.is_symlink() or not path.is_file() or not path.stat().st_size:
        raise ValueError('missing nonempty regular file: ' + str(path))
    return hashlib.sha256(path.read_bytes()).hexdigest()


def write(path, data):
    with Path(path).open('x') as out:
        json.dump(data, out, indent=2)
        out.write('\n')


def assessment_identity(path):
    """Read scope from the original bytes, never just their envelope metadata."""
    text = Path(path).read_text()
    try:
        value = load(path)
    except ValueError:
        value = None
    if isinstance(value, dict):
        return value.get('commit'), value.get('disposition')
    commits = re.findall(r'(?im)^(?:commit|head):\s*([0-9a-f]{40})\s*$', text)
    dispositions = re.findall(r'(?im)^(?:disposition|verdict):\s*(PASS|FAIL|PENDING)\s*$', text)
    if len(commits) != 1 or len(dispositions) != 1:
        raise ValueError('original assessment needs one explicit full commit and disposition')
    return commits[0], dispositions[0].upper()


def relative(root, name):
    path = root / name
    if Path(name).is_absolute() or '..' in Path(name).parts:
        raise ValueError('bundle path escapes root')
    if any(p.is_symlink() for p in (path, *path.parents)):
        raise ValueError('symlink in bundle path')
    return path


def pairing_status(packet, root, platform, binary_identity=True):
    """Derive one artifact's capability from its retained receipt and actual bytes.

    Receipt integrity is not signer authentication or release authorization.
    Synthetic evidence exercises this contract but never qualifies a package.
    """
    entry = packet['phone_pairing'][platform]
    retained = entry['receipt']
    if retained is None:
        return 'unqualified'
    path = relative(root, retained['path'])
    if digest(path) != retained['sha256']:
        raise ValueError('pairing receipt checksum mismatch: ' + platform)
    receipt = load(path)
    if type(receipt['schema']) is not int or receipt['schema'] != 1:
        raise ValueError('invalid pairing receipt schema')
    if receipt['commit'] != packet['commit']:
        raise ValueError('pairing receipt is for another commit')
    if receipt['platform'] != platform:
        raise ValueError('pairing receipt is for another platform')
    artifact = packet['artifacts'][platform]
    if receipt['binary_sha256'] != artifact['sha256'] or digest(
            relative(root, artifact['path'])) != receipt['binary_sha256']:
        raise ValueError('pairing receipt binary checksum mismatch')
    if (receipt['outcomes'] != {'pair': 'PASS', 'attach': 'PASS'}
            or type(receipt['exit_code']) is not int or receipt['exit_code'] != 0):
        raise ValueError('pairing receipt needs successful pair and attach outcomes')
    command = receipt['command']
    if (not isinstance(command, list) or not command
            or any(not isinstance(arg, str) or not arg for arg in command)):
        raise ValueError('pairing receipt needs an execution command')
    log = receipt['log']
    if digest(relative(path.parent, log['path'])) != log['sha256']:
        raise ValueError('pairing receipt log checksum mismatch')
    if receipt['kind'] == 'synthetic-fixture':
        return 'fixture-passed'
    if receipt['kind'] != 'packaged-binary':
        raise ValueError('unknown pairing receipt kind')
    if not binary_identity:
        raise ValueError('package pairing requires binary identity verification')
    return 'qualified'


def include_pairing_receipts(packet, root, filenames):
    """Retain original receipt bytes and logs; never relabel source or binaries."""
    for filename in filenames:
        source = Path(filename).resolve()
        receipt = load(source)
        platform = receipt['platform']
        if platform not in PLATFORMS:
            raise ValueError('unknown pairing receipt platform')
        entry = packet['phone_pairing'][platform]
        if entry['receipt'] is not None:
            raise ValueError('duplicate pairing receipt: ' + platform)
        log = receipt['log']
        source_log = relative(source.parent, log['path'])
        if digest(source_log) != log['sha256']:
            raise ValueError('pairing receipt log checksum mismatch')
        folder = root / 'pairing' / platform
        folder.mkdir(parents=True)
        # Keep each supplier's relative log namespace inside its platform.
        log_target = relative(folder / 'logs', log['path'])
        log_target.parent.mkdir(parents=True)
        # Preserve the receipt unchanged, including its relative log path.
        target = folder / 'logs' / 'receipt.json'
        if target == log_target:
            raise ValueError('pairing log collides with receipt')
        shutil.copyfile(source_log, log_target)
        shutil.copyfile(source, target)
        entry['receipt'] = {'path': str(target.relative_to(root)), 'sha256': digest(target)}
        entry['status'] = pairing_status(packet, root, platform)


def clean_head():
    if git('status', '--porcelain'):
        raise ValueError('commit all source changes before recording or building')
    return git('rev-parse', 'HEAD')


def unused(version):
    if not re.fullmatch(r'v\d+\.\d+\.\d+', version):
        raise ValueError('version must be vMAJOR.MINOR.PATCH')
    if git('tag', '--list', version):
        raise ValueError('version already exists locally; never reuse release bytes')


def build_command(version, target):
    # Go omits linker flags from build metadata with -trimpath. Preserve them
    # so a foreign-version binary cannot be relabeled using envelope metadata,
    # even when the caller's GOFLAGS enables -trimpath.
    return ['go', 'build', '-p', '2', '-trimpath=false', '-buildvcs=true',
            '-ldflags=-s -w -X main.version=' + version, '-o', str(target), './cmd/wt']


def record(args):
    head = clean_head()
    root = Path(args.output).resolve()
    root.mkdir(parents=True, exist_ok=True)
    log = root / (args.record_test + '.txt')
    command = args.command
    if command[:1] == ['--']:
        command = command[1:]
    if not command:
        raise ValueError('a test command is required')
    with log.open('x') as out:
        out.write('command: ' + json.dumps(command) + '\n')
        out.flush()
        result = subprocess.run(command, stdout=out, stderr=subprocess.STDOUT)
    if clean_head() != head:
        raise ValueError('source changed while recording')
    receipt = {'id': args.record_test, 'commit': head, 'command': command,
               'exit_code': result.returncode, 'log': str(log), 'sha256': digest(log)}
    write(root / (args.record_test + '.json'), receipt)
    print(json.dumps(receipt))
    return result.returncode


def stage(args):
    head = clean_head()
    unused(args.version)
    root = Path(args.output).resolve()
    checkout = Path(git('rev-parse', '--show-toplevel')).resolve()
    if root == checkout or checkout in root.parents:
        raise ValueError('keep artifacts and execution evidence outside the checkout')
    root.mkdir()  # Refuse every existing destination, including partial attempts.
    (root / 'artifacts').mkdir()
    artifacts = {}
    build_logs = {}
    for platform in PLATFORMS:
        system, arch = platform.split('-')
        target = root / 'artifacts' / ('wt-' + platform)
        command = build_command(args.version, target)
        log = root / ('build-' + platform + '.txt')
        with log.open('x') as out:
            out.write(json.dumps(command) + '\n')
            out.flush()
            subprocess.run(command, env=dict(os.environ, CGO_ENABLED='0', GOOS=system,
                                            GOARCH=arch), stdout=out,
                           stderr=subprocess.STDOUT, check=True)
        build_logs[platform] = {'path': log.name, 'sha256': digest(log)}
        artifacts[platform] = {'path': 'artifacts/' + target.name, 'sha256': digest(target)}
    (root / 'SHA256SUMS').write_text(''.join(
        a['sha256'] + '  ' + a['path'] + '\n' for a in artifacts.values()))
    tests = []
    (root / 'tests').mkdir()
    for filename in args.receipt:
        receipt = load(filename)
        name = receipt['id']
        if name not in REQUIRED_TESTS:
            raise ValueError('unknown test receipt: ' + name)
        if receipt['commit'] != head or digest(receipt['log']) != receipt['sha256']:
            raise ValueError('test receipt source or log mismatch')
        target = root / 'tests' / (name + '.txt')
        shutil.copyfile(receipt['log'], target)
        receipt['log'] = str(target.relative_to(root))
        tests.append(receipt)
    assessment = None
    if args.assessment:
        assessment = load(args.assessment)
        shutil.copyfile(assessment['original'], root / 'assessment.txt')
        assessment['original'] = 'assessment.txt'
    shutil.copyfile('docs/release-packet.md', root / 'OPERATOR.md')
    shutil.copyfile('scripts/install.sh', root / 'install.sh')
    packet = {'schema': 1, 'commit': head, 'version': args.version,
              'version_scope': 'local reservation; operator must check authoritative registry',
              'artifacts': artifacts, 'build_logs': build_logs, 'tests': tests,
              'assessment': assessment, 'supported_features': FEATURES,
              'build_limits': BUILD_LIMITS,
              'phone_pairing': {platform: {'status': 'unqualified', 'receipt': None}
                                for platform in PLATFORMS},
              'qualifications': {key: {'status': 'pending', 'receipt': None}
                                 for key in QUALIFICATIONS},
              'operator': {'path': 'OPERATOR.md', 'sha256': digest(root / 'OPERATOR.md')},
              'installer': {'path': 'install.sh', 'sha256': digest(root / 'install.sh')},
              'publication': 'not authorized', 'decision': 'HOLD'}
    include_pairing_receipts(packet, root, args.pairing_receipt)
    verify(packet, root, head)
    if clean_head() != head:
        raise ValueError('source changed during build')
    write(root / 'candidate.json', packet)
    # Sealing is accidental-write protection, not a signature or trust bootstrap.
    for path in root.rglob('*'):
        if path.is_file():
            path.chmod(0o555 if path.parent.name == 'artifacts' else 0o444)
    print(str(root / 'candidate.json'))
    return 0


def verify(packet, root, expected_head=None, binary_identity=True):
    """Check integrity first; return unsatisfied promotion requirements separately."""
    if packet['schema'] != 1 or not re.fullmatch('[0-9a-f]{40}', packet['commit']):
        raise ValueError('invalid source identity')
    if expected_head is not None and packet['commit'] != expected_head:
        raise ValueError('packet source differs from current committed HEAD')
    unused(packet['version'])
    if packet['publication'] != 'not authorized' or packet['decision'] != 'HOLD':
        raise ValueError('this tool only prepares held operator packets')
    if packet['build_limits'] != BUILD_LIMITS:
        raise ValueError('unsupported build profile')
    if set(packet['artifacts']) != set(PLATFORMS) or set(packet['build_logs']) != set(PLATFORMS):
        raise ValueError('all four platform artifacts and build logs are required')
    for key in ('operator', 'installer'):
        entry = packet[key]
        if digest(relative(root, entry['path'])) != entry['sha256']:
            raise ValueError(key + ' checksum mismatch')
    sums = []
    for platform, artifact in packet['artifacts'].items():
        path = relative(root, artifact['path'])
        if digest(path) != artifact['sha256']:
            raise ValueError('artifact checksum mismatch: ' + platform)
        sums.append(artifact['sha256'] + '  ' + artifact['path'] + '\n')
        build = packet['build_logs'][platform]
        if digest(relative(root, build['path'])) != build['sha256']:
            raise ValueError('build log checksum mismatch')
        if binary_identity:
            info = subprocess.check_output(['go', 'version', '-m', str(path)], text=True)
            system, arch = platform.split('-')
            for setting in ('vcs.revision=' + packet['commit'], 'vcs.modified=false',
                            'GOOS=' + system, 'GOARCH=' + arch,
                            'CGO_ENABLED=0',
                            '-X main.version=' + packet['version']):
                if setting not in info:
                    raise ValueError('artifact build identity missing: ' + setting)
    if relative(root, 'SHA256SUMS').read_text() != ''.join(sums):
        raise ValueError('checksum manifest mismatch')
    if set(packet['phone_pairing']) != set(PLATFORMS):
        raise ValueError('all platform pairing dispositions must be visible')
    blockers = []
    for platform in PLATFORMS:
        entry = packet['phone_pairing'][platform]
        if set(entry) != {'status', 'receipt'}:
            raise ValueError('invalid pairing capability fields')
        status = pairing_status(packet, root, platform, binary_identity)
        if entry['status'] != status:
            raise ValueError('unsupported phone pairing capability claim: ' + platform)
        if status != 'qualified':
            blockers.append('phone pairing unqualified: ' + platform + ' (' + status + ')')
    ids = set()
    for receipt in packet['tests']:
        if receipt['id'] in ids or receipt['id'] not in REQUIRED_TESTS:
            raise ValueError('duplicate or unknown test receipt')
        ids.add(receipt['id'])
        if receipt['commit'] != packet['commit']:
            raise ValueError('test receipt is for another commit')
        if not receipt['command'] or digest(relative(root, receipt['log'])) != receipt['sha256']:
            raise ValueError('missing command or changed test log')
        if type(receipt['exit_code']) is not int or receipt['exit_code'] != 0:
            blockers.append('failed test: ' + receipt['id'])
    blockers += ['missing test: ' + name for name in REQUIRED_TESTS if name not in ids]
    assessment = packet['assessment']
    if assessment is None:
        blockers.append('missing original exact-commit assessment')
    else:
        if assessment['commit'] != packet['commit']:
            raise ValueError('assessment is for another commit')
        if digest(relative(root, assessment['original'])) != assessment['sha256']:
            raise ValueError('original assessment checksum mismatch')
        if assessment_identity(relative(root, assessment['original'])) != (
                packet['commit'], assessment['disposition']):
            raise ValueError('original assessment scope or disposition differs from its envelope')
        if assessment['disposition'] != 'PASS':
            blockers.append('assessment disposition: ' + assessment['disposition'])
    if not packet['supported_features'] or any(f not in FEATURES for f in packet['supported_features']):
        raise ValueError('unsupported feature claim')
    if set(packet['qualifications']) != set(QUALIFICATIONS):
        raise ValueError('all qualification dispositions must be visible')
    # External qualification is deliberately not accepted from arbitrary JSON.
    # Suppliers must extend validation with native, signed or operator evidence.
    for name, value in packet['qualifications'].items():
        if value != {'status': 'pending', 'receipt': None}:
            raise ValueError('external qualification requires independently validated supplier evidence')
        blockers.append('pending qualification: ' + name)
    blockers.append('authoritative version uniqueness and publication authorization pending')
    return blockers


def negative_fixtures(packet, root):
    changes = {
        'missing receipts': lambda p: p.update(tests=[]),
        'foreign assessment': lambda p: p.update(assessment={
            'commit': '0' * 40, 'original': 'OPERATOR.md',
            'sha256': p['operator']['sha256'], 'disposition': 'PASS'}),
        'tampered artifact': lambda p: p['artifacts']['linux-amd64'].update(sha256='0' * 64),
        'false qualification': lambda p: p['qualifications']['native-mac'].update(status='passed'),
    }
    for name, mutate in changes.items():
        altered = copy.deepcopy(packet)
        mutate(altered)
        try:
            blockers = verify(altered, root)
        except ValueError:
            pass
        else:
            if name != 'missing receipts' or not any(b.startswith('missing test:') for b in blockers):
                raise ValueError('negative fixture was accepted: ' + name)
        print('rejected negative fixture: ' + name)


def rehearse(packet, root):
    """Exercise local object staging and pointer rollback; never run an installer."""
    with tempfile.TemporaryDirectory(prefix='publish-rehearsal-', dir=root.parent) as tmp:
        origin = Path(tmp)
        old_selector = {'enabled': False, 'reason': 'no verified previous release supplied'}
        for name in ('primary', 'secondary'):
            store = origin / name
            store.mkdir()
            write(store / 'latest.json', old_selector)
            version = store / packet['version']
            shutil.copytree(root / 'artifacts', version)
            for artifact in packet['artifacts'].values():
                if digest(version / Path(artifact['path']).name) != artifact['sha256']:
                    raise ValueError('local staged object mismatch')
            try:
                shutil.copytree(root / 'artifacts', version)
            except FileExistsError:
                pass
            else:
                raise ValueError('immutable version overwrite accepted')
            # Production selector promotion requires a separately authorized tool.
            selector = store / 'latest.json'
            selector.write_text(json.dumps({'enabled': True, 'version': packet['version']}))
            if load(selector)['version'] != packet['version']:
                raise ValueError('local promotion failed')
            selector.write_text(json.dumps(old_selector))
            if load(selector) != old_selector:
                raise ValueError('local rollback failed')
            for artifact in packet['artifacts'].values():
                if digest(version / Path(artifact['path']).name) != artifact['sha256']:
                    raise ValueError('rollback modified retained bytes')
    print('PASS local primary/secondary staging, overwrite refusal, selection and rollback to disabled distribution')
    print('No network, daemon replacement, previous-version runtime or owned-origin qualification exercised')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--stage', action='store_true')
    parser.add_argument('--version')
    parser.add_argument('--output')
    parser.add_argument('--receipt', action='append', default=[])
    parser.add_argument('--pairing-receipt', action='append', default=[],
                        help='exact-source pair/attach receipt with platform, binary SHA and log')
    parser.add_argument('--assessment', help='JSON with commit, disposition, original path and sha256')
    parser.add_argument('--record-test', choices=REQUIRED_TESTS)
    parser.add_argument('--verify', type=Path)
    parser.add_argument('--inspect', action='store_true', help='report integrity and blockers; never approve')
    parser.add_argument('--negative-fixtures', action='store_true')
    parser.add_argument('--rehearse-publish-rollback', action='store_true')
    parser.add_argument('--local-only', action='store_true')
    parser.add_argument('command', nargs=argparse.REMAINDER)
    args = parser.parse_args()
    try:
        if args.record_test:
            if not args.output:
                raise ValueError('--record-test needs --output')
            return record(args)
        if args.stage:
            if not args.version or not args.output:
                raise ValueError('--stage needs --version and --output')
            return stage(args)
        if not args.verify:
            raise ValueError('--verify is required')
        packet = load(args.verify)
        root = args.verify.resolve().parent
        blockers = verify(packet, root, clean_head())
        if args.negative_fixtures:
            negative_fixtures(packet, root)
        if args.rehearse_publish_rollback:
            if not args.local_only or not args.inspect:
                raise ValueError('rehearsal requires --local-only --inspect; release remains held')
            rehearse(packet, root)
        print(json.dumps({'commit': packet['commit'], 'version': packet['version'],
                          'integrity': 'PASS', 'decision': 'HOLD', 'blockers': blockers}, indent=2))
        return 0 if args.inspect else 1
    except (ValueError, KeyError, TypeError, OSError, subprocess.CalledProcessError) as error:
        parser.exit(1, 'Release packet refused: ' + str(error) + '\n')


if __name__ == '__main__':
    raise SystemExit(main())
