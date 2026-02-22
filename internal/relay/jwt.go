package relay

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// WingClaims are the JWT claims for a wing connection.
type WingClaims struct {
	jwt.RegisteredClaims
	PublicKey string `json:"pub,omitempty"`
	WingID    string `json:"wing,omitempty"`
}

// HandoffClaims are short-lived JWT claims for browser direct-mode connections.
type HandoffClaims struct {
	jwt.RegisteredClaims
	Email   string `json:"email,omitempty"`
	OrgRole string `json:"org_role,omitempty"`
}

// ParseECKeyFromEnv parses a P-256 private key from an environment variable value.
// Accepts PEM or base64-encoded DER. Returns an error if the value is empty or invalid.
func ParseECKeyFromEnv(envValue string) (*ecdsa.PrivateKey, error) {
	if envValue == "" {
		return nil, fmt.Errorf("WT_JWT_KEY is required — generate with: wt keygen")
	}
	return parseECKey(envValue)
}

// GenerateECKey creates a new P-256 private key and returns it along with
// its base64-DER encoding (suitable for storing in wing.yaml).
func GenerateECKey() (*ecdsa.PrivateKey, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, "", fmt.Errorf("generate ec key: %w", err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, "", fmt.Errorf("marshal ec key: %w", err)
	}
	return key, base64.StdEncoding.EncodeToString(der), nil
}

// parseECKey parses a P-256 private key from PEM or base64-encoded DER.
func parseECKey(data string) (*ecdsa.PrivateKey, error) {
	// Try PEM first
	block, _ := pem.Decode([]byte(data))
	if block != nil {
		key, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse pem ec key: %w", err)
		}
		return key, nil
	}

	// Try base64-encoded DER
	der, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return nil, fmt.Errorf("decode base64 ec key: %w", err)
	}
	key, err := x509.ParseECPrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("parse der ec key: %w", err)
	}
	return key, nil
}

// IssueWingJWT creates an ES256-signed JWT for a wing connection.
func IssueWingJWT(key *ecdsa.PrivateKey, userID, publicKey, wingID string) (string, time.Time, error) {
	exp := time.Now().Add(365 * 24 * time.Hour)
	claims := WingClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(exp),
		},
		PublicKey: publicKey,
		WingID:    wingID,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	signed, err := token.SignedString(key)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign jwt: %w", err)
	}
	return signed, exp, nil
}

// ValidateWingJWT verifies an ES256 JWT and returns the claims.
func ValidateWingJWT(pubKey *ecdsa.PublicKey, tokenString string) (*WingClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &WingClaims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodECDSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return pubKey, nil
	})
	if err != nil {
		return nil, fmt.Errorf("parse jwt: %w", err)
	}

	claims, ok := token.Claims.(*WingClaims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid jwt claims")
	}
	return claims, nil
}

// IssueHandoffJWT creates a short-lived ES256 JWT for browser direct-mode connections.
func IssueHandoffJWT(key *ecdsa.PrivateKey, userID, email, orgRole string) (string, error) {
	claims := HandoffClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
		},
		Email:   email,
		OrgRole: orgRole,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	signed, err := token.SignedString(key)
	if err != nil {
		return "", fmt.Errorf("sign handoff jwt: %w", err)
	}
	return signed, nil
}

// MarshalECPublicKey returns the base64-encoded DER form of an ECDSA public key.
func MarshalECPublicKey(pub *ecdsa.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", fmt.Errorf("marshal ec public key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(der), nil
}

// ParseECPublicKey parses a base64-encoded DER ECDSA public key.
func ParseECPublicKey(data string) (*ecdsa.PublicKey, error) {
	der, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return nil, fmt.Errorf("decode base64 ec public key: %w", err)
	}
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("parse ec public key: %w", err)
	}
	ecPub, ok := pub.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("key is not ECDSA P-256")
	}
	return ecPub, nil
}
