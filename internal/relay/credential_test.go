package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestRotatedJWTRejectedOnEveryAuthSurface(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "current"
		if legacy {
			name = "legacy-without-jti"
		}
		t.Run(name, func(t *testing.T) {
			store := testStore(t)
			mustTest(t, store.CreateUser("user"))
			key, _, err := GenerateECKey()
			mustTest(t, err)
			login := NewServer(store, ServerConfig{InternalSecret: "secret"})
			login.SetJWTKey(key)
			old, exp, err := IssueWingJWT(key, "user", "public-key", "wing")
			mustTest(t, err)
			if legacy {
				// Old releases had neither jti nor token_use and a one-year lifetime.
				exp = time.Now().Add(365 * 24 * time.Hour)
				old, err = jwt.NewWithClaims(jwt.SigningMethodES256, WingClaims{
					RegisteredClaims: jwt.RegisteredClaims{Subject: "user", ExpiresAt: jwt.NewNumericDate(exp)},
					WingID:           "wing", PublicKey: "public-key",
				}).SignedString(key)
				mustTest(t, err)
			}
			mustTest(t, store.CreateDeviceToken(old, "user", "wing", &exp))
			loginHTTP := httptest.NewServer(login)
			defer loginHTTP.Close()
			edge := NewServer(nil, ServerConfig{NodeRole: "edge", LoginNodeAddr: loginHTTP.URL, InternalSecret: "secret"})
			edge.SetJWTKey(key)
			for _, server := range []*Server{login, edge} {
				if claims, err := server.validateWingCredential(context.Background(), old); err != nil || claims.Subject != "user" {
					t.Fatalf("active JWT rejected: %v", err)
				}
			}
			response := httptest.NewRecorder()
			login.handleAuthRefresh(response, httptest.NewRequest(http.MethodPost, "/auth/refresh", strings.NewReader(`{"token":"`+old+`"}`)))
			if response.Code != http.StatusOK {
				t.Fatalf("refresh: %d %s", response.Code, response.Body.String())
			}
			var replacement struct {
				Token     string `json:"token"`
				ExpiresAt int64  `json:"expires_at"`
			}
			decodeTestJSON(t, response.Body, &replacement)
			if replacement.Token == old || replacement.ExpiresAt <= time.Now().Unix() || replacement.ExpiresAt > time.Now().Add(wingTokenTTL).Unix() {
				t.Fatalf("invalid replacement expiry: %#v", replacement)
			}
			claims, err := ValidateWingJWT(&key.PublicKey, replacement.Token)
			mustTest(t, err)
			if claims.PublicKey != "public-key" || claims.WingID != "wing" {
				t.Fatalf("refresh lost wing binding: %#v", claims)
			}
			for node, server := range map[string]*Server{"login": login, "edge": edge} {
				for surface, handler := range map[string]http.HandlerFunc{
					"http": func(w http.ResponseWriter, r *http.Request) { server.requireToken(w, r) },
					"pty":  server.handlePTYWS,
					"wing": server.handleWingWS,
				} {
					t.Run(node+"/"+surface, func(t *testing.T) {
						recorder := httptest.NewRecorder()
						request := httptest.NewRequest(http.MethodGet, "/?token="+old, nil)
						handler(recorder, request)
						if recorder.Code != http.StatusUnauthorized {
							t.Fatalf("rotated JWT accepted: %d %s", recorder.Code, recorder.Body.String())
						}
					})
				}
				if _, err := server.validateWingCredential(context.Background(), replacement.Token); err != nil {
					t.Fatalf("replacement rejected by %s: %v", node, err)
				}
			}
			mustTest(t, store.DeleteToken(replacement.Token))
			if _, err := edge.validateWingCredential(context.Background(), replacement.Token); err == nil {
				t.Fatal("deleted JWT remained accepted on edge")
			}
		})
	}
}

func TestWingCredentialRejectsExpiredJWTWithLiveRow(t *testing.T) {
	store := testStore(t)
	mustTest(t, store.CreateUser("user"))
	key, _, err := GenerateECKey()
	mustTest(t, err)
	server := NewServer(store, ServerConfig{})
	server.SetJWTKey(key)
	token, err := jwt.NewWithClaims(jwt.SigningMethodES256, WingClaims{
		RegisteredClaims: jwt.RegisteredClaims{Subject: "user", ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Minute))},
		WingID:           "wing", TokenUse: "wing",
	}).SignedString(key)
	mustTest(t, err)
	mustTest(t, store.CreateDeviceToken(token, "user", "wing", nil))
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/?token="+token, nil)
	if user := server.requireToken(response, request); user != "" || response.Code != http.StatusUnauthorized {
		t.Fatalf("expired JWT fell back to live DB row: user=%q status=%d", user, response.Code)
	}
}

func TestOpaqueTokenRefreshMigratesToExpiringJWT(t *testing.T) {
	server, _ := testServer(t)
	old, userID := createTestToken(t, server.Store, "wing")
	response := httptest.NewRecorder()
	server.handleAuthRefresh(response, httptest.NewRequest(http.MethodPost, "/auth/refresh", strings.NewReader(`{"token":"`+old+`"}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("refresh: %d %s", response.Code, response.Body.String())
	}
	var result struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
	}
	decodeTestJSON(t, response.Body, &result)
	claims, err := server.validateWingCredential(context.Background(), result.Token)
	if err != nil || claims.Subject != userID || claims.WingID != "wing" || result.ExpiresAt == 0 {
		t.Fatalf("opaque refresh failed: %#v %v", result, err)
	}
	if _, _, err := server.Store.ValidateToken(old); err == nil {
		t.Fatal("opaque token survived rotation")
	}
}

func TestWingAdmissionDistinguishesValidationOutagesFromRejection(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
		want   int
	}{
		{"unreachable", 0, "", http.StatusServiceUnavailable},
		{"unavailable", http.StatusServiceUnavailable, "upstream unavailable", http.StatusServiceUnavailable},
		{"internal error", http.StatusInternalServerError, "failed", http.StatusServiceUnavailable},
		{"missing endpoint", http.StatusNotFound, "not found", http.StatusServiceUnavailable},
		{"malformed response", http.StatusOK, "{", http.StatusServiceUnavailable},
		{"missing identity", http.StatusOK, "{}", http.StatusServiceUnavailable},
		{"oversized response", http.StatusOK, strings.Repeat("x", maxSessionValidationBytes+1), http.StatusServiceUnavailable},
		{"unauthorized", http.StatusUnauthorized, "invalid token", http.StatusUnauthorized},
		{"forbidden", http.StatusForbidden, "account rejected", http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer login.Close()
			if test.status == 0 {
				login.Close()
			}
			edge := NewServer(nil, ServerConfig{NodeRole: "edge", LoginNodeAddr: login.URL, InternalSecret: "secret"})
			response := httptest.NewRecorder()
			edge.handleWingWS(response, httptest.NewRequest(http.MethodGet, "/ws/wing?token=device-token", nil))
			if response.Code != test.want {
				t.Fatalf("wing admission = %d %s, want %d", response.Code, response.Body.String(), test.want)
			}
		})
	}
}
