"""Synthetic packet contract tests; these never qualify a release or platform."""
import copy
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location('release_packet', Path(__file__).with_name('release_packet.py'))
packet_tool = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(packet_tool)


class PacketContract(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(dir=os.environ.get('TMPDIR'))
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        (self.root / 'artifacts').mkdir()
        (self.root / 'tests').mkdir()
        self.head = 'a' * 40
        self.packet = {'schema': 1, 'commit': self.head, 'version': 'v0.0.999999',
                       'artifacts': {}, 'build_logs': {}, 'tests': [],
                       'assessment': None, 'publication': 'not authorized', 'decision': 'HOLD',
                       'supported_features': packet_tool.FEATURES,
                       'build_limits': packet_tool.BUILD_LIMITS.copy(),
                       'phone_pairing': {p: {'status': 'unqualified', 'receipt': None}
                                         for p in packet_tool.PLATFORMS},
                       'qualifications': {k: {'status': 'pending', 'receipt': None}
                                          for k in packet_tool.QUALIFICATIONS}}
        for platform in packet_tool.PLATFORMS:
            self.packet['artifacts'][platform] = self.file('artifacts/wt-' + platform,
                                                          'SYNTHETIC ' + platform)
            self.packet['build_logs'][platform] = self.file('build-' + platform + '.txt',
                                                           'SYNTHETIC build log')
        for key in ('operator', 'installer'):
            self.packet[key] = self.file(key + '.txt', 'SYNTHETIC ' + key)
        self.original = self.file('assessment.txt',
                                  'Commit: ' + self.head + '\nDisposition: PASS\nSYNTHETIC fixture\n')
        self.packet['assessment'] = {'commit': self.head, 'disposition': 'PASS',
                                     'original': self.original['path'],
                                     'sha256': self.original['sha256']}
        for name in packet_tool.REQUIRED_TESTS:
            log = self.file('tests/' + name + '.txt', 'SYNTHETIC test log')
            self.packet['tests'].append({'id': name, 'commit': self.head,
                                         'exit_code': 0, 'command': ['fixture-only'],
                                         'log': log['path'], 'sha256': log['sha256']})
        (self.root / 'SHA256SUMS').write_text(''.join(
            a['sha256'] + '  ' + a['path'] + '\n' for a in self.packet['artifacts'].values()))

    def file(self, name, content):
        path = self.root / name
        path.write_text(content)
        return {'path': name, 'sha256': packet_tool.digest(path)}

    def check(self):
        return packet_tool.verify(self.packet, self.root, self.head, binary_identity=False)

    def test_complete_inventory_still_holds_for_external_qualification(self):
        blockers = self.check()
        self.assertFalse(any('missing test:' in b for b in blockers))
        for name in packet_tool.QUALIFICATIONS:
            self.assertIn('pending qualification: ' + name, blockers)

    def test_missing_receipts_block(self):
        self.packet['tests'] = []
        blockers = self.check()
        for name in packet_tool.REQUIRED_TESTS:
            self.assertIn('missing test: ' + name, blockers)
        self.packet['assessment'] = None
        self.assertIn('missing original exact-commit assessment', self.check())

    def test_foreign_assessment_fails_even_with_original_bytes(self):
        self.packet['assessment']['commit'] = 'b' * 40
        with self.assertRaisesRegex(ValueError, 'assessment is for another commit'):
            self.check()

    def test_relabelled_ancestor_original_fails(self):
        original = self.file('assessment.txt', 'Commit: ' + 'b' * 40 + '\nDisposition: PASS\n')
        self.packet['assessment']['sha256'] = original['sha256']
        with self.assertRaisesRegex(ValueError, 'original assessment scope'):
            self.check()

    def test_foreign_test_and_packet_fail(self):
        self.packet['tests'][0]['commit'] = 'b' * 40
        with self.assertRaisesRegex(ValueError, 'test receipt is for another commit'):
            self.check()
        with self.assertRaisesRegex(ValueError, 'differs from current committed HEAD'):
            packet_tool.verify(self.packet, self.root, 'c' * 40, binary_identity=False)

    def test_failed_test_and_nonpass_disposition_block(self):
        self.packet['tests'][0]['exit_code'] = 1
        self.packet['assessment']['disposition'] = 'FAIL'
        original = self.file('assessment.txt', 'Commit: ' + self.head + '\nDisposition: FAIL\n')
        self.packet['assessment']['sha256'] = original['sha256']
        self.assertIn('failed test: make-check', self.check())
        self.assertIn('assessment disposition: FAIL', self.check())

    def test_tampered_log_artifact_original_and_manifest_fail(self):
        for name in ('tests/race.txt', 'artifacts/wt-darwin-arm64', 'assessment.txt', 'SHA256SUMS'):
            with self.subTest(name=name):
                path = self.root / name
                original = path.read_bytes()
                path.write_bytes(original + b'tamper')
                with self.assertRaises(ValueError):
                    self.check()
                path.write_bytes(original)

    def test_missing_platform_duplicate_receipt_and_path_escape_fail(self):
        original = copy.deepcopy(self.packet)
        del self.packet['artifacts']['darwin-amd64']
        with self.assertRaises(ValueError):
            self.check()
        self.packet = copy.deepcopy(original)
        self.packet['tests'].append(self.packet['tests'][0])
        with self.assertRaises(ValueError):
            self.check()
        self.packet = original
        self.packet['artifacts']['linux-amd64']['path'] = '../outside'
        with self.assertRaisesRegex(ValueError, 'escapes root'):
            self.check()

    def test_symlink_is_not_an_immutable_artifact(self):
        path = self.root / 'alias'
        path.symlink_to(self.root / 'operator.txt')
        self.packet['operator']['path'] = 'alias'
        with self.assertRaisesRegex(ValueError, 'symlink'):
            self.check()

    def test_cross_build_cannot_become_native_receipt(self):
        self.packet['qualifications']['native-mac'] = {'status': 'passed', 'receipt': 'linux-log'}
        with self.assertRaisesRegex(ValueError, 'independently validated supplier evidence'):
            self.check()

    def pairing_receipt(self, kind='synthetic-fixture'):
        platform = 'linux-amd64'
        log = self.file('pair-attach.txt', 'SYNTHETIC pair PASS; attach PASS')
        self.receipt = {'schema': 1, 'kind': kind, 'commit': self.head,
                        'platform': platform,
                        'binary_sha256': self.packet['artifacts'][platform]['sha256'],
                        'outcomes': {'pair': 'PASS', 'attach': 'PASS'},
                        'command': ['fixture-only', 'pair-and-attach'], 'exit_code': 0,
                        'log': log}
        self.save_pairing_receipt()
        self.packet['phone_pairing'][platform]['status'] = (
            'fixture-passed' if kind == 'synthetic-fixture' else 'qualified')

    def save_pairing_receipt(self):
        retained = self.file('pairing.json', json.dumps(self.receipt))
        self.packet['phone_pairing']['linux-amd64']['receipt'] = retained

    def binary_metadata(self, command, **kwargs):
        # Mocked metadata is only a contract oracle, never a package receipt.
        platform = Path(command[-1]).name.removeprefix('wt-')
        system, arch = platform.split('-')
        return '\n'.join(('vcs.revision=' + self.head, 'vcs.modified=false',
                          'GOOS=' + system, 'GOARCH=' + arch, 'CGO_ENABLED=0',
                          '-X main.version=' + self.packet['version']))

    def test_absent_receipt_is_unqualified_regardless_of_cgo(self):
        for platform in packet_tool.PLATFORMS:
            self.assertIn('phone pairing unqualified: ' + platform + ' (unqualified)', self.check())
        self.packet['phone_pairing']['linux-amd64']['status'] = 'qualified'
        with self.assertRaisesRegex(ValueError, 'unsupported phone pairing capability claim'):
            self.check()

    def test_valid_synthetic_pair_attach_fixture_never_qualifies_package(self):
        self.pairing_receipt()
        self.assertEqual(packet_tool.pairing_status(
            self.packet, self.root, 'linux-amd64', binary_identity=False), 'fixture-passed')
        self.assertIn('phone pairing unqualified: linux-amd64 (fixture-passed)', self.check())
        self.packet['phone_pairing']['linux-amd64']['status'] = 'qualified'
        with self.assertRaisesRegex(ValueError, 'unsupported phone pairing capability claim'):
            self.check()

    def test_packaged_receipt_requires_binary_identity_and_preserves_other_holds(self):
        self.pairing_receipt('packaged-binary')
        with self.assertRaisesRegex(ValueError, 'requires binary identity verification'):
            self.check()
        with patch.object(packet_tool, 'git', return_value=''), \
                patch.object(packet_tool.subprocess, 'check_output', side_effect=self.binary_metadata):
            blockers = packet_tool.verify(self.packet, self.root, self.head)
        self.assertFalse(any('phone pairing unqualified: linux-amd64' in b for b in blockers))
        for platform in packet_tool.PLATFORMS[1:]:
            self.assertIn('phone pairing unqualified: ' + platform + ' (unqualified)', blockers)
        for name in packet_tool.QUALIFICATIONS:
            self.assertIn('pending qualification: ' + name, blockers)
        self.assertIn('authoritative version uniqueness and publication authorization pending', blockers)
        self.assertEqual(self.packet['decision'], 'HOLD')
        self.assertEqual(self.packet['publication'], 'not authorized')
        self.packet['assessment']['commit'] = 'b' * 40
        with patch.object(packet_tool, 'git', return_value=''), \
                patch.object(packet_tool.subprocess, 'check_output', side_effect=self.binary_metadata):
            with self.assertRaisesRegex(ValueError, 'assessment is for another commit'):
                packet_tool.verify(self.packet, self.root, self.head)

    def test_stale_source_and_other_platform_pairing_receipts_fail(self):
        self.pairing_receipt()
        original = copy.deepcopy(self.receipt)
        for field, value, reason in (('commit', 'b' * 40, 'another commit'),
                                     ('platform', 'darwin-arm64', 'another platform'),
                                     ('binary_sha256', '0' * 64, 'binary checksum mismatch')):
            with self.subTest(field=field):
                self.receipt = dict(original, **{field: value})
                self.save_pairing_receipt()
                with self.assertRaisesRegex(ValueError, reason):
                    self.check()

    def test_changed_binary_cannot_reuse_pairing_receipt_even_after_rehash(self):
        self.pairing_receipt()
        self.packet['artifacts']['linux-amd64'] = self.file(
            'artifacts/wt-linux-amd64', 'SYNTHETIC replacement bytes')
        (self.root / 'SHA256SUMS').write_text(''.join(
            a['sha256'] + '  ' + a['path'] + '\n' for a in self.packet['artifacts'].values()))
        with self.assertRaisesRegex(ValueError, 'pairing receipt binary checksum mismatch'):
            self.check()

    def test_pair_and_attach_success_command_and_log_are_required(self):
        self.pairing_receipt()
        original = copy.deepcopy(self.receipt)
        cases = [('outcomes', {'pair': 'PASS'}, 'pair and attach'),
                 ('outcomes', {'pair': 'PASS', 'attach': 'FAIL'}, 'pair and attach'),
                 ('exit_code', 1, 'pair and attach'), ('exit_code', False, 'pair and attach'),
                 ('command', [], 'execution command'), ('command', 'assertion', 'execution command'),
                 ('kind', 'source-test', 'unknown pairing receipt kind'),
                 ('log', {'path': '../escape', 'sha256': '0' * 64}, 'escapes root')]
        for field, value, reason in cases:
            with self.subTest(field=field, value=value):
                self.receipt = dict(original, **{field: value})
                self.save_pairing_receipt()
                with self.assertRaisesRegex(ValueError, reason):
                    self.check()
        self.receipt = original
        self.save_pairing_receipt()
        (self.root / 'pair-attach.txt').write_text('changed log')
        with self.assertRaisesRegex(ValueError, 'pairing receipt log checksum mismatch'):
            self.check()

    def test_missing_and_tampered_pairing_receipts_fail(self):
        self.pairing_receipt()
        entry = self.packet['phone_pairing']['linux-amd64']['receipt']
        entry['path'] = 'absent.json'
        with self.assertRaisesRegex(ValueError, 'missing nonempty regular file'):
            self.check()
        entry['path'] = 'pairing.json'
        (self.root / 'pairing.json').write_text('{}')
        with self.assertRaisesRegex(ValueError, 'pairing receipt checksum mismatch'):
            self.check()

    def test_staging_retains_pairing_receipt_and_log_without_relabelling(self):
        self.pairing_receipt()
        source = self.root / 'pairing.json'
        original = source.read_bytes()
        self.packet['phone_pairing']['linux-amd64'] = {'status': 'unqualified', 'receipt': None}
        packet_tool.include_pairing_receipts(self.packet, self.root, [source])
        retained = self.packet['phone_pairing']['linux-amd64']['receipt']
        self.assertEqual((self.root / retained['path']).read_bytes(), original)
        self.assertIn('phone pairing unqualified: linux-amd64 (fixture-passed)', self.check())
        with self.assertRaisesRegex(ValueError, 'duplicate pairing receipt'):
            packet_tool.include_pairing_receipts(self.packet, self.root, [source])

    def test_pairing_cannot_be_claimed_via_global_features_or_build_flags(self):
        self.packet['build_limits']['phone_pairing'] = 'supported'
        with self.assertRaisesRegex(ValueError, 'unsupported build profile'):
            self.check()
        self.packet['build_limits'] = packet_tool.BUILD_LIMITS.copy()
        self.packet['supported_features'] = packet_tool.FEATURES + ['explicit LAN pairing']
        with self.assertRaisesRegex(ValueError, 'unsupported feature claim'):
            self.check()

    def test_existing_version_is_rejected(self):
        with patch.object(packet_tool, 'git', return_value='v0.0.999999'):
            with self.assertRaisesRegex(ValueError, 'already exists'):
                self.check()

    def test_fake_binary_cannot_satisfy_source_identity(self):
        with self.assertRaises(subprocess.CalledProcessError):
            packet_tool.verify(self.packet, self.root, self.head)

    def test_actual_compiler_preserves_version_in_build_metadata(self):
        source = self.root / 'main.go'
        source.write_text('package main\nimport "fmt"\nvar version = "dev"\n'
                          'func main() { fmt.Println(version) }\n')
        target = self.root / 'metadata-probe'
        command = packet_tool.build_command('v0.0.999999', target)
        command[-1] = str(source)
        subprocess.run(command, check=True, env=dict(os.environ, CGO_ENABLED='0'))
        info = subprocess.check_output(['go', 'version', '-m', str(target)], text=True)
        self.assertIn('-X main.version=v0.0.999999', info)
        self.assertIn('CGO_ENABLED=0', info)
        self.assertEqual(subprocess.check_output([str(target)], text=True).strip(), 'v0.0.999999')

    def test_duplicate_json_keys_fail(self):
        path = self.root / 'duplicate.json'
        path.write_text('{"commit":"a","commit":"b"}')
        with self.assertRaisesRegex(ValueError, 'duplicate JSON key'):
            packet_tool.load(path)

    def test_strict_cli_never_approves_an_incomplete_inventory(self):
        write = self.root / 'candidate.json'
        write.write_text(json.dumps(self.packet))
        with patch.object(packet_tool, 'clean_head', return_value=self.head), \
                patch.object(packet_tool, 'verify', return_value=['missing test: race']), \
                patch('sys.argv', ['release_packet.py', '--verify', str(write)]):
            self.assertEqual(packet_tool.main(), 1)

    def test_rehearsal_preserves_source_and_uses_no_network(self):
        before = {str(p): p.read_bytes() for p in self.root.rglob('*') if p.is_file()}
        with patch('socket.socket', side_effect=AssertionError('network forbidden')):
            packet_tool.rehearse(self.packet, self.root)
        self.assertEqual(before, {str(p): p.read_bytes() for p in self.root.rglob('*') if p.is_file()})


if __name__ == '__main__':
    unittest.main()
