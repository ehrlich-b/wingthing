package fsutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/testsocket"
)

func TestSyncDirectory(t *testing.T) {
	directory := testsocket.Dir(t)
	if err := os.WriteFile(filepath.Join(directory, "entry"), []byte("durable"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SyncDirectory(directory); err != nil {
		t.Fatalf("SyncDirectory: %v", err)
	}
}

func TestSyncDirectoryReportsOpenFailure(t *testing.T) {
	err := SyncDirectory(filepath.Join(testsocket.Dir(t), "missing"))
	if err == nil || !strings.Contains(err.Error(), "open directory for sync") {
		t.Fatalf("SyncDirectory error = %v", err)
	}
}
