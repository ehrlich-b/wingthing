"""Bounded interface tests: real pinned inputs, empty temporary directories, no browser."""
import hashlib
import importlib.util
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[1]


def load(name):
    spec = importlib.util.spec_from_file_location(name, HERE / (name + '.py'))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


BASELINE = load('session-answer-baseline')
RETAIN = load('retain-session-captures')


class SessionHelperInputs(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temporary = tempfile.TemporaryDirectory(prefix='session-helper-inputs-')
        cls.root = Path(cls.temporary.name)
        cls.source = cls.root / 'explicit source'
        cls.source.mkdir()
        # Reconstruct only the helper's declared source files and pinned object.
        for name in [*('test/web/' + name for name in BASELINE.SUPPORT), *BASELINE.REGRESSIONS]:
            target = cls.source / name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(REPO / name, target)
        subject = json.loads((HERE / 'fixtures/session-answer-subject.json').read_text())
        for command in [['git', 'init', '-q'], ['git', 'fetch', '-q', '--depth=1', str(REPO), subject['head']]]:
            subprocess.run(['nice', '-n', '10', *command], cwd=cls.source, check=True,
                           stdout=subprocess.PIPE, stderr=subprocess.PIPE)

    @classmethod
    def tearDownClass(cls):
        cls.temporary.cleanup()

    def setUp(self):
        self.case = self.root / self._testMethodName
        self.case.mkdir()
        self.packet = self.case / 'explicit packet'
        manifest = json.loads((HERE / 'fixtures/session-answer-baseline-inputs.json').read_text())
        for name in manifest:
            target = self.packet / name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(HERE / 'fixtures/session-answer-baseline' / name, target)
        self.work, self.output = self.case / 'empty workspace', self.case / 'reports'

    def validate(self):
        return BASELINE.validate(self.source, self.packet, self.work, self.output)

    def test_exact_declared_packet_and_subject(self):
        manifest, subject = self.validate()
        self.assertEqual(len(manifest), 8)
        self.assertEqual(subject['head'], 'fb896393e94e710fd23cc52ee49c091ffbbccd8f')
        self.assertEqual({str(p.relative_to(self.packet)) for p in self.packet.rglob('*') if p.is_file()}, set(manifest))
        self.assertFalse(self.work.exists())
        self.assertFalse(self.output.exists())

    def test_absent_baseline_file_fails_before_writes(self):
        # Reconstruct an incomplete packet without deleting any existing input.
        incomplete = self.case / 'incomplete'
        incomplete.mkdir()
        with self.assertRaisesRegex(ValueError, 'missing baseline input'):
            BASELINE.validate(self.source, incomplete, self.work, self.output)
        self.assertFalse(self.work.exists())
        self.assertFalse(self.output.exists())

    def test_wrong_baseline_hash_fails(self):
        (self.packet / 'evidence/REMEDIATION.json').write_text('{}\n')
        with self.assertRaisesRegex(ValueError, 'wrong baseline hash'):
            self.validate()

    def test_public_packet_cannot_claim_historical_hashes(self):
        with self.assertRaisesRegex(ValueError, 'wrong baseline hash'):
            BASELINE.validate(self.source, self.packet, self.work, self.output, 'historical')
        self.assertFalse(self.work.exists())
        self.assertFalse(self.output.exists())

    def test_unknown_packet_format_fails_before_writes(self):
        with self.assertRaisesRegex(ValueError, 'unknown packet format'):
            BASELINE.validate(self.source, self.packet, self.work, self.output, 'unknown')
        self.assertFalse(self.work.exists())
        self.assertFalse(self.output.exists())

    def test_escaping_paths_fail(self):
        for name in ['../outside', '/absolute', 'evidence/../../outside']:
            with self.subTest(name=name), self.assertRaisesRegex(ValueError, 'escaping input path'):
                BASELINE.inside(self.packet, name)
        (self.packet / 'escape').symlink_to(self.source, target_is_directory=True)
        with self.assertRaisesRegex(ValueError, 'escaping input path'):
            BASELINE.inside(self.packet, 'escape/test/web/session-answer-audit.py')

    def test_escaping_subject_manifest_fails_before_execution(self):
        manifest = self.source / 'test/web/fixtures/session-answer-subject.json'
        original = manifest.read_bytes()
        value = json.loads(original)
        value['files'] = {'../../outside': '0' * 64}
        try:
            manifest.write_text(json.dumps(value))
            with self.assertRaisesRegex(ValueError, 'escaping input path'):
                self.validate()
        finally:
            manifest.write_bytes(original)
        self.assertFalse(self.work.exists())

    def test_baseline_symlink_escape_fails_before_writes(self):
        outside = self.case / 'outside.json'
        outside.write_bytes((self.packet / 'evidence/REMEDIATION.json').read_bytes())
        incomplete = self.case / 'linked packet'
        incomplete.mkdir()
        for name in json.loads((HERE / 'fixtures/session-answer-baseline-inputs.json').read_text()):
            target = incomplete / name
            target.parent.mkdir(parents=True, exist_ok=True)
            if name == 'evidence/REMEDIATION.json':
                target.symlink_to(outside)
            else:
                shutil.copyfile(self.packet / name, target)
        with self.assertRaisesRegex(ValueError, 'escaping input path'):
            BASELINE.validate(self.source, incomplete, self.work, self.output)
        self.assertFalse(self.work.exists())

    def test_wrong_subject_hash_fails(self):
        manifest = self.source / 'test/web/fixtures/session-answer-subject.json'
        original = manifest.read_bytes()
        value = json.loads(original)
        value['files']['web/src/conversation-recovery.js'] = '0' * 64
        try:
            manifest.write_text(json.dumps(value))
            with self.assertRaisesRegex(ValueError, 'wrong subject hash'):
                self.validate()
        finally:
            manifest.write_bytes(original)
        self.assertFalse(self.work.exists())

    def test_overlapping_or_nonempty_destinations_fail(self):
        with self.assertRaisesRegex(ValueError, 'disjoint'):
            BASELINE.validate(self.source, self.packet, self.packet / 'work', self.output)
        self.output.mkdir()
        (self.output / 'existing').write_text('preserve')
        with self.assertRaisesRegex(ValueError, 'must be empty'):
            self.validate()
        self.assertEqual((self.output / 'existing').read_text(), 'preserve')

    def test_cli_requires_all_paths_even_from_unrelated_cwd(self):
        for script in ['session-answer-baseline.py', 'retain-session-captures.py']:
            result = subprocess.run([sys.executable, '-B', str(HERE / script)], cwd=self.case,
                                    text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
            self.assertEqual(result.returncode, 2, result.stdout)
            self.assertIn('--source', result.stdout)
            self.assertIn('--output', result.stdout)

    def test_baseline_cli_missing_file_fails_closed(self):
        incomplete = self.case / 'absent packet'
        incomplete.mkdir()
        result = subprocess.run([sys.executable, '-B', str(HERE / 'session-answer-baseline.py'),
                                 '--source', str(self.source), '--baseline', str(incomplete),
                                 '--work-dir', str(self.work), '--output', str(self.output)],
                                cwd=self.case, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('missing baseline input', result.stdout)
        self.assertFalse(self.work.exists())

    def test_capture_cli_all_modes_preserve_selection_and_report_hashes(self):
        source = self.case / 'captures'
        source.mkdir()
        for name in ['state.json', 'events.jsonl', 'claude-control-one.jpg', 'codex-control-one.jpg', 'large.jpg', 'raw.txt']:
            (source / name).write_text(name)
        for mode in RETAIN.MODES:
            output = self.case / mode
            result = subprocess.run([sys.executable, '-B', str(HERE / 'retain-session-captures.py'),
                                     '--source', str(source), '--output', str(output), '--mode', mode],
                                    cwd=self.case, check=True, text=True, stdout=subprocess.PIPE)
            report = json.loads(result.stdout)
            expected = {p.name for p in source.iterdir() if p.suffix != '.txt'}
            if mode == 'native-final':
                expected.remove('large.jpg')
            self.assertEqual({p.name for p in output.iterdir()}, expected)
            self.assertEqual({item['path'] for item in report['inputs']}, expected)
            for item in report['inputs']:
                self.assertEqual(item['sha256'], hashlib.sha256((source / item['path']).read_bytes()).hexdigest())
                self.assertEqual((output / item['path']).read_bytes(), (source / item['path']).read_bytes())

    def test_capture_symlink_and_overwrite_fail_without_copying(self):
        source = self.case / 'captures'
        source.mkdir()
        (source / 'escape.json').symlink_to(self.packet / 'evidence/REMEDIATION.json')
        with self.assertRaisesRegex(ValueError, 'symlink'):
            RETAIN.retain(source, self.output, 'states-final')
        self.assertFalse(self.output.exists())
        other = self.case / 'other'
        other.mkdir()
        (other / 'same.json').write_text('new')
        self.output.mkdir()
        (self.output / 'same.json').write_text('old')
        with self.assertRaisesRegex(ValueError, 'already exists'):
            RETAIN.retain(other, self.output, 'states-final')
        self.assertEqual((self.output / 'same.json').read_text(), 'old')

    def test_missing_capture_source_and_overlap_fail(self):
        with self.assertRaises(FileNotFoundError):
            RETAIN.retain(self.case / 'missing', self.output, 'states-final')
        with self.assertRaisesRegex(ValueError, 'disjoint'):
            RETAIN.retain(self.packet, self.packet / 'output', 'states-final')
