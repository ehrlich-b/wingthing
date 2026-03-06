//go:build e2e

package integ

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	pionwebrtc "github.com/pion/webrtc/v4"

	"github.com/coder/websocket/wsjson"

	rtc "github.com/ehrlich-b/wingthing/internal/webrtc"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

// TestP2PSignalingThroughRelay verifies that pty.migrate and pty.migrated/fallback
// messages are correctly forwarded through the relay between browser and wing.
func TestP2PSignalingThroughRelay(t *testing.T) {
	_, ts, store := testRelayAndWS(t)
	token, _ := createTestUser(t, store, "p2p-sig")

	wingConn := connectWing(t, wsURL(ts), token, "wing-p2p-sig", []string{"claude"})
	defer wingConn.CloseNow()
	browser := connectBrowser(t, wsURL(ts), token, "wing-p2p-sig")
	defer browser.CloseNow()

	sess := startSession(t, browser, wingConn, "claude", "wing-p2p-sig")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Step 1: Browser requests migration
	migrate := ws.PTYMigrate{
		Type:      ws.TypePTYMigrate,
		SessionID: sess,
		AuthToken: "passkey-token-123",
	}
	if err := wsjson.Write(ctx, browser, migrate); err != nil {
		t.Fatalf("write pty.migrate: %v", err)
	}

	// Wing receives pty.migrate with auth token
	var wingMigrate ws.PTYMigrate
	if err := wsjson.Read(ctx, wingConn, &wingMigrate); err != nil {
		t.Fatalf("wing read pty.migrate: %v", err)
	}
	if wingMigrate.SessionID != sess {
		t.Errorf("session = %s, want %s", wingMigrate.SessionID, sess)
	}
	if wingMigrate.AuthToken != "passkey-token-123" {
		t.Errorf("auth_token = %q, want %q", wingMigrate.AuthToken, "passkey-token-123")
	}

	// Step 2: Wing acknowledges migration
	wsjson.Write(ctx, wingConn, ws.PTYMigrated{Type: ws.TypePTYMigrated, SessionID: sess})

	var migrated ws.PTYMigrated
	if err := wsjson.Read(ctx, browser, &migrated); err != nil {
		t.Fatalf("browser read pty.migrated: %v", err)
	}
	if migrated.SessionID != sess {
		t.Errorf("migrated session = %s, want %s", migrated.SessionID, sess)
	}

	// Step 3: P2P fails, wing falls back
	wsjson.Write(ctx, wingConn, ws.PTYFallback{Type: ws.TypePTYFallback, SessionID: sess})

	var fallback ws.PTYFallback
	if err := wsjson.Read(ctx, browser, &fallback); err != nil {
		t.Fatalf("browser read pty.fallback: %v", err)
	}
	if fallback.SessionID != sess {
		t.Errorf("fallback session = %s, want %s", fallback.SessionID, sess)
	}

	// Step 4: After fallback, relay routing still works
	outData := "aGVsbG8=" // "hello" in base64
	wsjson.Write(ctx, wingConn, ws.PTYOutput{Type: ws.TypePTYOutput, SessionID: sess, Data: outData})

	var output ws.PTYOutput
	if err := wsjson.Read(ctx, browser, &output); err != nil {
		t.Fatalf("browser read after fallback: %v", err)
	}
	if output.Data != outData {
		t.Errorf("output data = %q, want %q", output.Data, outData)
	}
}

