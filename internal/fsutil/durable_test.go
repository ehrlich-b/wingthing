package fsutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyncDirectory(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "entry"), []byte("durable"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SyncDirectory(directory); err != nil {
		t.Fatalf("SyncDirectory: %v", err)
	}
}

func TestSyncDirectoryReportsOpenFailure(t *testing.T) {
	err := SyncDirectory(filepath.Join(t.TempDir(), "missing"))
	if err == nil || !strings.Contains(err.Error(), "open directory for sync") {
		t.Fatalf("SyncDirectory error = %v", err)
	}
}
