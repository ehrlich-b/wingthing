// Package testsocket provides short, private directories for Unix socket fixtures.
package testsocket

import (
	"os"
	"path/filepath"
	"testing"
)

// Dir reserves room for fixture socket names and removes the directory at cleanup.
func Dir(t testing.TB) string {
	t.Helper()
	base := os.TempDir()
	// Leave room for nested fixture paths on both Darwin and Linux.
	if len(filepath.Join(base, "wt-1234567890")) > 50 {
		base = "/tmp"
	}
	dir, err := os.MkdirTemp(base, "wt-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