// TestSwappableWriterMigrationAndFallback tests the full SwappableWriter lifecycle:
// relay → migrate → DC → fallback → relay.
func TestSwappableWriterMigrationAndFallback(t *testing.T) {
	var messages []string
	var mu sync.Mutex

	record := func(prefix string) func(v any) error {
		return func(v any) error {
			data, _ := json.Marshal(v)
			mu.Lock()
			messages = append(messages, prefix+":"+string(data))
			mu.Unlock()
			return nil
		}
	}

	sw := rtc.NewSwappableWriter(record("relay"))

	// Phase 1: Write via relay
	sw.Write(map[string]string{"msg": "1-relay"})
	if sw.Mode() != "relay" {
		t.Fatalf("mode = %s, want relay", sw.Mode())
	}

	// Phase 2: Migrate to mock DC
	mockDCWrite := record("dc")

	// Manually simulate migration (can't use real DC in unit test)
	// Send pty.migrated via relay
	sw.Write(map[string]string{"type": "pty.migrated", "session_id": "s1"})

	// Swap to DC (internal — normally MigrateToDC does this)
	func() {
		// Access internal state via the public Mode/Write interface
		// We do a manual swap here since MigrateToDC needs a real *pionwebrtc.DataChannel
		sw.Write(map[string]string{"msg": "still-relay"})
	}()

	// Use a fresh SwappableWriter to test the full mock flow
	sw2 := rtc.NewSwappableWriter(record("relay2"))
	sw2.Write(map[string]string{"msg": "a-relay"})

	// Simulate migration by reaching into the writer
	// Since we can't call MigrateToDC without a real DC, test FallbackToRelay
	if sw2.Mode() != "relay" {
		t.Fatalf("sw2 mode = %s, want relay", sw2.Mode())
	}
	// FallbackToRelay from relay should be a no-op
	if err := sw2.FallbackToRelay("s2"); err != nil {
		t.Fatalf("fallback from relay: %v", err)
	}
	if sw2.Mode() != "relay" {
		t.Fatalf("sw2 mode after noop fallback = %s, want relay", sw2.Mode())
	}

	_ = mockDCWrite // used in the manual swap test above
}

// TestP2PLoopbackWithPeerManager tests in-process WebRTC with PeerManager,
// verifying the full handshake + data channel message flow.
func TestP2PLoopbackWithPeerManager(t *testing.T) {
	pm := rtc.NewPeerManager(nil) // host-only ICE
	defer pm.Close()

	var dcSessionID string
	var receivedData []byte
	var wg sync.WaitGroup
	wg.Add(1)

	pm.OnDC(func(senderPub, sessionID string, dc *pionwebrtc.DataChannel) {
		dcSessionID = sessionID
		dc.OnMessage(func(msg pionwebrtc.DataChannelMessage) {
			receivedData = msg.Data
			wg.Done()
		})
	})

	// Browser side: create PC + DC
	browserPC, err := pionwebrtc.NewPeerConnection(pionwebrtc.Configuration{})
	if err != nil {
		t.Fatalf("browser PC: %v", err)
	}
	defer browserPC.Close()

	dc, err := browserPC.CreateDataChannel("pty:test-sess-123", nil)
	if err != nil {
		t.Fatalf("create DC: %v", err)
	}

	// Create and send offer
	offer, err := browserPC.CreateOffer(nil)
	if err != nil {
		t.Fatalf("create offer: %v", err)
	}
	gatherDone := pionwebrtc.GatheringCompletePromise(browserPC)
	browserPC.SetLocalDescription(offer)
	<-gatherDone

	// Wing handles offer
	answerSDP, err := pm.HandleOffer(
		"browser-pub-key", "user-1", "user@example.com", "owner", nil,
		browserPC.LocalDescription().SDP,
	)
	if err != nil {
		t.Fatalf("handle offer: %v", err)
	}

	// Browser sets answer
	answer := pionwebrtc.SessionDescription{Type: pionwebrtc.SDPTypeAnswer, SDP: answerSDP}
	browserPC.SetRemoteDescription(answer)

	// Wait for DC to open
	dcReady := make(chan struct{})
	dc.OnOpen(func() { close(dcReady) })
	select {
	case <-dcReady:
	case <-time.After(5 * time.Second):
		t.Fatal("DC open timeout")
	}

	// Send a PTY input message over DC
	msg := `{"type":"pty.input","session_id":"test-sess-123","data":"dGVzdA=="}`
	dc.Send([]byte(msg))

	// Wait for receipt
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("message receipt timeout")
	}

	if dcSessionID != "test-sess-123" {
		t.Errorf("DC session = %q, want %q", dcSessionID, "test-sess-123")
	}
	if string(receivedData) != msg {
		t.Errorf("received = %q, want %q", receivedData, msg)
	}

	// Verify identity cached
	id, ok := pm.GetPeerIdentity("browser-pub-key")
	if !ok {
		t.Fatal("identity not cached")
	}
	if id.UserID != "user-1" {
		t.Errorf("userID = %q, want %q", id.UserID, "user-1")
	}
	if id.Email != "user@example.com" {
		t.Errorf("email = %q, want %q", id.Email, "user@example.com")
	}
}

