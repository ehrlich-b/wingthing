// Package testsocket provides short, private directories for Unix socket fixtures.
package testsocket

import (
	"os"
	"path/filepath"
	"testing"
)

// Dir creates a private physical fixture root independent of checkout depth and
// ambient temporary directory settings, and removes it at cleanup.
func Dir(t testing.TB) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "wt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
