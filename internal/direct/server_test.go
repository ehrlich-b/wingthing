package direct

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestValidateHandoffJWTRequiresDedicatedTokenUse(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	issue := func(tokenUse string) string {
		t.Helper()
		claims := HandoffClaims{
			RegisteredClaims: jwt.RegisteredClaims{
				Subject: "alice", ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
			},
			TokenUse: tokenUse,
		}
		token, err := jwt.NewWithClaims(jwt.SigningMethodES256, claims).SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}

	if _, err := validateHandoffJWT(&key.PublicKey, issue("handoff")); err != nil {
		t.Fatalf("valid handoff token rejected: %v", err)
	}
	if _, err := validateHandoffJWT(&key.PublicKey, issue("mcp")); err == nil {
		t.Fatal("MCP token use accepted as a direct-mode handoff token")
	}
}
