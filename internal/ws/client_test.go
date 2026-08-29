package ws

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestClientClassifiesOnlyHTTPUnauthorizedAsAuthFailure(t *testing.T) {
	for _, test := range []struct {
		name       string
		status     int
		body       string
		wantReject bool
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, body: "expired", wantReject: true},
		{name: "unavailable body mentions 401", status: http.StatusServiceUnavailable, body: "upstream 401", wantReject: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, test.body, test.status)
			}))
			defer server.Close()

			client := &Client{RoostURL: strings.Replace(server.URL, "http://", "ws://", 1), Token: "token"}
			_, err := client.connectAndServe(context.Background())
			if got := errors.Is(err, ErrAuthRejected); got != test.wantReject {
				t.Fatalf("auth rejection = %v, want %v (error: %v)", got, test.wantReject, err)
			}
		})
	}
}

func TestRegisterPTYSessionIsAtomicAndCleanupIsOwnershipSafe(t *testing.T) {
	client := &Client{}
	ctx := context.Background()
	_, firstInput, firstCleanup, registered := client.RegisterPTYSession(ctx, "session-1")
	if !registered {
		t.Fatal("first registration failed")
	}
	_, duplicateInput, duplicateCleanup, registered := client.RegisterPTYSession(ctx, "session-1")
	if registered {
		t.Fatal("duplicate registration succeeded")
	}
	duplicateCleanup()
	if _, open := <-duplicateInput; open {
		t.Fatal("duplicate registration returned an open input channel")
	}

	firstCleanup()
	_, replacementInput, replacementCleanup, registered := client.RegisterPTYSession(ctx, "session-1")
	if !registered {
		t.Fatal("replacement registration failed after cleanup")
	}
	defer replacementCleanup()

	// Cleanup can be called more than once or arrive late. It must not remove a
	// newer owner of the same session ID.
	firstCleanup()
	message := []byte(`{"type":"pty.input"}`)
	if !client.PushPTYInput("session-1", message) {
		t.Fatal("stale cleanup removed replacement session")
	}
	select {
	case got := <-replacementInput:
		if string(got) != string(message) {
			t.Fatalf("replacement input = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("replacement session did not receive input")
	}

	select {
	case <-firstInput:
		t.Fatal("replacement input was delivered to stale session")
	default:
	}
}

func TestClientRejectsDuplicatePTYStart(t *testing.T) {
	denial := make(chan ErrorMsg, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer func() { _ = conn.Close(websocket.StatusNormalClosure, "done") }()
		ctx := r.Context()
		if _, _, err := conn.Read(ctx); err != nil {
			t.Errorf("read registration: %v", err)
			return
		}
		if err := wsjson.Write(ctx, conn, RegisteredMsg{Type: TypeRegistered, WingID: "wing-1"}); err != nil {
			t.Errorf("write registration: %v", err)
			return
		}
		start := PTYStart{Type: TypePTYStart, SessionID: "session-1", WingID: "wing-1"}
		if err := wsjson.Write(ctx, conn, start); err != nil {
			t.Errorf("write first PTY start: %v", err)
			return
		}
		if err := wsjson.Write(ctx, conn, start); err != nil {
			t.Errorf("write duplicate PTY start: %v", err)
			return
		}
		var response ErrorMsg
		if err := wsjson.Read(ctx, conn, &response); err != nil {
			t.Errorf("read duplicate denial: %v", err)
			return
		}
		denial <- response
		<-ctx.Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{}, 2)
	client := &Client{
		RoostURL: strings.Replace(server.URL, "http://", "ws://", 1), Token: "token", WingID: "wing-1",
		OnPTY: func(ctx context.Context, _ PTYStart, _ PTYWriteFunc, _ <-chan []byte) {
			started <- struct{}{}
			<-ctx.Done()
		},
	}
	done := make(chan struct{})
	go func() {
		_, _ = client.connectAndServe(ctx)
		close(done)
	}()

	select {
	case response := <-denial:
		if response.Type != TypeError || response.SessionID != "session-1" || !strings.Contains(response.Message, "already active") {
			t.Fatalf("duplicate denial = %#v", response)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("duplicate PTY start was not rejected")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first PTY handler did not start")
	}
	select {
	case <-started:
		t.Fatal("duplicate PTY handler started")
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("client did not stop")
	}
}

func TestClientRejectsPlaintextRelayControls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer func() { _ = conn.Close(websocket.StatusNormalClosure, "done") }()

		ctx := r.Context()
		if _, _, err := conn.Read(ctx); err != nil { // wing.register
			t.Errorf("read registration: %v", err)
			return
		}
		messages := []any{
			RegisteredMsg{
				Type: TypeRegistered, WingID: "connection-1",
				PasskeyRPID: "roost.example.test", PasskeyOrigins: []string{"https://app.roost.example.test"},
			},
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
	registered := make(chan RegisteredMsg, 1)
	client.OnRegistered = func(message RegisteredMsg) { registered <- message }
	_, input, cleanup, registeredOK := client.RegisterPTYSession(ctx, "session-1")
	if !registeredOK {
		t.Fatal("session registration failed")
	}
	defer cleanup()

	done := make(chan struct{})
	go func() {
		_, _ = client.connectAndServe(ctx)
		close(done)
	}()

	select {
	case message := <-registered:
		if message.PasskeyRPID != "roost.example.test" || len(message.PasskeyOrigins) != 1 {
			t.Fatalf("registered callback policy = %#v", message)
		}
	case <-time.After(time.Second):
		t.Fatal("registered callback did not receive coordinator policy")
	}

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
		if err := json.Unmarshal(data, &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Type != TypePTYAttentionAck {
			t.Fatalf("attention acknowledgement type = %q", envelope.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("authorized attention acknowledgement did not reach session")
	}

	trustedResize, err := json.Marshal(PTYResize{Type: TypePTYResize, SessionID: "session-1", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	if !client.PushPTYInput("session-1", trustedResize) {
		t.Fatal("trusted P2P control was not delivered")
	}
	select {
	case data := <-input:
		var envelope Envelope
		if err := json.Unmarshal(data, &envelope); err != nil {
			t.Fatal(err)
		}
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

func TestTunnelHandlerAdmissionIsBoundedAndReusable(t *testing.T) {
	client := &Client{}
	for range maxTunnelHandlers {
		if !client.acquireTunnelHandler() {
			t.Fatal("handler rejected below concurrency limit")
		}
	}
	if client.acquireTunnelHandler() {
		t.Fatal("handler admitted above concurrency limit")
	}
	client.releaseTunnelHandler()
	if !client.acquireTunnelHandler() {
		t.Fatal("released handler slot was not reusable")
	}
	for range maxTunnelHandlers {
		client.releaseTunnelHandler()
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
		defer func() { _ = conn.Close(websocket.StatusNormalClosure, "done") }()
		ctx := r.Context()

		var reg WingRegister
		if err := wsjson.Read(ctx, conn, &reg); err != nil {
			t.Errorf("read registration: %v", err)
			return
		}
		if reg.HostedRelay != HostedRelayDeny {
			t.Errorf("registered hosted relay policy = %q", reg.HostedRelay)
		}
		if !reg.PurposeBinding || !reg.DirectMCP {
			t.Errorf("registered direct capabilities: purpose_binding=%v direct_mcp=%v", reg.PurposeBinding, reg.DirectMCP)
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
		DirectMCP:   true,
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
		_, _ = client.connectAndServe(ctx)
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

func TestClientRuntimeConfigPublishesOneDefensiveSnapshot(t *testing.T) {
	client := &Client{HostedRelay: HostedRelayDeny, RelayPubKey: "relay-key"}
	labels := []string{"one"}
	client.UpdateRuntimeConfig(true, 3, true, labels, "/work")
	labels[0] = "mutated"

	snapshot := client.runtimeConfig()
	if !snapshot.Locked || snapshot.AllowedCount != 3 || !snapshot.DirectMCP ||
		snapshot.RootDir != "/work" || snapshot.HostedRelay != HostedRelayDeny ||
		snapshot.RelayPublicKey != "relay-key" || len(snapshot.Labels) != 1 || snapshot.Labels[0] != "one" {
		t.Fatalf("runtime snapshot = %#v", snapshot)
	}
	snapshot.Labels[0] = "changed"
	if got := client.runtimeConfig().Labels[0]; got != "one" {
		t.Fatalf("snapshot shared label storage: %q", got)
	}

	client.UpdateAccessConfig(false, 1)
	snapshot = client.runtimeConfig()
	if snapshot.Locked || snapshot.AllowedCount != 1 || !snapshot.DirectMCP || snapshot.RootDir != "/work" {
		t.Fatalf("access update tore unrelated runtime config: %#v", snapshot)
	}
}
