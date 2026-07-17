package relay

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// WingClaims are the JWT claims for a wing connection.
type WingClaims struct {
	jwt.RegisteredClaims
	PublicKey string `json:"pub,omitempty"`
	WingID    string `json:"wing,omitempty"`
	TokenUse  string `json:"token_use"`
}

// HandoffClaims are short-lived JWT claims for browser direct-mode connections.
type HandoffClaims struct {
	jwt.RegisteredClaims
	Email    string `json:"email,omitempty"`
	OrgRole  string `json:"org_role,omitempty"`
	TokenUse string `json:"token_use"`
}

// MCPClaims are deliberately distinct from wing connection credentials. Audience binds the
// token to one MCP resource, while TokenUse prevents another JWT class from crossing surfaces.
type MCPClaims struct {
	jwt.RegisteredClaims
	TokenUse string `json:"token_use"`
	ClientID string `json:"client_id"`
}

const mcpAccessTokenTTL = time.Hour

const jwtSecretDerivationContext = "wingthing/jwt-signing-key/es256/v1"

// DeriveECKeyStringFromSecret deterministically derives a P-256 signing key from an existing
// high-entropy deployment secret. Rejection sampling avoids modulo bias, while the context
// string domain-separates this key from any other use of the same secret.
func DeriveECKeyStringFromSecret(secret string) (string, error) {
	if len(secret) < 16 {
		return "", fmt.Errorf("WT_JWT_SECRET must contain at least 16 bytes")
	}
	curve := elliptic.P256()
	var d *big.Int
	for counter := 0; counter < 256; counter++ {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(jwtSecretDerivationContext))
		mac.Write([]byte{byte(counter)})
		candidate := new(big.Int).SetBytes(mac.Sum(nil))
		if candidate.Sign() > 0 && candidate.Cmp(curve.Params().N) < 0 {
			d = candidate
			break
		}
	}
	if d == nil {
		return "", fmt.Errorf("derive P-256 key from WT_JWT_SECRET")
	}
	x, y := curve.ScalarBaseMult(d.Bytes())
	key := &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{Curve: curve, X: x, Y: y},
		D:         d,
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", fmt.Errorf("marshal derived EC key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(der), nil
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
		if key.Curve != elliptic.P256() {
			return nil, fmt.Errorf("EC private key must use P-256")
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
	if key.Curve != elliptic.P256() {
		return nil, fmt.Errorf("EC private key must use P-256")
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
		TokenUse:  "wing",
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
		return pubKey, nil
	}, jwt.WithValidMethods([]string{"ES256"}), jwt.WithExpirationRequired(), jwt.WithIssuedAt())
	if err != nil {
		return nil, fmt.Errorf("parse jwt: %w", err)
	}

	claims, ok := token.Claims.(*WingClaims)
	// Empty token_use is accepted only for wing credentials issued before token classes were
	// separated. New wing tokens are explicit; MCP and handoff validators never accept empty.
	if !ok || !token.Valid || (claims.TokenUse != "wing" && claims.TokenUse != "") || claims.Subject == "" || claims.WingID == "" {
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
		Email:    email,
		OrgRole:  orgRole,
		TokenUse: "handoff",
	}

	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	signed, err := token.SignedString(key)
	if err != nil {
		return "", fmt.Errorf("sign handoff jwt: %w", err)
	}
	return signed, nil
}

// IssueMCPJWT mints a short-lived bearer token for exactly one MCP resource and client.
func IssueMCPJWT(key *ecdsa.PrivateKey, userID, issuer, resource, clientID string) (string, time.Time, error) {
	now := time.Now()
	exp := now.Add(mcpAccessTokenTTL)
	claims := MCPClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    issuer,
			Subject:   userID,
			Audience:  jwt.ClaimStrings{resource},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
		},
		TokenUse: "mcp",
		ClientID: clientID,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	signed, err := token.SignedString(key)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign mcp jwt: %w", err)
	}
	return signed, exp, nil
}

// ValidateMCPJWT accepts only an ES256 MCP token issued for this authorization server and
// audience. General wing, handoff, and database tokens are not valid MCP credentials.
func ValidateMCPJWT(pubKey *ecdsa.PublicKey, tokenString, issuer, resource string) (*MCPClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &MCPClaims{}, func(t *jwt.Token) (any, error) {
		return pubKey, nil
	},
		jwt.WithValidMethods([]string{"ES256"}),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithIssuer(issuer),
		jwt.WithAudience(resource),
	)
	if err != nil {
		return nil, fmt.Errorf("parse mcp jwt: %w", err)
	}
	claims, ok := token.Claims.(*MCPClaims)
	if !ok || !token.Valid || claims.TokenUse != "mcp" || claims.Subject == "" || claims.ClientID == "" {
		return nil, fmt.Errorf("invalid mcp jwt claims")
	}
	return claims, nil
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
	if !ok || ecPub.Curve != elliptic.P256() {
		return nil, fmt.Errorf("key is not ECDSA P-256")
	}
	return ecPub, nil
}
