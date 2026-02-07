package sync

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// KeyStore manages encryption keys for sync.
type KeyStore struct {
	Dir string // ~/.wingthing/
}

// KeyFile is the on-disk format for the encryption key material.
type KeyFile struct {
	Salt         []byte `yaml:"salt"`
	EncryptedKey []byte `yaml:"encrypted_key"`
	KeyHash      string `yaml:"key_hash"`
	CreatedAt    int64  `yaml:"created_at"`
}

func NewKeyStore(dir string) *KeyStore {
	return &KeyStore{Dir: dir}
}

func (ks *KeyStore) keyFilePath() string {
	return filepath.Join(ks.Dir, "sync.key")
}

// Init generates a new random symmetric key, encrypts it with the passphrase, and saves.
func (ks *KeyStore) Init(passphrase string) error {
	salt, err := GenerateSalt()
	if err != nil {
		return fmt.Errorf("generate salt: %w", err)
	}

	// Generate the actual symmetric key
	symKey := make([]byte, 32)
	if _, err := rand.Read(symKey); err != nil {
		return fmt.Errorf("generate symmetric key: %w", err)
	}

	// Derive wrapping key from passphrase
	wrapKey := DeriveKey(passphrase, salt)

	// Encrypt the symmetric key with the wrapping key
	encrypted, err := Encrypt(wrapKey, symKey)
	if err != nil {
		return fmt.Errorf("encrypt symmetric key: %w", err)
	}

	// Hash the symmetric key for validation
	hash := sha256.Sum256(symKey)

	kf := &KeyFile{
		Salt:         salt,
		EncryptedKey: encrypted,
		KeyHash:      hex.EncodeToString(hash[:]),
		CreatedAt:    time.Now().UTC().Unix(),
	}

	data, err := yaml.Marshal(kf)
	if err != nil {
		return fmt.Errorf("marshal key file: %w", err)
	}

	if err := os.MkdirAll(ks.Dir, 0o700); err != nil {
		return fmt.Errorf("create key dir: %w", err)
	}

	if err := os.WriteFile(ks.keyFilePath(), data, 0o600); err != nil {
		return fmt.Errorf("write key file: %w", err)
	}

	return nil
}

// Unlock derives the key from passphrase, decrypts the stored symmetric key, returns it.
func (ks *KeyStore) Unlock(passphrase string) ([]byte, error) {
	data, err := os.ReadFile(ks.keyFilePath())
	if err != nil {
		return nil, fmt.Errorf("read key file: %w", err)
	}

	var kf KeyFile
	if err := yaml.Unmarshal(data, &kf); err != nil {
		return nil, fmt.Errorf("parse key file: %w", err)
	}

	wrapKey := DeriveKey(passphrase, kf.Salt)

	symKey, err := Decrypt(wrapKey, kf.EncryptedKey)
	if err != nil {
		return nil, fmt.Errorf("unlock: %w", err)
	}

	// Validate the decrypted key against stored hash
	hash := sha256.Sum256(symKey)
	if hex.EncodeToString(hash[:]) != kf.KeyHash {
		return nil, fmt.Errorf("key validation failed")
	}

	return symKey, nil
}

// IsInitialized checks if a keyfile exists.
func (ks *KeyStore) IsInitialized() bool {
	_, err := os.Stat(ks.keyFilePath())
	return err == nil
}
