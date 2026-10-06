package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestRelaySocketReservationsAreAtomicAndReclaimed(t *testing.T) {
	server := NewServer(nil, ServerConfig{ResourceLimits: ResourceLimits{Connections: 2, ConnectionsPerUser: 1}})
	var wg sync.WaitGroup
	releases := make(chan func(), 20)
	for range 20 {
		wg.Go(func() {
			release, err := server.reserveSocket("one-user")
			if err == nil {
				releases <- release
			} else if !strings.Contains(err.Error(), "limit for this user") {
				t.Errorf("unclear per-user limit error: %v", err)
			}
		})
	}
	wg.Wait()
	close(releases)
	if len(releases) != 1 {
		t.Fatalf("admitted %d reservations for user limit 1", len(releases))
	}
	other, err := server.reserveSocket("other-user")
	mustTest(t, err)
	if _, err := server.reserveSocket("third-user"); err == nil || !strings.Contains(err.Error(), "global connection limit") {
		t.Fatalf("global admission error = %v", err)
	}
	for release := range releases {
		release()
		release()
	}
	other()
	if server.socketCount != 0 || len(server.socketsByUser) != 0 {
		t.Fatal("released reservations retained counters")
	}
	release, err := server.reserveSocket("one-user")
	mustTest(t, err)
	release()
}

func waitSocketCount(t *testing.T, server *Server, count int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		server.socketMu.Lock()
		got := server.socketCount
		server.socketMu.Unlock()
		if got == count {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("socket count = %d, want %d", got, count)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRelaySocketAdmissionSharedAcrossAllEndpoints(t *testing.T) {
	store := testStore(t)
	server := NewServer(store, ServerConfig{ResourceLimits: ResourceLimits{Connections: 2, ConnectionsPerUser: 1}})
	firstToken, firstUser := createTestToken(t, store, "first")
	secondToken, _ := createTestToken(t, store, "second")
	thirdToken, _ := createTestToken(t, store, "third")
	mustTest(t, store.CreateSession("session", firstUser, time.Now().Add(time.Hour)))
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)
	dial := func(path, token string, want int) *websocket.Conn {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		headers := http.Header{}
		if token != "" {
			headers.Set("Authorization", "Bearer "+token)
		} else {
			headers.Set("Cookie", "wt_session=session")
		}
		conn, response, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+path, &websocket.DialOptions{HTTPHeader: headers})
		if want == 101 {
			if err != nil {
				t.Fatalf("dial %s: %v", path, err)
			}
			t.Cleanup(func() { _ = conn.CloseNow() })
			return conn
		}
		if err == nil || response == nil || response.StatusCode != want {
			t.Fatalf("dial %s status: response=%v err=%v", path, response, err)
		}
		return nil
	}
	first := dial("/ws/pty", firstToken, 101)
	dial("/ws/relay", firstToken, 429)
	dial("/ws/app", "", 429)
	dial("/ws/wing", firstToken, 429)
	second := dial("/ws/wing", secondToken, 101)
	dial("/ws/pty", thirdToken, 429)
	mustTest(t, first.CloseNow())
	waitSocketCount(t, server, 1)
	third := dial("/ws/pty", thirdToken, 101)
	mustTest(t, second.CloseNow())
	mustTest(t, third.CloseNow())
	waitSocketCount(t, server, 0)
	// A failed upgrade must release its reservation too.
	request := httptest.NewRequest(http.MethodGet, "/ws/pty", nil)
	request.Header.Set("Authorization", "Bearer "+firstToken)
	server.handlePTYWS(httptest.NewRecorder(), request)
	waitSocketCount(t, server, 0)
}

func TestRelaySocketHeartbeatReclaimsUnresponsiveClients(t *testing.T) {
	server := NewServer(nil, ServerConfig{ResourceLimits: ResourceLimits{PingInterval: 10 * time.Millisecond, PongTimeout: 30 * time.Millisecond}})
	closed := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, ctx, release, err := server.acceptSocket(w, r, "user", nil)
		if err != nil {
			return
		}
		defer close(closed)
		defer release()
		defer func() { _ = conn.CloseNow() }()
		_, _, _ = conn.Read(ctx)
	}))
	t.Cleanup(ts.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http"), nil)
	mustTest(t, err)
	t.Cleanup(func() { _ = client.CloseNow() })
	// No reader means no automatic pong: model an abandoned transport.
	select {
	case <-closed:
	case <-ctx.Done():
		t.Fatal("unresponsive client survived ping/pong deadline")
	}
	waitSocketCount(t, server, 0)
}

func TestRelaySocketHeartbeatKeepsResponsiveIdleClients(t *testing.T) {
	server := NewServer(nil, ServerConfig{ResourceLimits: ResourceLimits{PingInterval: 10 * time.Millisecond, PongTimeout: 100 * time.Millisecond}})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, ctx, release, err := server.acceptSocket(w, r, "user", nil)
		if err != nil {
			return
		}
		defer release()
		defer func() { _ = conn.CloseNow() }()
		<-conn.CloseRead(ctx).Done()
	}))
	t.Cleanup(ts.Close)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http"), nil)
	mustTest(t, err)
	t.Cleanup(func() { _ = client.CloseNow() })
	readCtx := client.CloseRead(ctx)
	select {
	case <-readCtx.Done():
		t.Fatal("responsive idle client was disconnected")
	case <-time.After(200 * time.Millisecond):
	}
	mustTest(t, client.CloseNow())
	waitSocketCount(t, server, 0)
}
