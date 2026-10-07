//go:build darwin || linux

package protectedfile

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestProtectedFileRefusesFIFO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("accepted FIFO")
	}
}

func TestProtectedFileRefusesUnexpectedOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path, os.Getuid()+1, -1); err != nil {
		t.Skipf("cannot change owner: %v", err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("accepted another owner's file")
	}
}
