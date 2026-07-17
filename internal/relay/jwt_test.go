package relay

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestValidateWingJWTAcceptsLegacyTokenUseOnlyForWing(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	claims := WingClaims{RegisteredClaims: jwt.RegisteredClaims{
		Subject: "user", ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}, WingID: "wing-1"}
	token, err := jwt.NewWithClaims(jwt.SigningMethodES256, claims).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateWingJWT(&key.PublicKey, token); err != nil {
		t.Fatalf("legacy wing token rejected: %v", err)
	}
	if _, err := ValidateMCPJWT(&key.PublicKey, token, "https://wing.example", "https://wing.example/mcp"); err == nil {
		t.Fatal("legacy wing token accepted as MCP")
	}
}

func TestParseECKeyRejectsNonP256Curve(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseECKeyFromEnv(base64.StdEncoding.EncodeToString(der)); err == nil {
		t.Fatal("P-384 key accepted for ES256")
	}
}

func TestDeriveECKeyStringFromSecretIsStableAndDomainSafe(t *testing.T) {
	first, err := DeriveECKeyStringFromSecret("0123456789abcdef-existing-secret")
	if err != nil {
		t.Fatal(err)
	}
	again, err := DeriveECKeyStringFromSecret("0123456789abcdef-existing-secret")
	if err != nil {
		t.Fatal(err)
	}
	other, err := DeriveECKeyStringFromSecret("fedcba9876543210-different-secret")
	if err != nil {
		t.Fatal(err)
	}
	if first != again {
		t.Fatal("same secret derived different signing keys")
	}
	if first == other {
		t.Fatal("different secrets derived the same signing key")
	}
	key, err := ParseECKeyFromEnv(first)
	if err != nil {
		t.Fatalf("derived key is not a valid P-256 private key: %v", err)
	}
	if key.Curve != elliptic.P256() || key.D.Sign() <= 0 {
		t.Fatal("derived key has invalid P-256 parameters")
	}
	if _, err := DeriveECKeyStringFromSecret("too-short"); err == nil {
		t.Fatal("short deployment secret was accepted")
	}
}
