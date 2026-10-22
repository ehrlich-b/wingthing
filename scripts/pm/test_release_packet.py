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
