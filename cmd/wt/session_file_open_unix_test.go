//go:build linux || darwin

package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
	"golang.org/x/sys/unix"
)

func TestOpenSessionFileNoFollowRejectsHardlinks(t *testing.T) {
	root := wingpolicy.CanonicalPolicyPath(t.TempDir())
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(secret, filepath.Join(root, "allowed.txt")); err != nil {
		t.Fatal(err)
	}
	file, err := openSessionFileNoFollow(root, "allowed.txt")
	if file != nil {
		_ = file.Close()
	}
	if err == nil {
		t.Fatal("hardlink to a file outside the authorized root was accepted")
	}
	if err := os.Remove(secret); err != nil {
		t.Fatal(err)
	}
	file, err = openSessionFileNoFollow(root, "allowed.txt")
	if err != nil {
		t.Fatalf("single-link regular file rejected: %v", err)
	}
	_ = file.Close()
}

func TestOpenSessionFileNoFollowRejectsRootAncestorSymlink(t *testing.T) {
	base := wingpolicy.CanonicalPolicyPath(t.TempDir())
	realParent := filepath.Join(base, "real")
	root := filepath.Join(realParent, "workspace")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realParent, filepath.Join(base, "swapped")); err != nil {
		t.Fatal(err)
	}
	file, err := openSessionFileNoFollow(filepath.Join(base, "swapped", "workspace"), "secret.txt")
	if file != nil {
		_ = file.Close()
	}
	if err == nil {
		t.Fatal("symlink in an authorized root's ancestor was followed")
	}
	file, err = openSessionFileNoFollow(root, "secret.txt")
	if err != nil {
		t.Fatalf("root without symlinks rejected: %v", err)
	}
	_ = file.Close()
}

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
