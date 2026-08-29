package webrtc

import (
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

func TestLoopbackWebRTC(t *testing.T) {
	pm := NewPeerManager(nil)
	defer pm.Close()

	var dcOpened atomic.Bool
	var receivedMsg []byte
	var wg sync.WaitGroup
	wg.Add(1)

	pm.OnDC(func(senderPub, sessionID string, _ PeerIdentity, dc *webrtc.DataChannel) {
		dcOpened.Store(true)
		if sessionID != "test-session" {
			t.Errorf("expected session_id 'test-session', got %q", sessionID)
		}
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			receivedMsg = msg.Data
			wg.Done()
		})
	})

	// Browser side: create a PeerConnection and a DataChannel
	browserPC, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("browser PC: %v", err)
	}
	defer func() { _ = browserPC.Close() }()

	dc, err := browserPC.CreateDataChannel("pty:test-session", nil)
	if err != nil {
		t.Fatalf("create data channel: %v", err)
	}

	// Create offer
	offer, err := browserPC.CreateOffer(nil)
	if err != nil {
		t.Fatalf("create offer: %v", err)
	}
	gatherDone := webrtc.GatheringCompletePromise(browserPC)
	if err := browserPC.SetLocalDescription(offer); err != nil {
		t.Fatalf("set local desc: %v", err)
	}
	<-gatherDone

	// Wing side: handle the offer
	answerSDP, err := pm.HandleOffer("sender-pub-key", "user1", "user@test.com", "owner", nil, browserPC.LocalDescription().SDP)
	if err != nil {
		t.Fatalf("handle offer: %v", err)
	}

	// Browser side: set remote description
	answer := webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answerSDP}
	if err := browserPC.SetRemoteDescription(answer); err != nil {
		t.Fatalf("set remote desc: %v", err)
	}

	// Wait for DC to open on browser side, then send a message
	dcReady := make(chan struct{})
	dc.OnOpen(func() { close(dcReady) })

	select {
	case <-dcReady:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for DC to open")
	}

	testMsg := []byte(`{"type":"pty.input","data":"hello"}`)
	if err := dc.Send(testMsg); err != nil {
		t.Fatalf("dc send: %v", err)
	}

	// Wait for message receipt on wing side
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for message")
	}

	if !dcOpened.Load() {
		t.Error("DC handler was never called")
	}
	if string(receivedMsg) != string(testMsg) {
		t.Errorf("received %q, want %q", receivedMsg, testMsg)
	}

	// Verify identity was cached
	id, ok := pm.GetPeerIdentity("sender-pub-key")
	if !ok {
		t.Fatal("peer identity not cached")
	}
	if id.UserID != "user1" || id.Email != "user@test.com" || id.OrgRole != "owner" {
		t.Errorf("unexpected identity: %+v", id)
	}
}

func TestInvalidReplacementOfferDoesNotEvictHealthyPeer(t *testing.T) {
	pm := NewPeerManager(nil)
	defer pm.Close()

	browserPC, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = browserPC.Close() }()
	if _, err := browserPC.CreateDataChannel("pty:session", nil); err != nil {
		t.Fatal(err)
	}
	offer, err := browserPC.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered := webrtc.GatheringCompletePromise(browserPC)
	if err := browserPC.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	<-gathered
	if _, err := pm.HandleOffer("sender-pub-key", "user1", "user@test.com", "owner", nil, browserPC.LocalDescription().SDP); err != nil {
		t.Fatalf("valid offer: %v", err)
	}
	pm.mu.Lock()
	if len(pm.peers) != 1 {
		got := len(pm.peers)
		pm.mu.Unlock()
		t.Fatalf("healthy peers = %d, want 1", got)
	}
	var healthy *webrtc.PeerConnection
	for _, peer := range pm.peers {
		healthy = peer
	}
	pm.mu.Unlock()

	if _, err := pm.HandleOffer("sender-pub-key", "user1", "user@test.com", "owner", nil, "not valid SDP"); err == nil {
		t.Fatal("malformed replacement offer was accepted")
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if len(pm.peers) != 1 {
		t.Fatalf("malformed replacement changed peer count to %d", len(pm.peers))
	}
	for _, peer := range pm.peers {
		if peer != healthy {
			t.Fatal("malformed replacement evicted the healthy peer")
		}
	}
}

func TestPeerManagerBoundsConcurrentOffersDeterministically(t *testing.T) {
	pm := NewPeerManager(nil)
	pm.maxPeers = 3
	pm.maxPerPeer = 2
	defer pm.Close()

	add := func(sender, suffix string) {
		t.Helper()
		pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
		if err != nil {
			t.Fatal(err)
		}
		if !pm.installPeer(sender+"\x00"+suffix, sender, PeerIdentity{UserID: sender}, pc) {
			t.Fatal("valid peer was not installed")
		}
	}
	add("sender-a", "1")
	add("sender-a", "2")
	add("sender-a", "3")

	pm.mu.Lock()
	if got := len(pm.peers); got != 2 {
		pm.mu.Unlock()
		t.Fatalf("peers after per-sender eviction = %d, want 2", got)
	}
	if _, exists := pm.peers["sender-a\x001"]; exists {
		pm.mu.Unlock()
		t.Fatal("oldest per-sender peer was not evicted")
	}
	pm.mu.Unlock()

	add("sender-b", "1")
	add("sender-b", "2")
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if got := len(pm.peers); got != 3 {
		t.Fatalf("total peers = %d, want 3", got)
	}
	if _, exists := pm.peers["sender-a\x002"]; exists {
		t.Fatal("oldest total peer was not evicted")
	}
	if _, exists := pm.identities["sender-a"]; !exists {
		t.Fatal("sender identity was removed while another peer remained")
	}
}

