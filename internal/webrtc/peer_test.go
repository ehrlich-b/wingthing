package webrtc

import (
	"encoding/json"
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

	pm.OnDC(func(senderPub, sessionID string, dc *webrtc.DataChannel) {
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
	defer browserPC.Close()

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
	sw.Write(map[string]string{"msg": "1"})
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
	sw.relayWrite(map[string]string{"type": "pty.migrated", "session_id": "s1"})
	sw.dcWrite = mockDCWrite
	sw.mode = "p2p"
	sw.mu.Unlock()

	// Write via DC
	sw.Write(map[string]string{"msg": "2"})
	if sw.Mode() != "p2p" {
		t.Errorf("mode = %s, want p2p", sw.Mode())
	}

	// Fallback
	sw.FallbackToRelay("s1")
	sw.Write(map[string]string{"msg": "3"})
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