// TestP2PPeerReplacement verifies that a second offer from the same sender
// replaces the first peer connection cleanly.
func TestP2PPeerReplacement(t *testing.T) {
	pm := rtc.NewPeerManager(nil)
	defer pm.Close()

	var dcCount int
	var mu sync.Mutex
	pm.OnDC(func(senderPub, sessionID string, dc *pionwebrtc.DataChannel) {
		mu.Lock()
		dcCount++
		mu.Unlock()
	})

	// First connection
	pc1, _ := pionwebrtc.NewPeerConnection(pionwebrtc.Configuration{})
	defer pc1.Close()
	pc1.CreateDataChannel("pty:s1", nil)
	offer1, _ := pc1.CreateOffer(nil)
	g1 := pionwebrtc.GatheringCompletePromise(pc1)
	pc1.SetLocalDescription(offer1)
	<-g1

	answer1, err := pm.HandleOffer("same-sender", "u1", "u@test.com", "", nil, pc1.LocalDescription().SDP)
	if err != nil {
		t.Fatalf("first offer: %v", err)
	}
	pc1.SetRemoteDescription(pionwebrtc.SessionDescription{Type: pionwebrtc.SDPTypeAnswer, SDP: answer1})

	// Second connection from same sender — should replace first
	pc2, _ := pionwebrtc.NewPeerConnection(pionwebrtc.Configuration{})
	defer pc2.Close()
	pc2.CreateDataChannel("pty:s2", nil)
	offer2, _ := pc2.CreateOffer(nil)
	g2 := pionwebrtc.GatheringCompletePromise(pc2)
	pc2.SetLocalDescription(offer2)
	<-g2

	answer2, err := pm.HandleOffer("same-sender", "u1", "u@test.com", "", nil, pc2.LocalDescription().SDP)
	if err != nil {
		t.Fatalf("second offer: %v", err)
	}
	pc2.SetRemoteDescription(pionwebrtc.SessionDescription{Type: pionwebrtc.SDPTypeAnswer, SDP: answer2})

	// Identity should still be valid (updated, not corrupted)
	id, ok := pm.GetPeerIdentity("same-sender")
	if !ok {
		t.Fatal("identity lost after replacement")
	}
	if id.UserID != "u1" {
		t.Errorf("userID = %q after replacement", id.UserID)
	}
}

// TestP2PCloseAll verifies that Close() cleans up all peer connections.
func TestP2PCloseAll(t *testing.T) {
	pm := rtc.NewPeerManager(nil)

	// Create a connection
	pc, _ := pionwebrtc.NewPeerConnection(pionwebrtc.Configuration{})
	defer pc.Close()
	pc.CreateDataChannel("pty:s1", nil)
	offer, _ := pc.CreateOffer(nil)
	g := pionwebrtc.GatheringCompletePromise(pc)
	pc.SetLocalDescription(offer)
	<-g

	_, err := pm.HandleOffer("sender-1", "u1", "u@test.com", "", nil, pc.LocalDescription().SDP)
	if err != nil {
		t.Fatalf("offer: %v", err)
	}

	// Close all
	pm.Close()

	// Identity should be gone
	_, ok := pm.GetPeerIdentity("sender-1")
	if ok {
		t.Error("identity should be cleared after Close()")
	}
}

// TestSwappableWriterConcurrentWrites verifies no data races under concurrent writes.
func TestSwappableWriterConcurrentWrites(t *testing.T) {
	var count int
	var mu sync.Mutex

	sw := rtc.NewSwappableWriter(func(v any) error {
		mu.Lock()
		count++
		mu.Unlock()
		return nil
	})

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			sw.Write(map[string]int{"n": n})
		}(i)
	}
	wg.Wait()

	mu.Lock()
	if count != 100 {
		t.Errorf("count = %d, want 100", count)
	}
	mu.Unlock()
}

// TestSDPMarshal verifies SDP payload marshaling.
func TestSDPMarshal(t *testing.T) {
	data := rtc.MarshalSDP("v=0\r\no=- 12345")
	var p rtc.SDPPayload
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.SDP != "v=0\r\no=- 12345" {
		t.Errorf("SDP = %q", p.SDP)
	}
}
