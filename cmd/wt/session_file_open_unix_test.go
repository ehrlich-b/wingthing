//go:build linux || darwin

package main

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestOpenSessionFileNoFollowRejectsFIFOWithoutBlocking(t *testing.T) {
	root := t.TempDir()
	name := "pipe"
	if err := unix.Mkfifo(filepath.Join(root, name), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		file, err := openSessionFileNoFollow(root, name)
		if file != nil {
			_ = file.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO opened as a downloadable file")
		}
		if errors.Is(err, unix.ENXIO) {
			t.Fatalf("FIFO open used write-only nonblocking semantics: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("FIFO open blocked waiting for a writer")
	}
}
