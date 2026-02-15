package auth

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"sync"
	"time"

	goecdh "crypto/ecdh"
)

// VerifyPasskeyAssertion verifies a WebAuthn assertion using a raw P-256
// public key (64 bytes: X||Y). Uses Go stdlib only — no external library.
func VerifyPasskeyAssertion(allowedKey, challenge, authenticatorData, clientDataJSON, signature []byte) error {
	// 1. Parse clientDataJSON, verify challenge matches
	var cd struct {
		Challenge string `json:"challenge"`
		Type      string `json:"type"`
	}
	if err := json.Unmarshal(clientDataJSON, &cd); err != nil {
		return errors.New("invalid clientDataJSON")
	}
	if cd.Type != "webauthn.get" {
		return errors.New("wrong type: expected webauthn.get")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(cd.Challenge)
	if err != nil {
		return errors.New("invalid challenge encoding")
	}
	if !bytes.Equal(decoded, challenge) {
		return errors.New("challenge mismatch")
	}

	// 2. Build signed data: authenticatorData || SHA-256(clientDataJSON)
	cdHash := sha256.Sum256(clientDataJSON)
	signedData := make([]byte, len(authenticatorData)+len(cdHash))
	copy(signedData, authenticatorData)
	copy(signedData[len(authenticatorData):], cdHash[:])

	// 3. Hash the signed data
	digest := sha256.Sum256(signedData)

	// 4. Parse P-256 public key (64 bytes: X||Y)
	if len(allowedKey) != 64 {
		return errors.New("invalid key length: expected 64 bytes")
	}
	pubKey := &ecdsa.PublicKey{
		Curve: elliptic.P256(),
		X:     new(big.Int).SetBytes(allowedKey[:32]),
		Y:     new(big.Int).SetBytes(allowedKey[32:]),
	}

	// 5. Verify ECDSA-SHA256 signature (ASN.1 DER encoded)
	if !ecdsa.VerifyASN1(pubKey, digest[:], signature) {
		return errors.New("invalid passkey signature")
	}
	return nil
}

// GenerateChallenge returns 32 random bytes for a passkey challenge.
func GenerateChallenge() ([]byte, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	return b, err
}

// GenerateAuthToken returns a random hex-encoded auth token (32 bytes).
func GenerateAuthToken() (string, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// IsValidP256Point checks if 64 raw bytes (X||Y) represent a valid point on the P-256 curve.
func IsValidP256Point(raw []byte) bool {
	if len(raw) != 64 {
		return false
	}
	// Build uncompressed point encoding: 0x04 || X || Y
	uncompressed := make([]byte, 65)
	uncompressed[0] = 0x04
	copy(uncompressed[1:], raw)
	_, err := goecdh.P256().NewPublicKey(uncompressed)
	return err == nil
}

// SHA256Sum returns the SHA-256 hash of data.
func SHA256Sum(data []byte) [32]byte {
	return sha256.Sum256(data)
}

// authEntry stores a cached auth token with its creation time.
type authEntry struct {
	pubKey    []byte
	createdAt time.Time
}

// AuthCache caches passkey auth tokens in memory. Tokens live until the
// process dies (boot-scoped) or until auth_ttl expires. Wing restart revokes all sessions.
type AuthCache struct {
	mu     sync.Mutex
	tokens map[string]authEntry // token → entry
}

// NewAuthCache creates a new boot-scoped in-memory auth cache.
func NewAuthCache() *AuthCache {
	return &AuthCache{tokens: make(map[string]authEntry)}
}

// Put stores a token with the given public key.
func (c *AuthCache) Put(token string, pubKey []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tokens[token] = authEntry{pubKey: pubKey, createdAt: time.Now()}
}

// Check returns the public key for a valid token. If ttl > 0, expired tokens
// are rejected and removed from the cache.
func (c *AuthCache) Check(token string, ttl time.Duration) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.tokens[token]
	if !ok {
		return nil, false
	}
	if ttl > 0 && time.Since(entry.createdAt) > ttl {
		delete(c.tokens, token)
		return nil, false
	}
	return entry.pubKey, true
}
