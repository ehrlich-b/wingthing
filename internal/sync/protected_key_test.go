package sync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyncKeyReaderRefusesHardLink(t *testing.T) {
	ks := NewKeyStore(t.TempDir())
	if err := ks.Init("fixture-passphrase"); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(ks.keyFilePath(), filepath.Join(t.TempDir(), "ordinary")); err != nil {
		t.Fatal(err)
	}
	if _, err := ks.Unlock("fixture-passphrase"); err == nil || !strings.Contains(err.Error(), "hard links") {
		t.Fatalf("read linked sync key: %v", err)
	}
}

func TestSyncKeyInitDoesNotWriteThroughAlias(t *testing.T) {
	ks := NewKeyStore(t.TempDir())
	if err := os.WriteFile(ks.keyFilePath(), []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "ordinary")
	if err := os.Link(ks.keyFilePath(), alias); err != nil {
		t.Fatal(err)
	}
	if err := ks.Init("fixture-passphrase"); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(alias); err != nil || string(data) != "untouched" {
		t.Fatalf("key initialization followed alias: %q, %v", data, err)
	}
}
