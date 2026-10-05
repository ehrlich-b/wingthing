package eggclient

import (
	"errors"
	"os"
	"path/filepath"

	"testing"
)

func TestLifecycleJournalSurvivesWingReaping(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "egg")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"egg.meta", "egg.owner", "egg.pid", "egg.token", "lifecycle.jsonl"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	CleanEggDir(dir)
	for _, name := range []string{"egg.meta", "egg.owner", "lifecycle.jsonl"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("lost %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "egg.pid")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime pid retained: %v", err)
	}
}
