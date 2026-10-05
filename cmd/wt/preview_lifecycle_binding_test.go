//go:build darwin || linux

package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ehrlich-b/wingthing/internal/config"
)

// Synthetic selected and default preview states only. The planted pid files
// are malformed, so no process is ever signalled, and a stale wing.status
// directory blocks the pre-spawn cleanup, so no daemon child is ever started.
func previewLifecycleBindingFixture(t *testing.T) (state, defaultDir string) {
	t.Helper()
	state, provider, _ := previewProviderBindingFixture(t, "")
	defaultDir = config.DefaultDir()
	if strings.HasPrefix(defaultDir+string(filepath.Separator), state+string(filepath.Separator)) || filepath.Dir(filepath.Dir(defaultDir)) != filepath.Dir(state) {
		t.Fatalf("setup: default %q is not a temp sibling of selected %q", defaultDir, state)
	}
	for _, dir := range []string{filepath.Join(defaultDir, "wing.status"), filepath.Join(state, "wing.status")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatalf("setup: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "keep"), []byte("stale\n"), 0600); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	for _, dir := range []string{defaultDir, state} {
		if err := os.WriteFile(filepath.Join(dir, "wing.pid"), []byte("not-a-pid\n"), 0644); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	missing := filepath.Join(filepath.Dir(provider), "missing provider-home")
	if err := os.WriteFile(filepath.Join(state, config.ProviderHomeBinding), []byte(missing+"\n"), 0600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if _, err := config.Load(); err == nil {
		t.Fatal("setup: invalid provider binding loaded")
	}
	return state, defaultDir
}

func lifecycleTreeSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		entry := info.Mode().String()
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			entry += " " + string(data)
		}
		snap[rel] = entry
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return snap
}

func assertLifecycleTreeUnchanged(t *testing.T, label, root string, before map[string]string) {
	t.Helper()
	after := lifecycleTreeSnapshot(t, root)
	for rel, entry := range before {
		if got, ok := after[rel]; !ok {
			t.Errorf("%s: %s was removed", label, rel)
		} else if got != entry {
			t.Errorf("%s: %s changed: %q -> %q", label, rel, entry, got)
		}
	}
	for rel := range after {
		if _, ok := before[rel]; !ok {
			t.Errorf("%s: %s was created", label, rel)
		}
	}
}

func assertDaemonPathsAvoid(t *testing.T, defaultDir string) {
	t.Helper()
	for name, path := range map[string]string{
		"wing.pid": wingPidPath(), "wing.args": wingArgsPath(), "wing.log": wingLogPath(), "wing.status": wingStatusPath(),
		"roost.pid": roostPidPath(), "roost.args": roostArgsPath(), "roost.log": roostLogPath(),
	} {
		if path != "" && filepath.Dir(path) == defaultDir {
			t.Errorf("%s path %q falls back to the default state", name, path)
		}
	}
}

func runLifecycleCommand(cmd *cobra.Command, args ...string) error {
	cmd.SetArgs(args)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	return cmd.Execute()
}

func TestPreviewLifecycleBindingInvalidRefusesWithoutTouchingEitherState(t *testing.T) {
	cases := map[string]func() error{
		"lock": func() error {
			lock, err := acquireDaemonLifecycleLock()
			if lock != nil {
				_ = lock.Close()
			}
			return err
		},
		"wing start": func() error { return runLifecycleCommand(wingStartCmd(), "--local") },
		"wing stop":  func() error { return runLifecycleCommand(wingStopCmd()) },
		"stop":       func() error { return runLifecycleCommand(stopCmd()) },
		"roost stop": func() error { return runLifecycleCommand(roostStopCmd()) },
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			state, defaultDir := previewLifecycleBindingFixture(t)
			stateBefore := lifecycleTreeSnapshot(t, state)
			defaultBefore := lifecycleTreeSnapshot(t, defaultDir)
			if err := run(); err == nil {
				t.Errorf("%s succeeded with an invalid provider binding", name)
			} else if !strings.Contains(err.Error(), "provider") {
				t.Errorf("%s failed for an unrelated reason: %v", name, err)
			}
			assertLifecycleTreeUnchanged(t, "selected state", state, stateBefore)
			assertLifecycleTreeUnchanged(t, "default state", defaultDir, defaultBefore)
			assertDaemonPathsAvoid(t, defaultDir)
		})
	}
}

func TestPreviewLifecycleBindingStateDirErrorNeverBecomesDefault(t *testing.T) {
	state, defaultDir := previewLifecycleBindingFixture(t)
	t.Setenv("WINGTHING_PREVIEW_DIR", filepath.Join(filepath.Dir(state), "other state"))
	if _, err := config.StateDir(); err == nil {
		t.Fatal("setup: conflicting state selection resolved")
	}
	defaultBefore := lifecycleTreeSnapshot(t, defaultDir)
	lock, err := acquireDaemonLifecycleLock()
	if lock != nil {
		_ = lock.Close()
	}
	if err == nil {
		t.Error("daemon lifecycle lock acquired without a selected state")
	}
	assertLifecycleTreeUnchanged(t, "default state", defaultDir, defaultBefore)
	assertDaemonPathsAvoid(t, defaultDir)
}

func TestPreviewLifecycleBindingValidSelectionUsesSelectedState(t *testing.T) {
	for _, bound := range []bool{true, false} {
		name := "bound"
		if !bound {
			name = "unbound"
		}
		t.Run(name, func(t *testing.T) {
			state, _, _ := previewProviderBindingFixture(t, "")
			if !bound {
				if err := os.Remove(filepath.Join(state, config.ProviderHomeBinding)); err != nil {
					t.Fatalf("setup: %v", err)
				}
			}
			lock, err := acquireDaemonLifecycleLock()
			if err != nil {
				t.Fatal(err)
			}
			_ = lock.Close()
			if _, err := os.Stat(filepath.Join(state, "daemon.lock")); err != nil {
				t.Errorf("lifecycle lock not in selected state: %v", err)
			}
			if got := wingPidPath(); got != filepath.Join(state, "wing.pid") {
				t.Errorf("wing pid path = %q, want selected state", got)
			}
			if got := roostLogPath(); got != filepath.Join(state, "roost.log") {
				t.Errorf("roost log path = %q, want selected state", got)
			}
			if _, err := os.Stat(config.DefaultDir()); !os.IsNotExist(err) {
				t.Errorf("default preview state touched: %v", err)
			}
		})
	}
}

func TestPreviewLifecycleBindingStableDefaultUnchanged(t *testing.T) {
	old := config.ReleaseChannel
	config.ReleaseChannel = "stable"
	t.Cleanup(func() { config.ReleaseChannel = old })
	home := config.CanonicalProviderPath(t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("WINGTHING_DIR", "")
	t.Setenv("WINGTHING_PREVIEW_DIR", "")
	want := filepath.Join(home, ".wingthing")
	lock, err := acquireDaemonLifecycleLock()
	if err != nil {
		t.Fatal(err)
	}
	_ = lock.Close()
	if _, err := os.Stat(filepath.Join(want, "daemon.lock")); err != nil {
		t.Errorf("stable lifecycle lock not in default state: %v", err)
	}
	if got := wingPidPath(); got != filepath.Join(want, "wing.pid") {
		t.Errorf("stable wing pid path = %q, want %q", got, filepath.Join(want, "wing.pid"))
	}
	if got := roostArgsPath(); got != filepath.Join(want, "roost.args") {
		t.Errorf("stable roost args path = %q", got)
	}
}
