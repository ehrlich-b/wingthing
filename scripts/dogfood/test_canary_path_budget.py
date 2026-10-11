"""Pure allocation probes: no native sockets or built binary execution."""
import importlib.util
from unittest import mock
import unittest

import test_forge_package as fixtures

spec = importlib.util.spec_from_file_location("canary_path_budget", fixtures.HERE / "canary.py")
canary = importlib.util.module_from_spec(spec)
spec.loader.exec_module(canary)


class CanaryPathBudgetTests(fixtures.ForgeFixture):
    def test_current_clone_path_allocates_within_darwin_budget(self):
        scratch = fixtures.REPO / ".scratch"
        root = canary.fixture_root(scratch, "darwin")
        self.additional_roots.append(root)
        socket_path = root / "s/eggs" / ("f" * 16) / "egg.sock"
        self.assertLessEqual(len(str(socket_path).encode()), 103)
        self.assertEqual(root.stat().st_mode & 0o777, 0o700)
        self.assertEqual(root.parent, scratch)
        self.assertEqual(len(root.name), 3)

    def test_impossibly_long_path_refuses_before_state_allocation(self):
        scratch = self.root / ("x" * 100)
        scratch.mkdir()
        with self.assertRaisesRegex(RuntimeError, "socket budget"):
            canary.fixture_root(scratch, "darwin")
        self.assertEqual(list(scratch.iterdir()), [])

    def test_occupied_names_never_overwrite_prior_inputs(self):
        scratch = fixtures.REPO / ".scratch"
        root = scratch / "AAA"
        root.mkdir(mode=0o700, exist_ok=True)
        self.additional_roots.append(root)
        note = root / "immutable-input"
        note.write_text("keep")
        with mock.patch.object(canary.secrets, "choice", return_value="A"):
            with self.assertRaisesRegex(RuntimeError, "namespace occupied"):
                canary.fixture_root(scratch, "darwin")
        self.assertEqual(note.read_text(), "keep")
