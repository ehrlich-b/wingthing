package auth

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
)

const keyFileName = "wing_key"

// EnsureKeyPair loads or generates an X25519 keypair.
// Returns the base64-encoded public key. Private key is stored in dir/wing_key.
func EnsureKeyPair(dir string) (string, error) {
	keyPath := filepath.Join(dir, keyFileName)

	// Try loading existing key
	data, err := os.ReadFile(keyPath)
	if err == nil && len(data) > 0 {
		privBytes, err := base64.StdEncoding.DecodeString(string(data))
		if err != nil {
			return "", fmt.Errorf("decode existing key: %w", err)
		}
		priv, err := ecdh.X25519().NewPrivateKey(privBytes)
		if err != nil {
			return "", fmt.Errorf("parse existing key: %w", err)
		}
		return base64.StdEncoding.EncodeToString(priv.PublicKey().Bytes()), nil
	}

	// Generate new keypair
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", fmt.Errorf("generate key: %w", err)
	}

	encoded := base64.StdEncoding.EncodeToString(priv.Bytes())
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create dir: %w", err)
	}
	if err := os.WriteFile(keyPath, []byte(encoded), 0600); err != nil {
		return "", fmt.Errorf("write key: %w", err)
	}

	return base64.StdEncoding.EncodeToString(priv.PublicKey().Bytes()), nil
}

// LoadPrivateKey loads the X25519 private key from disk.
func LoadPrivateKey(dir string) (*ecdh.PrivateKey, error) {
	keyPath := filepath.Join(dir, keyFileName)
	data, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("read key: %w", err)
	}
	privBytes, err := base64.StdEncoding.DecodeString(string(data))
	if err != nil {
		return nil, fmt.Errorf("decode key: %w", err)
	}
	return ecdh.X25519().NewPrivateKey(privBytes)
}
