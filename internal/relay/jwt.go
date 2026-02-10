package relay

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const jwtSecretKey = "jwt_secret"

// WingClaims are the JWT claims for a wing connection.
type WingClaims struct {
	jwt.RegisteredClaims
	PublicKey string `json:"pub,omitempty"`
	MachineID string `json:"machine,omitempty"`
}

// GenerateOrLoadSecret returns the JWT signing secret.
// Priority: envSecret (from WT_JWT_SECRET) > relay_config DB > auto-generate.
func GenerateOrLoadSecret(store *RelayStore, envSecret string) ([]byte, error) {
	if envSecret != "" {
		return base64.StdEncoding.DecodeString(envSecret)
	}

	val, err := store.GetRelayConfig(jwtSecretKey)
	if err != nil {
		return nil, err
	}
	if val != "" {
		return base64.StdEncoding.DecodeString(val)
	}

	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("generate jwt secret: %w", err)
	}

	encoded := base64.StdEncoding.EncodeToString(secret)
	if err := store.SetRelayConfig(jwtSecretKey, encoded); err != nil {
		return nil, err
	}
	return secret, nil
}

// IssueWingJWT creates a signed JWT for a wing connection.
func IssueWingJWT(secret []byte, userID, publicKey, machineID string) (string, time.Time, error) {
	exp := time.Now().Add(365 * 24 * time.Hour)
	claims := WingClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(exp),
		},
		PublicKey: publicKey,
		MachineID: machineID,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign jwt: %w", err)
	}
	return signed, exp, nil
}

// ValidateWingJWT verifies a JWT and returns the claims.
func ValidateWingJWT(secret []byte, tokenString string) (*WingClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &WingClaims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return secret, nil
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
