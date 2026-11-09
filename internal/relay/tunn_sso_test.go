package relay

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/testsocket"
	"github.com/ehrlich-b/wingthing/internal/tunnidentity"
	"github.com/golang-jwt/jwt/v5"
)

func TestTunnSSOHTTPIngressPreservesLocalGateAndOwner(t *testing.T) {
	store := testStore(t)
	srv := NewServer(store, ServerConfig{BaseURL: "https://phone.example"})
	srv.LocalMode = true
	user, _, err := store.CreateLocalUser()
	if err != nil {
		t.Fatal(err)
	}
	srv.SetLocalUser(user)
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(testsocket.Dir(t), "public.json")
	data, _ := json.Marshal(map[string]any{"keys": []map[string]string{{"kty": "OKP", "crv": "Ed25519", "alg": "EdDSA", "use": "sig", "kid": "fixture", "x": base64.RawURLEncoding.EncodeToString(pub)}}})
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	const account = "b56ce660-7801-49f2-bfcc-168392716895"
	now := time.Now().Unix()
	c := jwt.MapClaims{"iss": "https://auth.example", "aud": "phone.example", "sub": account, "account_id": account, "email": "owner@example.com", "email_verified": true, "provider": "github", "iat": now, "nbf": now, "exp": now + 120}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, c)
	token.Header["kid"] = "fixture"
	raw, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	call := func(host, path, assertion, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "http://"+host+path, nil)
		r.Header.Set(tunnidentity.Header, assertion)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}
	if got := call("phone.example", "/api/app/me", raw, "").Code; got != 403 {
		t.Fatalf("default hostile Host bypassed: %d", got)
	}
	srv.TunnSSO, err = tunnidentity.New(tunnidentity.Config{Origin: "https://phone.example", Issuer: "https://auth.example", AccountID: account, JWKSFile: file})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/app/", "/app/sw.js", "/api/app/me", "/api/app/wings", "/ws/app"} {
		if got := call("phone.example", path, "", "").Code; got != http.StatusForbidden {
			t.Fatalf("anonymous %s = %d", path, got)
		}
		if got := call("phone.example", path, raw, "https://evil.example").Code; got != http.StatusForbidden {
			t.Fatalf("foreign origin %s = %d", path, got)
		}
	}
	w := call("phone.example", "/api/app/me", raw, "https://phone.example")
	if w.Code != 200 {
		t.Fatalf("owner response %d", w.Code)
	}
	var me map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &me); err != nil {
		t.Fatal(err)
	}
	if me["id"] != user.ID {
		t.Fatal("SSO changed the wing's execution owner")
	}
	if w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("SSO response can be cached")
	}
	if got := call("localhost", "/api/app/me", "", "").Code; got != 200 {
		t.Fatalf("local wing contract changed: %d", got)
	}
	if got := call("other.example", "/api/app/me", raw, "").Code; got != 403 {
		t.Fatal("sibling host admitted")
	}
}
