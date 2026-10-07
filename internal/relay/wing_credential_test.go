package relay

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/golang-jwt/jwt/v5"

	"github.com/ehrlich-b/wingthing/internal/ws"
)

func openWingAuthTestSocket(t *testing.T, server *Server, token, wingID string, interval time.Duration, orgRefs ...string) (*websocket.Conn, <-chan struct{}) {
	t.Helper()
	done := make(chan struct{})
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		server.handleWingWSWithAuthInterval(w, r, interval)
	}))
	t.Cleanup(httpServer.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, response, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(httpServer.URL, "http")+"/ws/wing", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	if err != nil {
		t.Fatalf("wing handshake: response=%v err=%v", response, err)
	}
	t.Cleanup(func() {
		_ = conn.CloseNow()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("wing socket handler did not finish cleanup")
		}
	})
	reg := ws.WingRegister{Type: ws.TypeWingRegister, WingID: wingID}
	if len(orgRefs) > 0 {
		reg.OrgSlug = orgRefs[0]
	}
	mustTest(t, wsjson.Write(ctx, conn, reg))
	var ack ws.RegisteredMsg
	mustTest(t, wsjson.Read(ctx, conn, &ack))
	if ack.Type != ws.TypeRegistered {
		t.Fatalf("wing registration = %#v", ack)
	}
	return conn, done
}

func TestWingSocketRevalidatesRegisteredOrgMembership(t *testing.T) {
	for _, node := range []string{"login", "edge", "roost"} {
		t.Run(node, func(t *testing.T) {
			store := testStore(t)
			mustTest(t, store.CreateUser("owner"))
			mustTest(t, store.CreateUser("member"))
			mustTest(t, store.CreateOrgWithSeats("org-id", "Org", "org-slug", "owner", 2))
			mustTest(t, store.AddOrgMember("org-id", "member", "admin"))
			mustTest(t, store.CreateDeviceToken("token", "member", "wing", nil))
			login := NewServer(store, ServerConfig{InternalSecret: "secret"})
			server := login
			if node == "edge" {
				loginHTTP := httptest.NewServer(login)
				defer loginHTTP.Close()
				server = NewServer(nil, ServerConfig{NodeRole: "edge", LoginNodeAddr: loginHTTP.URL, InternalSecret: "secret"})
			} else if node == "roost" {
				server.RoostMode = true
			}
			conn, done := openWingAuthTestSocket(t, server, "token", "wing", 30*time.Millisecond, "org-slug")
			_, err := store.RemoveOrgMemberAndEntitlement("org-id", "member")
			mustTest(t, err)
			expectPTYAuthorizationClosed(t, conn, done)
			if got := server.Wings.FindByID("wing"); got != nil {
				t.Fatalf("removed member's wing remained registered: %#v", got)
			}
			if _, _, err := store.ValidateToken("token"); err != nil {
				t.Fatal("test must leave the device credential valid")
			}
			if role := store.GetOrgMemberRole("org-id", "member"); role != "" {
				t.Fatalf("revalidation restored removed membership: %q", role)
			}
		})
	}
}

func TestWingSocketCredentialRevalidation(t *testing.T) {
	for _, node := range []string{"login", "edge"} {
		for _, kind := range []string{"opaque", "jwt", "legacy-jwt"} {
			for _, change := range []string{"revoke", "expire", "rotate", "identity"} {
				t.Run(node+"/"+kind+"/"+change, func(t *testing.T) {
					store := testStore(t)
					mustTest(t, store.CreateUser("user"))
					key, _, err := GenerateECKey()
					mustTest(t, err)
					login := NewServer(store, ServerConfig{InternalSecret: "secret"})
					login.SetJWTKey(key)
					token := "device-token"
					if kind == "opaque" {
						mustTest(t, store.CreateDeviceToken(token, "user", "wing", nil))
					} else {
						exp := time.Now().Add(365 * 24 * time.Hour)
						use := "wing"
						if kind == "legacy-jwt" {
							use = ""
						}
						token, err = jwt.NewWithClaims(jwt.SigningMethodES256, WingClaims{
							RegisteredClaims: jwt.RegisteredClaims{Subject: "user", ExpiresAt: jwt.NewNumericDate(exp)},
							WingID:           "wing", TokenUse: use,
						}).SignedString(key)
						mustTest(t, err)
						mustTest(t, store.CreateDeviceToken(token, "user", "wing", &exp))
					}
					server := login
					if node == "edge" {
						loginHTTP := httptest.NewServer(login)
						defer loginHTTP.Close()
						server = NewServer(nil, ServerConfig{NodeRole: "edge", LoginNodeAddr: loginHTTP.URL, InternalSecret: "secret"})
						server.SetJWTKey(key)
					}
					conn, done := openWingAuthTestSocket(t, server, token, "wing", 30*time.Millisecond)
					mustTest(t, wsjson.Write(context.Background(), conn, ws.Envelope{Type: ws.TypeWingHeartbeat}))
					switch change {
					case "revoke":
						mustTest(t, store.DeleteToken(token))
					case "expire":
						mustTestExec(t, store.DB(), "UPDATE device_tokens SET expires_at = ? WHERE token = ?", time.Now().Add(-time.Minute).UTC().Format("2006-01-02 15:04:05"), token)
					case "rotate":
						mustTest(t, store.RotateDeviceToken(token, "replacement", "user", "wing", nil))
					case "identity":
						mustTestExec(t, store.DB(), "UPDATE device_tokens SET device_id = 'another-wing' WHERE token = ?", token)
					}
					expectPTYAuthorizationClosed(t, conn, done)
					if got := server.Wings.FindByID("wing"); got != nil {
						t.Fatalf("revoked wing remained registered: %#v", got)
					}
				})
			}
		}
	}
}

