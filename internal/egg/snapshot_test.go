package egg

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsolatedClaudeSettingsAreNotRolledBackWhenSessionExits(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	for _, exists := range []bool{false, true} {
		if exists {
			if err := os.WriteFile(path, []byte(`{"theme":"old"}`), 0600); err != nil {
				t.Fatal(err)
			}
		}
		first := SnapshotAgentConfig("claude", filepath.Join(home, "isolated-user"))
		second := SnapshotAgentConfig("claude", filepath.Join(home, "another-user"))
		want := `{"theme":"user-changed","model":"claude-sonnet-5"}`
		if err := os.WriteFile(path, []byte(want), 0600); err != nil {
			t.Fatal(err)
		}
		second.Restore()
		first.Restore()
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Fatalf("session exit clobbered current settings: %q, %v", data, err)
		}
	}
}

func TestPersonalClaudeRetainsHostConfigSnapshotBoundary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if SnapshotAgentConfig("claude", "") == nil {
		t.Fatal("personal sandbox lost its host configuration snapshot")
	}
}

func TestConfigSnapshotRestoreReplacesSymlinkWithoutFollowingIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	target := filepath.Join(t.TempDir(), "must-not-change")
	if err := os.WriteFile(target, []byte("unrelated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}

	(&ConfigSnapshot{files: map[string][]byte{path: []byte("original config")}}).Restore()

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "unrelated" {
		t.Fatalf("symlink target changed: %q", data)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original config" {
		t.Fatalf("restored config = %q", data)
	}
	assertPrivateRegularFile(t, path)
}
