// Package tunnidentity validates the ingress assertion from a tunn app publisher.
package tunnidentity

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const Header = "X-Tunn-Identity"
const maxJWKSBytes = 32768

type Config struct {
	Origin    string
	Issuer    string
	AccountID string
	JWKSFile  string // Public-only JWKS; never a signing keyring.
}

type Verifier struct {
	config Config
	host   string
}

type publicKey struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	Alg string `json:"alg"`
	Use string `json:"use"`
	Kid string `json:"kid"`
	X   string `json:"x"`
	D   string `json:"d"`
}

func New(config Config) (*Verifier, error) {
	origin, err := url.Parse(config.Origin)
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil || origin.Port() != "" || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" || origin.Host != strings.ToLower(origin.Host) {
		return nil, errors.New("tunn origin must be an exact HTTPS hostname without a path or port")
	}
	issuer, err := url.Parse(config.Issuer)
	if err != nil || issuer.Scheme != "https" || issuer.Host == "" || issuer.User != nil || issuer.Path != "" || issuer.RawQuery != "" || issuer.Fragment != "" {
		return nil, errors.New("tunn issuer must be an HTTPS origin")
	}
	account, err := uuid.Parse(config.AccountID)
	if err != nil || account.String() != config.AccountID || account == uuid.Nil || config.JWKSFile == "" {
		return nil, errors.New("tunn requires a canonical owner account UUID and public JWKS file")
	}
	v := &Verifier{config: config, host: origin.Host}
	if _, err := v.keys(); err != nil {
		return nil, err
	}
	return v, nil
}

func (v *Verifier) keys() (map[string]ed25519.PublicKey, error) {
	f, err := os.Open(v.config.JWKSFile)
	if err != nil {
		return nil, errors.New("tunn public JWKS unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxJWKSBytes {
		return nil, errors.New("tunn public JWKS must be a bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxJWKSBytes+1))
	if err != nil || len(data) > maxJWKSBytes {
		return nil, errors.New("tunn public JWKS unavailable")
	}
	var set struct {
		Keys []publicKey `json:"keys"`
	}
	if json.Unmarshal(data, &set) != nil || len(set.Keys) == 0 || len(set.Keys) > 16 {
		return nil, errors.New("invalid tunn public JWKS")
	}
	keys := map[string]ed25519.PublicKey{}
	for _, k := range set.Keys {
		raw, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil || len(raw) != ed25519.PublicKeySize || k.Kty != "OKP" || k.Crv != "Ed25519" || k.Alg != "EdDSA" || k.Use != "sig" || k.Kid == "" || len(k.Kid) > 128 || k.D != "" || keys[k.Kid] != nil {
			return nil, errors.New("invalid or non-public tunn JWKS")
		}
		keys[k.Kid] = ed25519.PublicKey(raw)
	}
	return keys, nil
}

// Verify admits only the configured owner, not any account with a similar email.
// Reloading the public file on each request makes operator key rotation immediate.
func (v *Verifier) Verify(r *http.Request) error {
	if r.Host != v.host || len(r.Header.Values(Header)) != 1 || len(r.Header.Get(Header)) > 8192 {
		return errors.New("tunn ingress identity required")
	}
	raw := r.Header.Get(Header)
	if raw == "" {
		return errors.New("tunn ingress identity required")
	}
	origin := r.Header.Get("Origin")
	if origin != "" && origin != v.config.Origin {
		return errors.New("tunn browser origin rejected")
	}
	// Fetch metadata is advisory defense in depth; unsigned claims never grant access.
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return errors.New("tunn cross-site request rejected")
	}
	keys, err := v.keys()
	if err != nil {
		return err
	}
	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		if t.Method != jwt.SigningMethodEdDSA || keys[kid] == nil {
			return nil, errors.New("unknown tunn signing key")
		}
		return keys[kid], nil
	}, jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithIssuer(v.config.Issuer), jwt.WithAudience(v.host), jwt.WithExpirationRequired(), jwt.WithIssuedAt())
	if err != nil || !token.Valid {
		return errors.New("invalid tunn ingress identity")
	}
	issued, _ := claims.GetIssuedAt()
	expires, _ := claims.GetExpirationTime()
	notBefore, _ := claims.GetNotBefore()
	subject, _ := claims.GetSubject()
	audience, _ := claims.GetAudience()
	email, _ := claims["email"].(string)
	provider, _ := claims["provider"].(string)
	if subject != v.config.AccountID || claims["account_id"] != subject || claims["email_verified"] != true || !strings.Contains(email, "@") || provider == "" || len(audience) != 1 || issued == nil || notBefore == nil || expires == nil || expires.Time.Sub(issued.Time) <= 0 || expires.Time.Sub(issued.Time) > 120*time.Second || notBefore.Time.Before(issued.Time) || issued.Time.After(time.Now()) {
		return errors.New("tunn owner binding rejected")
	}
	return nil
}