func TestPeerManagerDoesNotInstallClosedPeer(t *testing.T) {
	pm := NewPeerManager(nil)
	defer pm.Close()
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	if err := pc.Close(); err != nil {
		t.Fatal(err)
	}
	if pm.installPeer("sender\x00closed", "sender", PeerIdentity{UserID: "user"}, pc) {
		t.Fatal("closed peer was installed")
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if len(pm.peers) != 0 || len(pm.identities) != 0 {
		t.Fatalf("closed peer left manager state: peers=%d identities=%d", len(pm.peers), len(pm.identities))
	}
}

func TestPeerManagerCloseRejectsLateAndFutureInstalls(t *testing.T) {
	pm := NewPeerManager(nil)
	pm.Close()
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	if pm.installPeer("sender\x00late", "sender", PeerIdentity{UserID: "user"}, pc) {
		t.Fatal("peer installed after manager shutdown")
	}
	if _, err := pm.HandleOffer("sender", "user", "", "member", nil, "v=0\r\n"); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("post-close offer error = %v", err)
	}
}

func TestPeerManagerBoundsOfferInputAndWorkInFlight(t *testing.T) {
	pm := NewPeerManager(nil)
	defer pm.Close()

	if _, err := pm.HandleOffer("short", "user", "", "member", nil, strings.Repeat("x", maxSDPOfferBytes+1)); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized SDP error = %v", err)
	}
	for range cap(pm.offerSlots) {
		pm.offerSlots <- struct{}{}
	}
	if _, err := pm.HandleOffer("short", "user", "", "member", nil, "v=0\r\n"); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Fatalf("saturated offer error = %v", err)
	}
	for range cap(pm.offerSlots) {
		<-pm.offerSlots
	}
	if got := logPrefix("tiny"); got != "tiny" {
		t.Fatalf("short log prefix = %q", got)
	}
}

func TestSwappableWriterOrdering(t *testing.T) {
	var messages []string
	var mu sync.Mutex

	relayWrite := func(v any) error {
		data, _ := json.Marshal(v)
		mu.Lock()
		messages = append(messages, "relay:"+string(data))
		mu.Unlock()
		return nil
	}

	sw := NewSwappableWriter(relayWrite)

	// Write via relay
	if err := sw.Write(map[string]string{"msg": "1"}); err != nil {
		t.Fatal(err)
	}
	if sw.Mode() != "relay" {
		t.Errorf("mode = %s, want relay", sw.Mode())
	}

	// Create a mock DC
	mockDC := make(chan []byte, 10)
	mockDCWrite := func(v any) error {
		data, _ := json.Marshal(v)
		mu.Lock()
		messages = append(messages, "dc:"+string(data))
		mu.Unlock()
		mockDC <- data
		return nil
	}

	// Migrate — this sends pty.migrated via relay and swaps
	// We can't use MigrateToDC (needs real DC), so test the write swap manually
	sw.mu.Lock()
	if err := sw.relayWrite(map[string]string{"type": "pty.migrated", "session_id": "s1"}); err != nil {
		t.Fatal(err)
	}
	sw.dcWrite = mockDCWrite
	sw.mode = "p2p"
	sw.mu.Unlock()

	// Write via DC
	if err := sw.Write(map[string]string{"msg": "2"}); err != nil {
		t.Fatal(err)
	}
	if sw.Mode() != "p2p" {
		t.Errorf("mode = %s, want p2p", sw.Mode())
	}

	// Fallback
	if err := sw.FallbackToRelay("s1"); err != nil {
		t.Fatal(err)
	}
	if err := sw.Write(map[string]string{"msg": "3"}); err != nil {
		t.Fatal(err)
	}
	if sw.Mode() != "relay" {
		t.Errorf("mode = %s, want relay", sw.Mode())
	}

	mu.Lock()
	defer mu.Unlock()

	// Verify ordering: relay(1), relay(migrated), dc(2), relay(fallback), relay(3)
	if len(messages) != 5 {
		t.Fatalf("expected 5 messages, got %d: %v", len(messages), messages)
	}

	// First message via relay
	if messages[0][:6] != "relay:" {
		t.Errorf("msg 0: expected relay, got %s", messages[0])
	}
	// pty.migrated via relay
	if messages[1][:6] != "relay:" {
		t.Errorf("msg 1: expected relay (migrated), got %s", messages[1])
	}
	// msg 2 via DC
	if messages[2][:3] != "dc:" {
		t.Errorf("msg 2: expected dc, got %s", messages[2])
	}
	// pty.fallback via relay
	if messages[3][:6] != "relay:" {
		t.Errorf("msg 3: expected relay (fallback), got %s", messages[3])
	}
	// msg 3 via relay
	if messages[4][:6] != "relay:" {
		t.Errorf("msg 4: expected relay, got %s", messages[4])
	}
}
