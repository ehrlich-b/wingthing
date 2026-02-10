package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

// DeriveSharedKey performs X25519 ECDH + HKDF to produce an AES-256-GCM key.
func DeriveSharedKey(privateKey *ecdh.PrivateKey, peerPublicKeyB64 string) (cipher.AEAD, error) {
	peerPubBytes, err := base64.StdEncoding.DecodeString(peerPublicKeyB64)
	if err != nil {
		return nil, fmt.Errorf("decode peer public key: %w", err)
	}
	peerPub, err := ecdh.X25519().NewPublicKey(peerPubBytes)
	if err != nil {
		return nil, fmt.Errorf("parse peer public key: %w", err)
	}

	shared, err := privateKey.ECDH(peerPub)
	if err != nil {
		return nil, fmt.Errorf("ecdh: %w", err)
	}

	// HKDF-SHA256, salt = 32 zero bytes, info = "wt-pty"
	salt := make([]byte, 32)
	kdf := hkdf.New(sha256.New, shared, salt, []byte("wt-pty"))
	aesKey := make([]byte, 32)
	if _, err := io.ReadFull(kdf, aesKey); err != nil {
		return nil, fmt.Errorf("hkdf: %w", err)
	}

	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return nil, fmt.Errorf("aes: %w", err)
	}
	return cipher.NewGCM(block)
}

// Encrypt encrypts plaintext with AES-256-GCM and returns base64(iv || ciphertext || tag).
func Encrypt(gcm cipher.AEAD, plaintext []byte) (string, error) {
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil) // nonce || ciphertext+tag
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt decodes base64 input then decrypts AES-256-GCM (iv || ciphertext || tag).
func Decrypt(gcm cipher.AEAD, encoded string) ([]byte, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	return gcm.Open(nil, nonce, ciphertext, nil)
}
