package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func signedAssertion(t *testing.T, challenge []byte, rpID, origin string, flags byte) ([]byte, []byte, []byte, []byte) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := make([]byte, 64)
	privateKey.X.FillBytes(publicKey[:32])
	privateKey.Y.FillBytes(publicKey[32:])

	clientData, err := json.Marshal(map[string]any{
		"type":        "webauthn.get",
		"challenge":   base64.RawURLEncoding.EncodeToString(challenge),
		"origin":      origin,
		"crossOrigin": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	authenticatorData := make([]byte, 37)
	rpHash := sha256.Sum256([]byte(rpID))
	copy(authenticatorData[:32], rpHash[:])
	authenticatorData[32] = flags

	clientHash := sha256.Sum256(clientData)
	signedData := append(append([]byte{}, authenticatorData...), clientHash[:]...)
	digest := sha256.Sum256(signedData)
	signature, err := ecdsa.SignASN1(rand.Reader, privateKey, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return publicKey, authenticatorData, clientData, signature
}

func TestVerifyPasskeyAssertionChecksWebAuthnContext(t *testing.T) {
	challenge := []byte("0123456789abcdef0123456789abcdef")
	key, authenticatorData, clientData, signature := signedAssertion(
		t, challenge, "wingthing.ai", "https://app.wingthing.ai", 0x01|0x04,
	)
	policy := PasskeyPolicy{
		RPID:                    "wingthing.ai",
		Origins:                 []string{"https://app.wingthing.ai"},
		RequireUserVerification: true,
	}
	if err := VerifyPasskeyAssertion(key, challenge, authenticatorData, clientData, signature, policy); err != nil {
		t.Fatalf("valid assertion rejected: %v", err)
	}

	wrongOrigin := policy
	wrongOrigin.Origins = []string{"https://attacker.example"}
	if err := VerifyPasskeyAssertion(key, challenge, authenticatorData, clientData, signature, wrongOrigin); err == nil {
		t.Fatal("expected origin mismatch")
	}

	wrongRP := policy
	wrongRP.RPID = "attacker.example"
	if err := VerifyPasskeyAssertion(key, challenge, authenticatorData, clientData, signature, wrongRP); err == nil {
		t.Fatal("expected relying-party mismatch")
	}

	withoutUV := append([]byte(nil), authenticatorData...)
	withoutUV[32] = 0x01
	if err := VerifyPasskeyAssertion(key, challenge, withoutUV, clientData, signature, policy); err == nil {
		t.Fatal("expected user-verification rejection")
	}
}

func TestAuthCacheBindsTokenToSubject(t *testing.T) {
	cache := NewAuthCache()
	cache.Put("token", []byte("key"), "user-1\x00client-key-1")
	if _, ok := cache.Check("token", 0, "user-1\x00client-key-1"); !ok {
		t.Fatal("token should be valid for its subject")
	}
	if _, ok := cache.Check("token", 0, "user-1\x00client-key-2"); ok {
		t.Fatal("token must not move to another client key")
	}
	if _, ok := cache.Check("token", 0, "user-2\x00client-key-1"); ok {
		t.Fatal("token must not move to another user")
	}
}

func TestAuthCacheIsBoundedAndIsolatesPublicKeys(t *testing.T) {
	cache := NewAuthCache()
	key := []byte("key")
	for index := 0; index < maxAuthCacheEntries; index++ {
		cache.Put(fmt.Sprintf("token-%d", index), key, "subject")
	}
	key[0] = 'X'
	cache.Put("overflow", []byte("new"), "subject")

	if _, ok := cache.Check("token-0", 0, "subject"); ok {
		t.Fatal("oldest auth token was not evicted")
	}
	got, ok := cache.Check("token-1", 0, "subject")
	if !ok || string(got) != "key" {
		t.Fatalf("cached key = %q, valid = %v", got, ok)
	}
	got[0] = 'Y'
	again, ok := cache.Check("token-1", 0, "subject")
	if !ok || string(again) != "key" {
		t.Fatalf("caller mutated cached key: %q, valid = %v", again, ok)
	}
	if got := len(cache.tokens); got != maxAuthCacheEntries {
		t.Fatalf("auth cache entries = %d, want %d", got, maxAuthCacheEntries)
	}
}

func TestChallengeCacheIsBoundAndOneTime(t *testing.T) {
	cache := NewChallengeCache()
	id, challenge, err := cache.Put("user-1\x00client-key-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(challenge) != 32 {
		t.Fatalf("challenge length = %d, want 32", len(challenge))
	}
	if _, ok := cache.Consume(id, "user-1\x00client-key-2"); ok {
		t.Fatal("challenge must not move to another subject")
	}
	if _, ok := cache.Consume(id, "user-1\x00client-key-1"); ok {
		t.Fatal("failed subject check must still consume the challenge")
	}

	id, challenge, err = cache.Put("user-1\x00client-key-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := cache.Consume(id, "user-1\x00client-key-1")
	if !ok || string(got) != string(challenge) {
		t.Fatal("valid challenge was not returned")
	}
	if _, ok := cache.Consume(id, "user-1\x00client-key-1"); ok {
		t.Fatal("challenge replay must fail")
	}
}

func TestChallengeCacheIsBoundedAndEvictsOldestChallenge(t *testing.T) {
	cache := NewChallengeCache()
	firstID := ""
	for index := 0; index <= maxChallengeCacheEntries; index++ {
		id, _, err := cache.Put("subject", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			firstID = id
		}
	}
	if _, ok := cache.Consume(firstID, "subject"); ok {
		t.Fatal("oldest challenge was not evicted")
	}
	if got := len(cache.challenges); got != maxChallengeCacheEntries {
		t.Fatalf("challenge cache entries = %d, want %d", got, maxChallengeCacheEntries)
	}
}
