package egg

import (
	"os"
	"path/filepath"
	"testing"
)

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
