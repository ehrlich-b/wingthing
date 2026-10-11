//go:build !windows

package fsutil

import (
	"path/filepath"
	"syscall"
	"testing"
)

func TestOpenBoundedRegularRejectsFIFOWithoutOpening(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	if file, err := OpenBoundedRegular(path, 64<<10); err == nil {
		file.Close()
		t.Fatal("FIFO accepted")
	}
}
