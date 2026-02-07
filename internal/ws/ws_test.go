package ws

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"nhooyr.io/websocket"
)

func TestBackoff(t *testing.T) {
	bo := NewBackoff(time.Second, 60*time.Second)

	expected := []time.Duration{
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		32 * time.Second,
		60 * time.Second, // capped
		60 * time.Second, // stays capped
	}

	for i, want := range expected {
		got := bo.Next()
		if got != want {
			t.Errorf("attempt %d: got %v, want %v", i, got, want)
		}
	}
}

func TestBackoffReset(t *testing.T) {
	bo := NewBackoff(time.Second, 60*time.Second)
	bo.Next() // 1s
	bo.Next() // 2s
	bo.Next() // 4s
	bo.Reset()

	got := bo.Next()
	if got != time.Second {
		t.Errorf("after reset: got %v, want %v", got, time.Second)
	}
}

func TestClientState(t *testing.T) {
	c := NewClient("ws://localhost:0/ws", "token")
	if c.State() != StateDisconnected {
		t.Errorf("initial state = %d, want %d", c.State(), StateDisconnected)
	}
}

func TestClientOfflineQueuing(t *testing.T) {
	c := NewClient("ws://localhost:0/ws", "token")

	for i := 0; i < 5; i++ {
		msg, err := NewMessage(MsgTaskSubmit, TaskSubmitPayload{What: "task"})
		if err != nil {
			t.Fatalf("NewMessage: %v", err)
		}
		if err := c.Send(msg); err != nil {
			t.Fatalf("Send: %v", err)
		}
	}

	c.mu.Lock()
	n := len(c.outbox)
	c.mu.Unlock()

	if n != 5 {
		t.Errorf("outbox len = %d, want 5", n)
	}
}

func TestClientOfflineQueueBound(t *testing.T) {
	c := NewClient("ws://localhost:0/ws", "token")

	for i := 0; i < maxOutbox+10; i++ {
		msg, err := NewMessage(MsgTaskSubmit, TaskSubmitPayload{What: "task"})
		if err != nil {
			t.Fatalf("NewMessage: %v", err)
		}
		_ = c.Send(msg)
	}

	c.mu.Lock()
	n := len(c.outbox)
	c.mu.Unlock()

	if n != maxOutbox {
		t.Errorf("outbox len = %d, want %d", n, maxOutbox)
	}
}

func newTestServer(t *testing.T, handler func(*websocket.Conn)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
		})
		if err != nil {
			t.Logf("accept error: %v", err)
			return
		}
		handler(conn)
	}))
}

func TestClientConnectAndAuth(t *testing.T) {
	srv := newTestServer(t, func(conn *websocket.Conn) {
		ctx := context.Background()
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Logf("server read: %v", err)
			return
		}

		var msg Message
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Logf("server unmarshal: %v", err)
			return
		}

		if msg.Type != MsgAuth {
			t.Logf("expected auth, got %q", msg.Type)
			return
		}

		reply, _ := NewMessage(MsgAuthResult, AuthResultPayload{Success: true})
		reply.ReplyTo = msg.ID
		replyData, _ := json.Marshal(reply)
		conn.Write(ctx, websocket.MessageText, replyData)

		// Keep connection open briefly for client to process
		time.Sleep(100 * time.Millisecond)
		conn.Close(websocket.StatusNormalClosure, "done")
	})
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	c := NewClient(wsURL, "test-token")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer c.Close()

	if c.State() != StateConnected {
		t.Fatalf("state after connect = %d, want %d", c.State(), StateConnected)
	}

	if err := c.authenticate(ctx); err != nil {
		t.Fatalf("authenticate: %v", err)
	}

	if c.State() != StateReady {
		t.Errorf("state after auth = %d, want %d", c.State(), StateReady)
	}
}

func TestClientReconnect(t *testing.T) {
	var connCount int
	var mu sync.Mutex

	srv := newTestServer(t, func(conn *websocket.Conn) {
		mu.Lock()
		connCount++
		n := connCount
		mu.Unlock()

		ctx := context.Background()

		// Handle auth
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var msg Message
		json.Unmarshal(data, &msg)
		reply, _ := NewMessage(MsgAuthResult, AuthResultPayload{Success: true})
		reply.ReplyTo = msg.ID
		replyData, _ := json.Marshal(reply)
		conn.Write(ctx, websocket.MessageText, replyData)

		if n == 1 {
			// First connection: close immediately to trigger reconnect
			conn.Close(websocket.StatusGoingAway, "test disconnect")
			return
		}

		// Second connection: stay open
		time.Sleep(2 * time.Second)
		conn.Close(websocket.StatusNormalClosure, "done")
	})
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	c := NewClient(wsURL, "test-token")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Run in background
	done := make(chan error, 1)
	go func() {
		done <- c.Run(ctx)
	}()

	// Wait for at least 2 connections (original + reconnect)
	deadline := time.After(8 * time.Second)
	for {
		mu.Lock()
		n := connCount
		mu.Unlock()
		if n >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for reconnect, connections: %d", n)
		case <-time.After(100 * time.Millisecond):
		}
	}

	cancel()
	<-done

	mu.Lock()
	final := connCount
	mu.Unlock()
	if final < 2 {
		t.Errorf("expected at least 2 connections, got %d", final)
	}
}
