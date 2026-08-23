package ws

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestClientRejectsPlaintextRelayControls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")

		ctx := r.Context()
		if _, _, err := conn.Read(ctx); err != nil { // wing.register
			t.Errorf("read registration: %v", err)
			return
		}
		messages := []any{
			RegisteredMsg{Type: TypeRegistered, WingID: "connection-1"},
			PTYResize{Type: TypePTYResize, SessionID: "session-1", Cols: 120, Rows: 40},
			PTYKill{Type: TypePTYKill, SessionID: "session-1"},
			PTYInput{Type: TypePTYInput, SessionID: "session-1", Data: "ciphertext"},
		}
		for _, message := range messages {
			data, _ := json.Marshal(message)
			if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
				t.Errorf("write message: %v", err)
				return
			}
		}
		<-ctx.Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &Client{
		RoostURL: strings.Replace(server.URL, "http://", "ws://", 1),
		Token:    "token",
		WingID:   "wing-1",
	}
	_, input, cleanup := client.RegisterPTYSession(ctx, "session-1")
	defer cleanup()

	done := make(chan struct{})
	go func() {
		client.connectAndServe(ctx)
		close(done)
	}()

	select {
	case data := <-input:
		var envelope Envelope
		if err := json.Unmarshal(data, &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Type != TypePTYInput {
			t.Fatalf("session received plaintext relay control %q", envelope.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("session did not receive allowed encrypted input envelope")
	}

	trustedResize, _ := json.Marshal(PTYResize{Type: TypePTYResize, SessionID: "session-1", Cols: 80, Rows: 24})
	if !client.PushPTYInput("session-1", trustedResize) {
		t.Fatal("trusted P2P control was not delivered")
	}
	select {
	case data := <-input:
		var envelope Envelope
		json.Unmarshal(data, &envelope)
		if envelope.Type != TypePTYResize {
			t.Fatalf("trusted control type = %q", envelope.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("trusted P2P resize did not reach session")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("client did not stop after cancellation")
	}
}
