package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenBoundedRegularRejectsMissingDirectoryAndOversized(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "input")
	if err := os.WriteFile(path, []byte("contents"), 0400); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{dir, filepath.Join(dir, "missing"), path} {
		if file, err := OpenBoundedRegular(name, 2); err == nil {
			file.Close()
			t.Fatalf("accepted invalid %s", name)
		}
	}
	file, err := OpenBoundedRegular(path, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Write([]byte("bad")); err == nil {
		t.Fatal("opened writable file")
	}
}
