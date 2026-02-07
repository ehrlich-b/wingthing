package sync

import (
	"encoding/base64"
	"fmt"

	"github.com/ehrlich-b/wingthing/internal/store"
)

// EncryptedEngine wraps Engine with E2E encryption.
type EncryptedEngine struct {
	Engine *Engine
	Key    []byte // 32-byte symmetric key (unlocked from KeyStore)
}

// EncryptFile encrypts plaintext file content.
func (ee *EncryptedEngine) EncryptFile(plaintext []byte) ([]byte, error) {
	return Encrypt(ee.Key, plaintext)
}

// DecryptFile decrypts data encrypted by EncryptFile.
func (ee *EncryptedEngine) DecryptFile(ciphertext []byte) ([]byte, error) {
	return Decrypt(ee.Key, ciphertext)
}

// EncryptThreadEntries encrypts the Summary and UserInput fields of thread entries.
// Returns new copies; the originals are not modified.
func (ee *EncryptedEngine) EncryptThreadEntries(entries []*store.ThreadEntry) ([]*store.ThreadEntry, error) {
	out := make([]*store.ThreadEntry, len(entries))
	for i, e := range entries {
		clone := *e
		enc, err := Encrypt(ee.Key, []byte(clone.Summary))
		if err != nil {
			return nil, fmt.Errorf("encrypt summary: %w", err)
		}
		clone.Summary = base64.StdEncoding.EncodeToString(enc)

		if clone.UserInput != nil {
			enc, err := Encrypt(ee.Key, []byte(*clone.UserInput))
			if err != nil {
				return nil, fmt.Errorf("encrypt user_input: %w", err)
			}
			s := base64.StdEncoding.EncodeToString(enc)
			clone.UserInput = &s
		}

		out[i] = &clone
	}
	return out, nil
}

// DecryptThreadEntries decrypts entries encrypted by EncryptThreadEntries.
// Returns new copies; the originals are not modified.
func (ee *EncryptedEngine) DecryptThreadEntries(entries []*store.ThreadEntry) ([]*store.ThreadEntry, error) {
	out := make([]*store.ThreadEntry, len(entries))
	for i, e := range entries {
		clone := *e

		ciphertext, err := base64.StdEncoding.DecodeString(clone.Summary)
		if err != nil {
			return nil, fmt.Errorf("decode summary: %w", err)
		}
		plain, err := Decrypt(ee.Key, ciphertext)
		if err != nil {
			return nil, fmt.Errorf("decrypt summary: %w", err)
		}
		clone.Summary = string(plain)

		if clone.UserInput != nil {
			ciphertext, err := base64.StdEncoding.DecodeString(*clone.UserInput)
			if err != nil {
				return nil, fmt.Errorf("decode user_input: %w", err)
			}
			plain, err := Decrypt(ee.Key, ciphertext)
			if err != nil {
				return nil, fmt.Errorf("decrypt user_input: %w", err)
			}
			s := string(plain)
			clone.UserInput = &s
		}

		out[i] = &clone
	}
	return out, nil
}
