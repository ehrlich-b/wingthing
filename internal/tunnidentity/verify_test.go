package tunnidentity

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/testsocket"
	"github.com/golang-jwt/jwt/v5"
)

const owner = "b56ce660-7801-49f2-bfcc-168392716895"

func fixture(t *testing.T) (*Verifier, ed25519.PrivateKey, string) {
	t.Helper()
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(testsocket.Dir(t), "public.json")
	data, _ := json.Marshal(map[string]any{"keys": []publicKey{{Kty: "OKP", Crv: "Ed25519", Alg: "EdDSA", Use: "sig", Kid: "fixture", X: base64.RawURLEncoding.EncodeToString(pub)}}})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	v, err := New(Config{Origin: "https://phone.example", Issuer: "https://auth.example", AccountID: owner, JWKSFile: path})
	if err != nil {
		t.Fatal(err)
	}
	return v, private, path
}
func claims() jwt.MapClaims {
	now := time.Now().Unix()
	return jwt.MapClaims{"iss": "https://auth.example", "aud": "phone.example", "sub": owner, "account_id": owner, "email": "owner@example.com", "email_verified": true, "provider": "github", "iat": now, "nbf": now, "exp": now + 120}
}
func signed(t *testing.T, private ed25519.PrivateKey, c jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, c)
	token.Header["kid"] = "fixture"
	raw, err := token.SignedString(private)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestTunnOwnerAssertionBoundaries(t *testing.T) {
	v, key, _ := fixture(t)
	for _, tc := range []struct {
		name  string
		field string
		value any
	}{
		{"issuer", "iss", "https://attacker.example"}, {"sibling host", "aud", "sibling.example"},
		{"shared account is not owner", "sub", "c0c0a576-b7a9-4d20-aa79-64c2649e0408"},
		{"unbound UUID", "account_id", "c0c0a576-b7a9-4d20-aa79-64c2649e0408"},
		{"unverified email", "email_verified", false}, {"expired", "exp", time.Now().Unix() - 1},
		{"excess lifetime", "exp", time.Now().Unix() + 121}, {"future issue", "iat", time.Now().Unix() + 1},
		{"missing issued", "iat", nil}, {"missing expiry", "exp", nil}, {"missing not-before", "nbf", nil},
		{"multi audience", "aud", []string{"phone.example", "sibling.example"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := claims()
			c[tc.field] = tc.value
			r := httptest.NewRequest("GET", "https://phone.example/api/app/me", nil)
			r.Header.Set(Header, signed(t, key, c))
			if v.Verify(r) == nil {
				t.Fatal("accepted invalid binding")
			}
		})
	}
	r := httptest.NewRequest("GET", "https://phone.example/api/app/me", nil)
	r.Header.Set(Header, signed(t, key, claims()))
	if err := v.Verify(r); err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{"https://evil.example", "null", "http://phone.example"} {
		r.Header.Set("Origin", origin)
		if v.Verify(r) == nil {
			t.Fatal("accepted foreign origin")
		}
	}
	r.Header.Set("Origin", "https://phone.example")
	if err := v.Verify(r); err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	if v.Verify(r) == nil {
		t.Fatal("accepted cross site")
	}
	r.Header.Del("Sec-Fetch-Site")
	r.Header.Add(Header, r.Header.Get(Header))
	if v.Verify(r) == nil {
		t.Fatal("accepted duplicate identity")
	}
}
func TestTunnPublicRotationAndFailClosed(t *testing.T) {
	v, key, path := fixture(t)
	r := httptest.NewRequest("GET", "https://phone.example/app/", nil)
	if v.Verify(r) == nil {
		t.Fatal("anonymous admitted")
	}
	r.Header.Set("X-Tunn-User-Email", "owner@example.com")
	if v.Verify(r) == nil {
		t.Fatal("unsigned identity admitted")
	}
	r.Header.Set(Header, signed(t, key, claims()))
	other, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"keys": []publicKey{{Kty: "OKP", Crv: "Ed25519", Alg: "EdDSA", Use: "sig", Kid: "fixture", X: base64.RawURLEncoding.EncodeToString(other)}}})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if v.Verify(r) == nil {
		t.Fatal("removed signing key remained trusted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if v.Verify(r) == nil {
		t.Fatal("JWKS outage admitted")
	}
}
func TestTunnConfigurationRejectsNonPublicAndAmbiguousInputs(t *testing.T) {
	v, _, path := fixture(t)
	for _, origin := range []string{"http://phone.example", "https://phone.example/app/", "https://user@phone.example", "https://phone.example:443", "https://phone.example?x=1", "https://Phone.example"} {
		c := v.config
		c.Origin = origin
		if _, err := New(c); err == nil {
			t.Fatal("invalid origin admitted")
		}
	}
	data, _ := json.Marshal(map[string]any{"keys": []publicKey{{Kty: "OKP", Crv: "Ed25519", Alg: "EdDSA", Use: "sig", Kid: "fixture", X: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), D: "not-public"}}})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(v.config); err == nil {
		t.Fatal("private keyring accepted")
	}
}
