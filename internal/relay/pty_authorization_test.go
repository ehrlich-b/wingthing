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

func TestWingAccessDoesNotTrustStaleOrgSnapshots(t *testing.T) {
	store := testStore(t)
	mustTest(t, store.CreateUser("owner"))
	mustTest(t, store.CreateUser("admin"))
	mustTest(t, store.CreateOrgWithSeats("org", "Org", "org", "owner", 2))
	mustTest(t, store.AddOrgMember("org", "admin", "admin"))
	server := NewServer(store, ServerConfig{})
	wing := &ConnectedWing{UserID: "owner", OrgID: "org"}
	ids, roles := []string{"org"}, map[string]string{"org": "admin"}
	if !server.canAccessWing("admin", wing, ids) || roleForWingUser(server, wing, "admin", ids, roles) != "admin" {
		t.Fatal("current org admin was denied")
	}
	mustTestExec(t, store.DB(), "UPDATE org_members SET role = 'member' WHERE user_id = 'admin'")
	if got := roleForWingUser(server, wing, "admin", ids, roles); got != "member" {
		t.Fatalf("stale role survived demotion: %q", got)
	}
	_, err := store.RemoveOrgMemberAndEntitlement("org", "admin")
	mustTest(t, err)
	if server.canAccessWing("admin", wing, ids) || roleForWingUser(server, wing, "admin", ids, roles) != "" {
		t.Fatal("stale snapshot survived membership removal")
	}
	if !server.canAccessWing("owner", wing) {
		t.Fatal("personal wing ownership was lost")
	}
}

// The handler's production interval is 30s. Use a shorter interval to exercise
// the same idle-socket path without making the regression suite wait for a TTL.
func openPTYAuthTestSocket(t *testing.T, server *Server, interval time.Duration, header http.Header, query string) (*websocket.Conn, <-chan struct{}) {
	t.Helper()
	done := make(chan struct{})
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		server.handlePTYWSWithAuthInterval(w, r, interval)
	}))
	t.Cleanup(httpServer.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, response, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(httpServer.URL, "http")+"/ws/pty"+query, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatalf("PTY handshake: response=%v err=%v", response, err)
	}
	t.Cleanup(func() {
		_ = conn.CloseNow()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("PTY socket handler did not finish cleanup")
		}
	})
	return conn, done
}

func expectPTYAuthorizationClosed(t *testing.T, conn *websocket.Conn, done <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("revoked socket still usable: data=%s err=%v", data, err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("revoked socket handler did not clean up")
	}
}

func TestPTYSocketClosesWhenOrgAuthorityRevoked(t *testing.T) {
	for _, node := range []string{"login", "edge"} {
		for _, change := range []string{"membership", "role"} {
			for _, trigger := range []string{"idle", "request"} {
				t.Run(node+"/"+change+"/"+trigger, func(t *testing.T) {
					store := testStore(t)
					mustTest(t, store.CreateUser("owner"))
					mustTest(t, store.CreateUser("admin"))
					mustTest(t, store.CreateSession("session", "admin", time.Now().Add(time.Hour)))
					mustTest(t, store.CreateOrgWithSeats("org", "Org", "org", "owner", 2))
					mustTest(t, store.AddOrgMember("org", "admin", "admin"))
					// Personal Pro survives removal of the org's authority.
					mustTest(t, store.CreateSubscription(&Subscription{ID: "sub", UserID: strPtr("admin"), Plan: "pro", Status: "active", Seats: 1}))
					mustTest(t, store.CreateEntitlement(&Entitlement{ID: "ent", UserID: "admin", SubscriptionID: "sub"}))
					login := NewServer(store, ServerConfig{InternalSecret: "secret", RelayPolicy: RelayPolicyDirectFree})
					server := login
					if node == "edge" {
						loginHTTP := httptest.NewServer(login)
						defer loginHTTP.Close()
						server = NewServer(nil, ServerConfig{NodeRole: "edge", LoginNodeAddr: loginHTTP.URL, InternalSecret: "secret"})
						server.SetSessionCache(NewSessionCache("secret"))
					}
					interval := 10 * time.Millisecond
					if trigger == "request" {
						interval = time.Hour
					}
					conn, done := openPTYAuthTestSocket(t, server, interval, http.Header{"Cookie": {sessionCookieNameForChannel() + "=session"}}, "")
					if change == "membership" {
						_, err := store.RemoveOrgMemberAndEntitlement("org", "admin")
						mustTest(t, err)
					} else {
						mustTestExec(t, store.DB(), "UPDATE org_members SET role = 'member' WHERE user_id = 'admin'")
					}
					if !login.relayAccess("admin").Allowed {
						t.Fatal("test lost personal Pro entitlement")
					}
					if trigger == "request" {
						mustTest(t, conn.Write(context.Background(), websocket.MessageText, []byte(`{"type":"tunnel.req","wing_id":"org-wing","request_id":"after-revocation"}`)))
					}
					expectPTYAuthorizationClosed(t, conn, done)
				})
			}
		}
	}
}

func TestPTYOrgValidationFailsClosedOnLoginOutage(t *testing.T) {
	store := testStore(t)
	mustTest(t, store.CreateUser("user"))
	mustTest(t, store.CreateSession("session", "user", time.Now().Add(time.Hour)))
	login := NewServer(store, ServerConfig{InternalSecret: "secret"})
	loginHTTP := httptest.NewServer(login)
	defer loginHTTP.Close()
	edge := NewServer(nil, ServerConfig{NodeRole: "edge", LoginNodeAddr: loginHTTP.URL, InternalSecret: "secret"})
	edge.SetSessionCache(NewSessionCache("secret"))
	conn, done := openPTYAuthTestSocket(t, edge, 10*time.Millisecond, http.Header{"Cookie": {sessionCookieNameForChannel() + "=session"}}, "")
	loginHTTP.Close()
	expectPTYAuthorizationClosed(t, conn, done)
}

func TestPTYSocketRevalidatesAuthorityAcquiredAfterAdmission(t *testing.T) {
	store := testStore(t)
	mustTest(t, store.CreateUser("owner"))
	mustTest(t, store.CreateUser("user"))
	mustTest(t, store.CreateSession("session", "user", time.Now().Add(time.Hour)))
	mustTest(t, store.CreateOrgWithSeats("org", "Org", "org", "owner", 2))
	server := NewServer(store, ServerConfig{})
	conn, done := openPTYAuthTestSocket(t, server, 10*time.Millisecond, http.Header{"Cookie": {sessionCookieNameForChannel() + "=session"}}, "")
	mustTest(t, store.AddOrgMember("org", "user", "admin"))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// A reply proves the new org authority was observed by the request loop.
	mustTest(t, conn.Write(ctx, websocket.MessageText, []byte(`{"type":"tunnel.req","wing_id":"missing","request_id":"refresh"}`)))
	_, _, err := conn.Read(ctx)
	mustTest(t, err)
	_, err = store.RemoveOrgMemberAndEntitlement("org", "user")
	mustTest(t, err)
	expectPTYAuthorizationClosed(t, conn, done)
}