func TestWingSocketRevalidationPreservesLocalRuntimeWingID(t *testing.T) {
	store := testStore(t)
	mustTest(t, store.CreateUser("local-user"))
	mustTest(t, store.CreateDeviceToken("local-token", "local-user", "local", nil))
	server := NewServer(store, ServerConfig{})
	server.LocalMode = true
	server.SetLocalUser(&User{ID: "local-user"})
	interval := 10 * time.Millisecond
	conn, done := openWingAuthTestSocket(t, server, "local-token", "configured-wing", interval)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	closed := make(chan error, 1)
	go func() {
		_, _, err := conn.Read(ctx)
		closed <- err
	}()
	select {
	case err := <-closed:
		t.Fatalf("valid local wing closed during periodic validation: %v", err)
	case <-time.After(3 * interval):
	}
	mustTest(t, store.DeleteToken("local-token"))
	select {
	case err := <-closed:
		if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
			t.Fatalf("revoked local wing = %v, want policy close", err)
		}
	case <-ctx.Done():
		t.Fatal("revoked local wing stayed connected")
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("revoked local wing handler did not clean up")
	}
}

func TestWingSocketClosesAtJWTExpiryWithLiveRow(t *testing.T) {
	server, _ := testServer(t)
	mustTest(t, server.Store.CreateUser("user"))
	token, err := jwt.NewWithClaims(jwt.SigningMethodES256, WingClaims{
		RegisteredClaims: jwt.RegisteredClaims{Subject: "user", ExpiresAt: jwt.NewNumericDate(time.Now().Add(2 * time.Second))},
		WingID:           "wing", TokenUse: "wing",
	}).SignedString(server.JWTKey())
	mustTest(t, err)
	mustTest(t, server.Store.CreateDeviceToken(token, "user", "wing", nil))
	conn, done := openWingAuthTestSocket(t, server, token, "wing", 10*time.Millisecond)
	expectPTYAuthorizationClosed(t, conn, done)
}

func TestWingClientRecoversFromCredentialStoreOutage(t *testing.T) {
	for _, node := range []string{"login", "edge"} {
		t.Run(node, func(t *testing.T) {
			store := testStore(t)
			token, _ := createTestToken(t, store, "wing")
			login := NewServer(store, ServerConfig{InternalSecret: "secret"})
			server := login
			if node == "edge" {
				loginHTTP := httptest.NewServer(login)
				defer loginHTTP.Close()
				server = NewServer(nil, ServerConfig{NodeRole: "edge", LoginNodeAddr: loginHTTP.URL, InternalSecret: "secret"})
			}
			mustTestExec(t, store.DB(), "ALTER TABLE device_tokens RENAME TO unavailable_tokens")
			var attempts atomic.Int32
			firstStatus := make(chan int, 1)
			httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if attempts.Add(1) == 1 {
					response := httptest.NewRecorder()
					server.handleWingWS(response, r)
					firstStatus <- response.Code
					if _, err := store.DB().Exec("ALTER TABLE unavailable_tokens RENAME TO device_tokens"); err != nil {
						t.Errorf("restore credential store: %v", err)
					}
					w.WriteHeader(response.Code)
					_, _ = w.Write(response.Body.Bytes())
					return
				}
				server.handleWingWS(w, r)
			}))
			defer httpServer.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			registered := false
			client := &ws.Client{
				RoostURL: "ws" + strings.TrimPrefix(httpServer.URL, "http"), Token: token, WingID: "wing",
				OnRegistered: func(ws.RegisteredMsg) {
					registered = true
					cancel()
				},
			}
			if err := client.Run(ctx); !errors.Is(err, context.Canceled) || !registered {
				t.Fatalf("wing failed to recover: registered=%v error=%v", registered, err)
			}
			if status := <-firstStatus; status != http.StatusServiceUnavailable {
				t.Fatalf("store outage = HTTP %d, want 503", status)
			}
			if got := attempts.Load(); got != 2 {
				t.Fatalf("handshake attempts = %d, want outage then recovery", got)
			}
		})
	}
}
