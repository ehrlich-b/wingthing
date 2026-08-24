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
	"fmt"
	"math/big"
	"net/url"
	"sync"
	"time"

	goecdh "crypto/ecdh"
)

// PasskeyPolicy defines the relying-party properties a wing requires from a
// WebAuthn assertion. These values are derived from the configured roost, not
// accepted from the browser or relay.
type PasskeyPolicy struct {
	RPID                    string
	Origins                 []string
	RequireUserVerification bool
}

// VerifyPasskeyAssertion verifies a WebAuthn assertion using a raw P-256
// public key (64 bytes: X||Y). In addition to the signature and challenge it
// validates the relying-party hash, browser origin, ceremony type, user
// presence, and (when requested) user verification.
func VerifyPasskeyAssertion(allowedKey, challenge, authenticatorData, clientDataJSON, signature []byte, policy PasskeyPolicy) error {
	if len(challenge) < 16 {
		return errors.New("challenge too short")
	}
	if policy.RPID == "" || len(policy.Origins) == 0 {
		return errors.New("passkey relying-party policy is incomplete")
	}

	// 1. Parse clientDataJSON and verify the browser ceremony context.
	var cd struct {
		Challenge   string `json:"challenge"`
		Type        string `json:"type"`
		Origin      string `json:"origin"`
		CrossOrigin bool   `json:"crossOrigin"`
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
	if cd.CrossOrigin {
		return errors.New("cross-origin passkey assertion rejected")
	}
	if !originAllowed(cd.Origin, policy.Origins) {
		return fmt.Errorf("passkey origin %q is not allowed", cd.Origin)
	}

	// authenticatorData is rpIdHash (32) || flags (1) || signCount (4) || ...
	if len(authenticatorData) < 37 {
		return errors.New("authenticator data too short")
	}
	rpIDHash := sha256.Sum256([]byte(policy.RPID))
	if !bytes.Equal(authenticatorData[:32], rpIDHash[:]) {
		return errors.New("relying-party ID hash mismatch")
	}
	flags := authenticatorData[32]
	if flags&0x01 == 0 {
		return errors.New("user presence is required")
	}
	if policy.RequireUserVerification && flags&0x04 == 0 {
		return errors.New("user verification is required")
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
	if !pubKey.Curve.IsOnCurve(pubKey.X, pubKey.Y) {
		return errors.New("invalid P-256 public key")
	}

	// 5. Verify ECDSA-SHA256 signature (ASN.1 DER encoded)
	if !ecdsa.VerifyASN1(pubKey, digest[:], signature) {
		return errors.New("invalid passkey signature")
	}
	return nil
}

func originAllowed(candidate string, allowed []string) bool {
	candidateURL, err := url.Parse(candidate)
	if err != nil || candidateURL.Scheme == "" || candidateURL.Host == "" || candidateURL.Path != "" {
		return false
	}
	candidateURL.RawQuery = ""
	candidateURL.Fragment = ""
	normalized := candidateURL.String()
	for _, value := range allowed {
		u, err := url.Parse(value)
		if err != nil || u.Scheme == "" || u.Host == "" {
			continue
		}
		u.Path = ""
		u.RawPath = ""
		u.RawQuery = ""
		u.Fragment = ""
		if normalized == u.String() {
			return true
		}
	}
	return false
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
	subject   string
	createdAt time.Time
}

// AuthCache caches passkey auth tokens in memory. Boot-scoped: tokens are
// valid until the wing process dies. Restart revokes everything. An optional
// TTL can further limit token lifetime (0 means no expiry).
type AuthCache struct {
	mu     sync.Mutex
	tokens map[string]authEntry // token → entry
}

// NewAuthCache creates a new boot-scoped in-memory auth cache.
func NewAuthCache() *AuthCache {
	return &AuthCache{tokens: make(map[string]authEntry)}
}

// Put stores a token with the public key that authorized it and the client
// subject it was issued to. Subjects bind a relay user identity to the
// browser/native client's X25519 public key.
func (c *AuthCache) Put(token string, pubKey []byte, subject string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tokens[token] = authEntry{pubKey: pubKey, subject: subject, createdAt: time.Now()}
}

// Check returns the public key for a valid token. If ttl > 0, expired tokens
// are rejected and removed from the cache. A token is valid only for the
// subject it was issued to. If ttl is 0, tokens never expire.
func (c *AuthCache) Check(token string, ttl time.Duration, subject string) ([]byte, bool) {
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
	if subject == "" || entry.subject != subject {
		return nil, false
	}
	return entry.pubKey, true
}

type challengeEntry struct {
	challenge []byte
	subject   string
	expiresAt time.Time
}

// ChallengeCache stores one-time, wing-generated WebAuthn challenges. Consume
// deletes a challenge even when the supplied subject is wrong, preventing an
// intercepted challenge identifier from becoming an authentication oracle.
type ChallengeCache struct {
	mu         sync.Mutex
	challenges map[string]challengeEntry
}

func NewChallengeCache() *ChallengeCache {
	return &ChallengeCache{challenges: make(map[string]challengeEntry)}
}

// Put creates a one-time challenge bound to subject and returns its opaque ID
// and raw challenge bytes.
func (c *ChallengeCache) Put(subject string, ttl time.Duration) (string, []byte, error) {
	if subject == "" {
		return "", nil, errors.New("empty challenge subject")
	}
	challenge, err := GenerateChallenge()
	if err != nil {
		return "", nil, err
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return "", nil, err
	}
	if ttl <= 0 {
		ttl = time.Minute
	}
	id := hex.EncodeToString(idBytes)
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for key, entry := range c.challenges {
		if now.After(entry.expiresAt) {
			delete(c.challenges, key)
		}
	}
	c.challenges[id] = challengeEntry{
		challenge: append([]byte(nil), challenge...),
		subject:   subject,
		expiresAt: now.Add(ttl),
	}
	return id, challenge, nil
}

func (c *ChallengeCache) Consume(id, subject string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.challenges[id]
	if !ok {
		return nil, false
	}
	delete(c.challenges, id)
	if subject == "" || entry.subject != subject || time.Now().After(entry.expiresAt) {
		return nil, false
	}
	return append([]byte(nil), entry.challenge...), true
}
