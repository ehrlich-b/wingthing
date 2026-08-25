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
	"github.com/coder/websocket/wsjson"
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
			PTYAttentionAck{Type: TypePTYAttentionAck, SessionID: "session-1"},
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
	select {
	case data := <-input:
		var envelope Envelope
		json.Unmarshal(data, &envelope)
		if envelope.Type != TypePTYAttentionAck {
			t.Fatalf("attention acknowledgement type = %q", envelope.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("authorized attention acknowledgement did not reach session")
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

func TestClientHostedRelayDenyAllowsOnlyBoundedCoordination(t *testing.T) {
	serverDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")
		ctx := r.Context()

		var reg WingRegister
		if err := wsjson.Read(ctx, conn, &reg); err != nil {
			t.Errorf("read registration: %v", err)
			return
		}
		if reg.HostedRelay != HostedRelayDeny {
			t.Errorf("registered hosted relay policy = %q", reg.HostedRelay)
		}
		if err := wsjson.Write(ctx, conn, RegisteredMsg{Type: TypeRegistered, WingID: "wing-1"}); err != nil {
			t.Errorf("write registered: %v", err)
			return
		}
		messages := []any{
			PTYStart{Type: TypePTYStart, SessionID: "blocked-session", WingID: "wing-1"},
			TunnelRequest{Type: TypeTunnelRequest, WingID: "wing-1", RequestID: "blocked-control", Purpose: TunnelPurposeControl, Payload: "opaque"},
			TunnelRequest{Type: TypeTunnelRequest, WingID: "wing-1", RequestID: "blocked-oversized", Purpose: TunnelPurposeDiscovery, Payload: strings.Repeat("x", MaxCoordinationTunnelPayload+1)},
			TunnelRequest{Type: TypeTunnelRequest, WingID: "wing-1", RequestID: "allowed-discovery", Purpose: TunnelPurposeDiscovery, Payload: "opaque"},
		}
		for _, message := range messages {
			if err := wsjson.Write(ctx, conn, message); err != nil {
				t.Errorf("write message: %v", err)
				return
			}
		}

		for range 3 {
			var denied ErrorMsg
			if err := wsjson.Read(ctx, conn, &denied); err != nil {
				t.Errorf("read denial: %v", err)
				return
			}
			if denied.Type != TypeError || !strings.Contains(denied.Message, "hosted relay") {
				t.Errorf("denial = %#v", denied)
			}
		}
		close(serverDone)
		<-ctx.Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ptyCalled := make(chan struct{}, 1)
	tunnelCalled := make(chan TunnelRequest, 1)
	denials := make(chan string, 3)
	client := &Client{
		RoostURL:    strings.Replace(server.URL, "http://", "ws://", 1),
		Token:       "token",
		WingID:      "wing-1",
		HostedRelay: HostedRelayDeny,
		OnPTY: func(context.Context, PTYStart, PTYWriteFunc, <-chan []byte) {
			ptyCalled <- struct{}{}
		},
		OnTunnel: func(_ context.Context, req TunnelRequest, _ PTYWriteFunc) {
			tunnelCalled <- req
		},
		OnHostedRelayDenied: func(operation string) {
			denials <- operation
		},
	}
	done := make(chan struct{})
	go func() {
		client.connectAndServe(ctx)
		close(done)
	}()

	select {
	case req := <-tunnelCalled:
		if req.RequestID != "allowed-discovery" {
			t.Fatalf("unexpected tunnel callback: %#v", req)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("coordination tunnel was not delivered")
	}
	select {
	case <-ptyCalled:
		t.Fatal("hosted PTY callback ran under deny policy")
	default:
	}
	gotDenials := map[string]int{}
	for gotDenials[TypePTYStart]+gotDenials[TypeTunnelRequest] < 3 {
		select {
		case operation := <-denials:
			gotDenials[operation]++
		case <-time.After(2 * time.Second):
			t.Fatalf("denials = %#v", gotDenials)
		}
	}
	if gotDenials[TypePTYStart] != 1 || gotDenials[TypeTunnelRequest] != 2 {
		t.Fatalf("denials = %#v", gotDenials)
	}
	<-serverDone
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("client did not stop")
	}
}

func TestClientHostedRelayDenySuppressesOutboundSessionAttention(t *testing.T) {
	denied := make(chan string, 1)
	client := &Client{
		HostedRelay: HostedRelayDeny,
		OnHostedRelayDenied: func(operation string) {
			denied <- operation
		},
	}
	if err := client.SendAttention(context.Background(), "private-session"); err != nil {
		t.Fatalf("suppressed attention returned error: %v", err)
	}
	select {
	case operation := <-denied:
		if operation != TypeSessionAttention {
			t.Fatalf("denied operation = %q", operation)
		}
	case <-time.After(time.Second):
		t.Fatal("suppressed attention was not audited")
	}

	legacy := &Client{}
	if err := legacy.SendAttention(context.Background(), "session"); err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("legacy attention behavior = %v", err)
	}
}
