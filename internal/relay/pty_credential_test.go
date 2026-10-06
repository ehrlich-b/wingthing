package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/golang-jwt/jwt/v5"
)

func TestPTYSocketCredentialRevalidation(t *testing.T) {
	for _, node := range []string{"login", "edge"} {
		for _, kind := range []string{"cookie", "opaque", "jwt"} {
			for _, change := range []string{"revoke", "expire", "rotate"} {
				if kind == "cookie" && change == "rotate" {
					continue
				}
				for _, trigger := range []string{"idle", "request"} {
					t.Run(node+"/"+kind+"/"+change+"/"+trigger, func(t *testing.T) {
						store := testStore(t)
						mustTest(t, store.CreateUser("user"))
						key, _, err := GenerateECKey()
						mustTest(t, err)
						login := NewServer(store, ServerConfig{InternalSecret: "secret"})
						login.SetJWTKey(key)
						token := "device-token"
						if kind == "cookie" {
							mustTest(t, store.CreateSession(token, "user", time.Now().Add(time.Hour)))
						} else if kind == "jwt" {
							var exp time.Time
							token, exp, err = IssueWingJWT(key, "user", "key", "wing")
							mustTest(t, err)
							mustTest(t, store.CreateDeviceToken(token, "user", "wing", &exp))
						} else {
							mustTest(t, store.CreateDeviceToken(token, "user", "wing", nil))
						}
						server := login
						if node == "edge" {
							loginHTTP := httptest.NewServer(login)
							defer loginHTTP.Close()
							server = NewServer(nil, ServerConfig{NodeRole: "edge", LoginNodeAddr: loginHTTP.URL, InternalSecret: "secret"})
							server.SetJWTKey(key)
							server.SetSessionCache(NewSessionCache("secret"))
						}
						interval := 10 * time.Millisecond
						if trigger == "request" {
							interval = time.Hour
						}
						header := http.Header{"Authorization": {"Bearer " + token}}
						if kind == "cookie" {
							header = http.Header{"Cookie": {sessionCookieNameForChannel() + "=" + token}}
						}
						conn, done := openPTYAuthTestSocket(t, server, interval, header, "")
						switch change {
						case "revoke":
							if kind == "cookie" {
								mustTest(t, store.DeleteSession(token))
							} else {
								mustTest(t, store.DeleteToken(token))
							}
						case "expire":
							table := "device_tokens"
							if kind == "cookie" {
								table = "sessions"
							}
							mustTestExec(t, store.DB(), "UPDATE "+table+" SET expires_at = ? WHERE token = ?", time.Now().Add(-time.Minute).UTC().Format("2006-01-02 15:04:05"), token)
						case "rotate":
							response := httptest.NewRecorder()
							login.handleAuthRefresh(response, httptest.NewRequest(http.MethodPost, "/auth/refresh", strings.NewReader(`{"token":"`+token+`"}`)))
							if response.Code != http.StatusOK {
								t.Fatalf("refresh: %d %s", response.Code, response.Body.String())
							}
						}
						if trigger == "request" {
							mustTest(t, conn.Write(context.Background(), websocket.MessageText, []byte(`{"type":"tunnel.req","wing_id":"wing","request_id":"after-revocation"}`)))
						}
						expectPTYAuthorizationClosed(t, conn, done)
					})
				}
			}
		}
	}
}

func TestPTYSocketClosesAtJWTExpiryEvenWithLiveRow(t *testing.T) {
	server, _ := testServer(t)
	mustTest(t, server.Store.CreateUser("user"))
	exp := time.Now().Add(2 * time.Second)
	token, err := jwt.NewWithClaims(jwt.SigningMethodES256, WingClaims{
		RegisteredClaims: jwt.RegisteredClaims{Subject: "user", ExpiresAt: jwt.NewNumericDate(exp)},
		WingID:           "wing", TokenUse: "wing",
	}).SignedString(server.JWTKey())
	mustTest(t, err)
	// A mismatched legacy row lifetime cannot bypass the signed JWT's expiry.
	mustTest(t, server.Store.CreateDeviceToken(token, "user", "wing", nil))
	conn, done := openPTYAuthTestSocket(t, server, 10*time.Millisecond, nil, "?token="+token)
	expectPTYAuthorizationClosed(t, conn, done)
}

func TestPTYSocketPinsSelectedCredential(t *testing.T) {
	server, _ := testServer(t)
	token, userID := createTestToken(t, server.Store, "wing")
	mustTest(t, server.Store.CreateSession("session", userID, time.Now().Add(time.Hour)))
	// Both credentials are valid, but the cookie is the selected identity.
	conn, done := openPTYAuthTestSocket(t, server, 10*time.Millisecond, http.Header{
		"Cookie":        {sessionCookieNameForChannel() + "=session"},
		"Authorization": {"Bearer " + token},
	}, "")
	mustTest(t, server.Store.DeleteSession("session"))
	expectPTYAuthorizationClosed(t, conn, done)
	if _, _, err := server.Store.ValidateToken(token); err != nil {
		t.Fatal("test must leave the alternative bearer credential valid")
	}
}

func TestPTYAdmissionRejectsRevokedCachedEdgeSession(t *testing.T) {
	store := testStore(t)
	mustTest(t, store.CreateUser("user"))
	mustTest(t, store.CreateSession("session", "user", time.Now().Add(time.Hour)))
	login := NewServer(store, ServerConfig{InternalSecret: "secret"})
	loginHTTP := httptest.NewServer(login)
	defer loginHTTP.Close()
	edge := NewServer(nil, ServerConfig{NodeRole: "edge", LoginNodeAddr: loginHTTP.URL, InternalSecret: "secret"})
	cache := NewSessionCache("secret")
	edge.SetSessionCache(cache)
	if user := cache.Validate("session", loginHTTP.URL); user == nil {
		t.Fatal("test failed to populate edge session cache")
	}
	mustTest(t, store.DeleteSession("session"))
	request := httptest.NewRequest(http.MethodGet, "/ws/pty", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieNameForChannel(), Value: "session"})
	response := httptest.NewRecorder()
	edge.handlePTYWS(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("cached revoked session admitted: %d %s", response.Code, response.Body.String())
	}
}

func TestPTYLocalImplicitIdentityRemainsUsable(t *testing.T) {
	server := NewServer(nil, ServerConfig{})
	server.LocalMode = true
	server.SetLocalUser(&User{ID: "local", Email: strPtr("local@example.com")})
	conn, _ := openPTYAuthTestSocket(t, server, 10*time.Millisecond, nil, "")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	mustTest(t, conn.Write(ctx, websocket.MessageText, []byte(`{"type":"tunnel.req","wing_id":"missing","request_id":"local"}`)))
	_, _, err := conn.Read(ctx)
	mustTest(t, err)
}
