//go:build linux

package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestPrefixPublicationPreservesHostChangesDuringStaging(t *testing.T) {
	for _, change := range []string{"replace", "in-place", "create", "delete"} {
		t.Run(change, func(t *testing.T) {
			dir := t.TempDir()
			name := ".claude.json"
			destination := filepath.Join(dir, name)
			if change != "create" {
				if err := os.WriteFile(destination, []byte("snapshot"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			parent, err := os.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer parent.Close()
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			defer writer.Close()
			done := make(chan error, 1)
			go func() { done <- publishPinnedFile(reader, 0600, parent, name) }()
			// Writing more than the pipe capacity requires a read, proving
			// comparison has completed. Keep EOF pending during the host edit.
			capacity, err := unix.FcntlInt(writer.Fd(), unix.F_GETPIPE_SZ, 0)
			if err != nil {
				t.Fatal(err)
			}
			session := strings.Repeat("session", capacity/len("session")+1)
			if _, err := writer.Write([]byte(session)); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "replace":
				if err := os.WriteFile(filepath.Join(dir, "host-new"), []byte("host"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(filepath.Join(dir, "host-new"), destination); err != nil {
					t.Fatal(err)
				}
			case "in-place", "create":
				if err := os.WriteFile(destination, []byte("host"), 0600); err != nil {
					t.Fatal(err)
				}
			case "delete":
				if err := os.Remove(destination); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("prefix publication did not finish")
			}
			conflicts, err := filepath.Glob(filepath.Join(dir, ".wingthing-conflict-*"))
			if err != nil || len(conflicts) != 1 {
				t.Fatalf("host edit was not preserved as a conflict: %v, %v", conflicts, err)
			}
			versions := make(map[string]bool)
			for _, path := range append(conflicts, destination) {
				data, err := os.ReadFile(path)
				if os.IsNotExist(err) && change == "delete" {
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				versions[string(data)] = true
			}
			if !versions[session] || (change != "delete" && !versions["host"]) {
				t.Fatalf("lost a concurrent version: session=%v host=%v", versions[session], versions["host"])
			}
			if change == "delete" {
				if _, err := os.Lstat(destination); !os.IsNotExist(err) {
					t.Fatalf("session undid host deletion: %v", err)
				}
			}
		})
	}
}
