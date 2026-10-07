package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestDashboardSocketRevalidatesPinnedSession(t *testing.T) {
	for _, node := range []string{"login", "edge"} {
		for _, change := range []string{"revoke", "expire"} {
			t.Run(node+"/"+change, func(t *testing.T) {
				store := testStore(t)
				mustTest(t, store.CreateUser("user"))
				mustTest(t, store.CreateSession("session", "user", time.Now().Add(time.Hour)))
				mustTest(t, store.CreateDeviceToken("alternative", "user", "wing", nil))
				login := NewServer(store, ServerConfig{InternalSecret: "secret"})
				server := login
				if node == "edge" {
					loginHTTP := httptest.NewServer(login)
					defer loginHTTP.Close()
					server = NewServer(nil, ServerConfig{NodeRole: "edge", LoginNodeAddr: loginHTTP.URL, InternalSecret: "secret"})
					server.SetSessionCache(NewSessionCache("secret"))
				}
				done := make(chan struct{})
				httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					defer close(done)
					server.handleAppWSWithAuthInterval(w, r, 10*time.Millisecond)
				}))
				defer httpServer.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(httpServer.URL, "http")+"/ws/app", &websocket.DialOptions{
					HTTPHeader: http.Header{
						"Cookie":        {sessionCookieNameForChannel() + "=session"},
						"Authorization": {"Bearer alternative"},
					},
				})
				mustTest(t, err)
				defer func() { _ = conn.CloseNow() }()
				if change == "revoke" {
					mustTest(t, store.DeleteSession("session"))
				} else {
					mustTestExec(t, store.DB(), "UPDATE sessions SET expires_at = ? WHERE token = ?", time.Now().Add(-time.Minute).UTC().Format("2006-01-02 15:04:05"), "session")
				}
				expectPTYAuthorizationClosed(t, conn, done)
				server.Wings.subMu.RLock()
				subs := len(server.Wings.subs["user"])
				server.Wings.subMu.RUnlock()
				if subs != 0 {
					t.Fatal("revoked dashboard subscription remained active")
				}
				if _, _, err := store.ValidateToken("alternative"); err != nil {
					t.Fatal("test must leave the alternative bearer credential valid")
				}
			})
		}
	}
}
