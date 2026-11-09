//go:build !windows

package fsutil

import (
	"path/filepath"
	"syscall"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/testsocket"
)

func TestOpenBoundedRegularRejectsFIFOWithoutOpening(t *testing.T) {
	path := filepath.Join(testsocket.Dir(t), "fifo")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	if file, err := OpenBoundedRegular(path, 64<<10); err == nil {
		file.Close()
		t.Fatal("FIFO accepted")
	}
}
