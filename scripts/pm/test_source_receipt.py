#!/usr/bin/env python3
"""Exercise coverage with real Git commits and strict original report headers."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

from source_receipt import Invalid, TOOLS, check_coverage, git, review_headers


class CoverageTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(dir=os.environ.get('TMPDIR'))
        self.addCleanup(self.tmp.cleanup)
        self.repo = Path(self.tmp.name)
        git(self.repo, 'init', '--quiet')
        git(self.repo, 'config', 'user.name', 'Bryan Ehrlich')
        git(self.repo, 'config', 'user.email', 'ehrlich.bryan@gmail.com')
        self.source = 'internal/relay/access.go'
        self.tool = 'scripts/pm/test_source_receipt.py'
        for name in [self.source, *TOOLS]:
            path = self.repo / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text('original ' + name + '\n' * 100)
        self.commit()
        self.base = git(self.repo, 'rev-parse', 'HEAD').decode().strip()

    def commit(self):
        git(self.repo, 'add', '--all')
        git(self.repo, 'commit', '--quiet', '-m', 'Record a coverage fixture.')

    def test_unchanged_and_tool_only_changes(self):
        self.assertEqual(check_coverage(self.repo, self.base), set())
        (self.repo / self.tool).write_text('updated tool\n')
        self.commit()
        self.assertEqual(check_coverage(self.repo, self.base), {self.tool})

    def test_every_exempt_tool_has_a_tracked_file(self):
        actual = Path(__file__).resolve().parents[2]
        for name in TOOLS:
            self.assertTrue((actual / name).is_file(), name)
            git(actual, 'ls-files', '--error-unmatch', name)

    def test_application_renamed_onto_tool_is_rejected(self):
        git(self.repo, 'config', 'diff.renames', 'true')
        git(self.repo, 'mv', '-f', self.source, self.tool)
        self.commit()
        with self.assertRaisesRegex(Invalid, 'coverage: internal/relay/access.go'):
            check_coverage(self.repo, self.base)

    def test_copy_detection_cannot_hide_modified_source(self):
        git(self.repo, 'config', 'diff.renames', 'copies')
        (self.repo / self.tool).write_bytes((self.repo / self.source).read_bytes())
        (self.repo / self.source).write_text('changed application\n')
        self.commit()
        with self.assertRaisesRegex(Invalid, 'coverage: internal/relay/access.go'):
            check_coverage(self.repo, self.base)

    def test_external_diff_cannot_hide_deletion(self):
        git(self.repo, 'config', 'diff.external', '/bin/true')
        (self.repo / self.source).unlink()
        self.commit()
        with self.assertRaisesRegex(Invalid, 'coverage: internal/relay/access.go'):
            check_coverage(self.repo, self.base)

    def test_newline_in_application_name_cannot_split_into_exempt_paths(self):
        path = self.repo / (self.tool + '\nscripts/pm/source_receipt.py')
        path.parent.mkdir(parents=True)
        path.write_text('application\n')
        self.commit()
        with self.assertRaisesRegex(Invalid, 'coverage'):
            check_coverage(self.repo, self.base)

    def test_unrelated_head_explains_ancestry_failure(self):
        git(self.repo, 'checkout', '--quiet', '--orphan', 'unrelated')
        git(self.repo, 'commit', '--quiet', '-m', 'Record an unrelated fixture.')
        with self.assertRaisesRegex(Invalid, 'required base is not an ancestor of HEAD'):
            check_coverage(self.repo, self.base)


class ReviewHeaderTests(unittest.TestCase):
    commit = 'a' * 40

    def test_original_header_and_quoted_body(self):
        text = 'Verdict: FAIL\nReviewed: ' + self.commit + '\n\nVerdict: PASS\nReviewed: ' + 'b' * 40
        self.assertEqual(review_headers(text, 'security-review'), (self.commit, 'FAIL'))

    def test_headers_embedded_in_body_are_rejected(self):
        text = 'A quoted assessment follows.\n\nVerdict: PASS\nReviewed: ' + self.commit
        with self.assertRaisesRegex(Invalid, 'unrecognized original review: integration-review'):
            review_headers(text, 'integration-review')

    def test_missing_reordered_or_split_headers_are_rejected(self):
        for text in ['', 'Verdict: PASS', 'Reviewed: ' + self.commit + '\nVerdict: PASS',
                     'Verdict: PASS\n\nReviewed: ' + self.commit]:
            with self.subTest(text=text), self.assertRaises(Invalid):
                review_headers(text, 'integration-review')


if __name__ == '__main__':
    unittest.main()
