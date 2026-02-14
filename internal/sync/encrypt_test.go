package sync

import (
	"bytes"
	"crypto/rand"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/store"
)

func TestDeriveKeyConsistent(t *testing.T) {
	salt := []byte("0123456789abcdef")
	k1 := DeriveKey("my-passphrase", salt)
	k2 := DeriveKey("my-passphrase", salt)
	if !bytes.Equal(k1, k2) {
		t.Error("same passphrase+salt produced different keys")
	}
	if len(k1) != 32 {
		t.Errorf("key length = %d, want 32", len(k1))
	}
}

func TestDeriveKeyDifferentPassphrases(t *testing.T) {
	salt := []byte("0123456789abcdef")
	k1 := DeriveKey("passphrase-a", salt)
	k2 := DeriveKey("passphrase-b", salt)
	if bytes.Equal(k1, k2) {
		t.Error("different passphrases produced same key")
	}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)

	plaintext := []byte("hello, wingthing encryption")
	ciphertext, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	if bytes.Equal(ciphertext, plaintext) {
		t.Error("ciphertext equals plaintext")
	}

	decrypted, err := Decrypt(key, ciphertext)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Errorf("decrypted = %q, want %q", decrypted, plaintext)
	}
}

func TestDecryptWrongKey(t *testing.T) {
	key1 := make([]byte, 32)
	key2 := make([]byte, 32)
	rand.Read(key1)
	rand.Read(key2)

	ciphertext, err := Encrypt(key1, []byte("secret"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	_, err = Decrypt(key2, ciphertext)
	if err == nil {
		t.Error("decrypt with wrong key should fail")
	}
}

func TestDecryptTamperedCiphertext(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)

	ciphertext, err := Encrypt(key, []byte("authentic data"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	// Flip a byte in the ciphertext body (after the nonce)
	ciphertext[len(ciphertext)-1] ^= 0xff

	_, err = Decrypt(key, ciphertext)
	if err == nil {
		t.Error("decrypt of tampered ciphertext should fail")
	}
}

func TestGenerateSaltUnique(t *testing.T) {
	s1, err := GenerateSalt()
	if err != nil {
		t.Fatalf("generate salt 1: %v", err)
	}
	s2, err := GenerateSalt()
	if err != nil {
		t.Fatalf("generate salt 2: %v", err)
	}
	if bytes.Equal(s1, s2) {
		t.Error("two salts are identical")
	}
	if len(s1) != 16 {
		t.Errorf("salt length = %d, want 16", len(s1))
	}
}

func TestKeyStoreInitUnlock(t *testing.T) {
	dir := t.TempDir()
	ks := NewKeyStore(dir)

	if ks.IsInitialized() {
		t.Error("keystore should not be initialized yet")
	}

	if err := ks.Init("test-passphrase"); err != nil {
		t.Fatalf("init: %v", err)
	}

	if !ks.IsInitialized() {
		t.Error("keystore should be initialized after init")
	}

	key, err := ks.Unlock("test-passphrase")
	if err != nil {
		t.Fatalf("unlock: %v", err)
	}
	if len(key) != 32 {
		t.Errorf("key length = %d, want 32", len(key))
	}

	// Unlock again to verify consistency
	key2, err := ks.Unlock("test-passphrase")
	if err != nil {
		t.Fatalf("unlock again: %v", err)
	}
	if !bytes.Equal(key, key2) {
		t.Error("unlock returned different keys")
	}
}

func TestKeyStoreUnlockWrongPassphrase(t *testing.T) {
	dir := t.TempDir()
	ks := NewKeyStore(dir)

	if err := ks.Init("correct-passphrase"); err != nil {
		t.Fatalf("init: %v", err)
	}

	_, err := ks.Unlock("wrong-passphrase")
	if err == nil {
		t.Error("unlock with wrong passphrase should fail")
	}
}

func TestEncryptedEngineFileRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)

	ee := &EncryptedEngine{Key: key}

	plaintext := []byte("# My Memory\nThis is private content.")
	ciphertext, err := ee.EncryptFile(plaintext)
	if err != nil {
		t.Fatalf("encrypt file: %v", err)
	}

	decrypted, err := ee.DecryptFile(ciphertext)
	if err != nil {
		t.Fatalf("decrypt file: %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Errorf("decrypted = %q, want %q", decrypted, plaintext)
	}
}

func TestEncryptedEngineThreadEntries(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)

	ee := &EncryptedEngine{Key: key}

	taskID := "task-001"
	userInput := "deploy the thing"

	entries := []*store.ThreadEntry{
		{
			TaskID:    &taskID,
			WingID: "mac-01",
			Summary:   "deployed to staging",
			UserInput: &userInput,
		},
		{
			WingID: "mac-01",
			Summary:   "ran tests",
			UserInput: nil,
		},
	}

	encrypted, err := ee.EncryptThreadEntries(entries)
	if err != nil {
		t.Fatalf("encrypt entries: %v", err)
	}

	// Encrypted fields should differ from originals
	if encrypted[0].Summary == entries[0].Summary {
		t.Error("encrypted summary matches plaintext")
	}
	if *encrypted[0].UserInput == *entries[0].UserInput {
		t.Error("encrypted user_input matches plaintext")
	}

	// Originals should be unmodified
	if entries[0].Summary != "deployed to staging" {
		t.Error("original entry was modified")
	}

	// nil UserInput should stay nil
	if encrypted[1].UserInput != nil {
		t.Error("nil user_input should remain nil after encryption")
	}

	// Decrypt round-trip
	decrypted, err := ee.DecryptThreadEntries(encrypted)
	if err != nil {
		t.Fatalf("decrypt entries: %v", err)
	}

	if decrypted[0].Summary != "deployed to staging" {
		t.Errorf("decrypted summary = %q, want 'deployed to staging'", decrypted[0].Summary)
	}
	if *decrypted[0].UserInput != "deploy the thing" {
		t.Errorf("decrypted user_input = %q, want 'deploy the thing'", *decrypted[0].UserInput)
	}
	if decrypted[1].Summary != "ran tests" {
		t.Errorf("decrypted summary = %q, want 'ran tests'", decrypted[1].Summary)
	}
	if decrypted[1].UserInput != nil {
		t.Error("nil user_input should remain nil after decrypt")
	}
}
