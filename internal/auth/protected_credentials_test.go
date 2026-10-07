package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCredentialReadersRefuseHardLinks(t *testing.T) {
	for _, name := range []string{"wing_key", "device_token.yaml", "local_device_token.yaml"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if name == "wing_key" {
				if _, err := EnsureKeyPair(dir); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(filepath.Join(dir, name), []byte("device_token: fixture\n"), 0600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, name)
			alias := filepath.Join(t.TempDir(), "ordinary")
			if err := os.Link(path, alias); err != nil {
				t.Fatal(err)
			}
			var err error
			switch name {
			case "wing_key":
				_, err = EnsureKeyPair(dir)
				if err == nil {
					t.Fatal("EnsureKeyPair admitted hard-linked private key")
				}
				_, err = LoadPrivateKey(dir)
			case "device_token.yaml":
				_, err = NewTokenStore(dir).Load()
			case "local_device_token.yaml":
				_, err = NewLocalTokenStore(dir).Load()
			}
			if err == nil || !strings.Contains(err.Error(), "hard links") {
				t.Fatalf("credential reader admitted workspace alias: %v", err)
			}
		})
	}
}

func TestEnsureKeyPairRefusesSymlinkWithoutReplacingTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "untouched")
	if err := os.WriteFile(target, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "wing_key")); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureKeyPair(dir); err == nil {
		t.Fatal("followed key symlink")
	}
	if data, err := os.ReadFile(target); err != nil || len(data) != 0 {
		t.Fatalf("changed symlink target: %q, %v", data, err)
	}
}

func TestCredentialReadersRequirePrivateMode(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnsureKeyPair(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "wing_key"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPrivateKey(dir); err == nil {
		t.Fatal("accepted world-readable private key")
	}
	store := NewTokenStore(dir)
	if err := store.Save(&DeviceToken{Token: "secret"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store.tokenPath(), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("accepted world-readable token")
	}
}
