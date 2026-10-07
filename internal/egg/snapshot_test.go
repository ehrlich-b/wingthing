package egg

import (
	"fmt"
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
	snapshot := SnapshotAgentConfig("claude", "")
	defer snapshot.Restore()
	if snapshot == nil {
		t.Fatal("personal sandbox lost its host configuration snapshot")
	}
}

func TestConfigSnapshotRestoreRefusesSymlinkWithoutFollowingIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("original config"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot := SnapshotAgentConfig("claude", "")
	target := filepath.Join(t.TempDir(), "must-not-change")
	if err := os.WriteFile(target, []byte("unrelated"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	snapshot.Restore()
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "unrelated" {
		t.Fatalf("symlink target changed: %q, %v", data, err)
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("restore admitted a substituted symlink: %v, %v", info, err)
	}
}

func TestConfigSnapshotRefusesSecretSymlink(t *testing.T) {
	for _, parent := range []bool{false, true} {
		t.Run(fmt.Sprint("parent=", parent), func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			secretDir := filepath.Join(home, ".wingthing")
			if err := os.MkdirAll(secretDir, 0700); err != nil {
				t.Fatal(err)
			}
			secret := filepath.Join(secretDir, "settings.json")
			if err := os.WriteFile(secret, []byte("private-wing-key"), 0600); err != nil {
				t.Fatal(err)
			}
			provider := filepath.Join(home, ".claude")
			path := filepath.Join(provider, "settings.json")
			if parent {
				if err := os.Symlink(secretDir, provider); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(provider, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(secret, path); err != nil {
					t.Fatal(err)
				}
			}
			snapshot := SnapshotAgentConfig("claude", "")
			if parent {
				if err := os.Remove(provider); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(provider, 0700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
				t.Fatal(err)
			}
			snapshot.Restore()
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "{}" {
				t.Fatalf("snapshot laundered denied secret: %q, %v", data, err)
			}
		})
	}
}

func TestConfigSnapshotRestoreRefusesParentSymlink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	provider := filepath.Join(home, ".claude")
	if err := os.Mkdir(provider, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(provider, "settings.json")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot := SnapshotAgentConfig("claude", "")
	if err := os.Rename(provider, provider+"-old"); err != nil {
		t.Fatal(err)
	}
	victim := t.TempDir()
	victimPath := filepath.Join(victim, "settings.json")
	if err := os.WriteFile(victimPath, []byte("victim"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, provider); err != nil {
		t.Fatal(err)
	}
	snapshot.Restore()
	data, err := os.ReadFile(victimPath)
	if err != nil || string(data) != "victim" {
		t.Fatalf("restore followed parent symlink: %q, %v", data, err)
	}
}

func TestConfigSnapshotRestoresRegularFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	provider := filepath.Join(home, ".claude")
	path := filepath.Join(provider, "settings.json")
	if err := os.Mkdir(provider, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("original config"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot := SnapshotAgentConfig("claude", "")
	if err := os.RemoveAll(provider); err != nil {
		t.Fatal(err)
	}
	snapshot.Restore()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "original config" {
		t.Fatalf("restored config = %q, %v", data, err)
	}
	assertPrivateRegularFile(t, path)
	info, err := os.Stat(provider)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("restored config directory is not private: %v, %v", info, err)
	}
}
