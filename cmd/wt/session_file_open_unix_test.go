//go:build linux || darwin

package main

import (
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestOpenSessionFileNoFollowRejectsFIFOWithoutBlocking(t *testing.T) {
	root := t.TempDir()
	name := "pipe"
	if err := unix.Mkfifo(filepath.Join(root, name), 0o600); err != nil {
		t.Fatal(err)
	}
	if file, err := openSessionFileNoFollow(root, name); err == nil {
		_ = file.Close()
		t.Fatal("FIFO opened as a downloadable file")
	}
}
