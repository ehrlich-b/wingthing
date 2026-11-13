package wing

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	webrtcpkg "github.com/ehrlich-b/wingthing/internal/webrtc"
	"github.com/ehrlich-b/wingthing/internal/ws"
	pionwebrtc "github.com/pion/webrtc/v4"
)

func TestPreviewReattachedControllerClosesSurvivingDataChannel(t *testing.T) {
	oldChannel := config.ReleaseChannel
	config.ReleaseChannel = "preview"
	t.Cleanup(func() { config.ReleaseChannel = oldChannel })
	pc, err := pionwebrtc.NewPeerConnection(pionwebrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	dc, err := pc.CreateDataChannel("pty:session", nil)
	if err != nil {
		t.Fatal(err)
	}
	var sessions sync.Map
	sessions.Store("session", dc)
	controller := &browserInputController{id: "before-reconnect", publicKey: "tab", userID: "owner"}
	browserInputs.Store("session", controller)
	t.Cleanup(func() { browserInputs.Delete("session") })
	var inputs []string
	handler := browserDataChannelInputHandler(&sessions, "session", dc, controller, func(sessionID string, data []byte) *ws.ErrorMsg {
		if sessionID != "session" {
			t.Fatalf("input reached wrong session: %s", sessionID)
		}
		inputs = append(inputs, string(data))
		return nil
	})
	handler(pionwebrtc.DataChannelMessage{Data: []byte("current")})
	if len(inputs) != 1 || inputs[0] != "current" {
		t.Fatal("current controller input was dropped")
	}
	// The relay reconnect installs a new controller for the same browser tab,
	// while its direct transport survives with the old immutable binding.
	replacement := &browserInputController{id: "after-reconnect", publicKey: "tab", userID: "owner"}
	browserInputs.Store("session", replacement)
	handler(pionwebrtc.DataChannelMessage{Data: []byte("stale")})
	if len(inputs) != 1 {
		t.Fatal("stale DataChannel borrowed the replacement's input lease")
	}
	if state := dc.ReadyState(); state != pionwebrtc.DataChannelStateClosed && state != pionwebrtc.DataChannelStateClosing {
		t.Fatalf("stale DataChannel still accepts sends instead of triggering relay fallback: %s", state)
	}
	if !currentBrowserInput("session", replacement) {
		t.Fatal("closing stale transport displaced the replacement controller")
	}
}

// Exercise the real handler and queue over a host-only WebRTC connection. There
// is no relay connection: refusals must reach the browser on this data channel.
func TestDataChannelFullAndMissingQueueRefusals(t *testing.T) {
	for _, channel := range []string{"stable", "preview"} {
		t.Run(channel, func(t *testing.T) {
			oldChannel := config.ReleaseChannel
			config.ReleaseChannel = channel
			t.Cleanup(func() { config.ReleaseChannel = oldChannel })
			client := &ws.Client{}
			_, input, cleanup, ok := client.RegisterPTYSession(t.Context(), "session")
			if !ok {
				t.Fatal("register session")
			}
			t.Cleanup(cleanup)
			for range 64 {
				if refusal := client.PushPTYInput("session", []byte("queued")); refusal != nil {
					t.Fatalf("fill queue: %+v", refusal)
				}
			}
			controller := &browserInputController{id: "controller", publicKey: "tab", userID: "owner"}
			browserInputs.Store("session", controller)
			t.Cleanup(func() { browserInputs.Delete("session") })
			var sessions sync.Map
			manager := webrtcpkg.NewPeerManager(nil)
			t.Cleanup(manager.Close)
			manager.OnDC(func(_, _ string, _ webrtcpkg.PeerIdentity, dc *pionwebrtc.DataChannel) {
				sessions.Store("session", dc)
				dc.OnMessage(browserDataChannelInputHandler(&sessions, "session", dc, controller, client.PushPTYInput))
			})
			pc, err := pionwebrtc.NewPeerConnection(pionwebrtc.Configuration{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = pc.Close() })
			dc, err := pc.CreateDataChannel("pty:session", nil)
			if err != nil {
				t.Fatal(err)
			}
			opened := make(chan struct{})
			dc.OnOpen(func() { close(opened) })
			replies := make(chan []byte, 4)
			dc.OnMessage(func(msg pionwebrtc.DataChannelMessage) { replies <- msg.Data })
			offer, err := pc.CreateOffer(nil)
			if err != nil {
				t.Fatal(err)
			}
			gathered := pionwebrtc.GatheringCompletePromise(pc)
			if err := pc.SetLocalDescription(offer); err != nil {
				t.Fatal(err)
			}
			select {
			case <-gathered:
			case <-time.After(5 * time.Second):
				t.Fatal("ICE gathering timeout")
			}
			answer, err := manager.HandleOffer("tab", "owner", "", "owner", nil, pc.LocalDescription().SDP)
			if err != nil {
				t.Fatal(err)
			}
			if err := pc.SetRemoteDescription(pionwebrtc.SessionDescription{Type: pionwebrtc.SDPTypeAnswer, SDP: answer}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-opened:
			case <-time.After(5 * time.Second):
				t.Fatal("data channel open timeout")
			}
			send := func() {
				t.Helper()
				if err := dc.SendText(`{"type":"pty.input","session_id":"session","data":"numbered-input","request_id":"probe"}`); err != nil {
					t.Fatal(err)
				}
			}
			checkRefusal := func(code string) {
				t.Helper()
				select {
				case data := <-replies:
					var refusal ws.ErrorMsg
					if err := json.Unmarshal(data, &refusal); err != nil {
						t.Fatal(err)
					}
					if refusal.Type != ws.TypeError || refusal.SessionID != "session" || refusal.Operation != ws.TypePTYInput || refusal.Code != code || refusal.RequestID != "probe" {
						t.Fatalf("refusal = %+v", refusal)
					}
				case <-time.After(time.Second):
					t.Fatal("data channel input disappeared without a refusal")
				}
			}
			began := time.Now()
			send()
			checkRefusal("session_input_busy")
			if elapsed := time.Since(began); elapsed < 200*time.Millisecond {
				t.Fatalf("full queue refused without bounded wait: %s", elapsed)
			}
			for range 64 {
				if got := string(<-input); got != "queued" {
					t.Fatalf("refused input corrupted queued messages: %q", got)
				}
			}
			send()
			select {
			case data := <-input:
				if !strings.Contains(string(data), "numbered-input") {
					t.Fatalf("delivered input = %q", data)
				}
			case <-time.After(time.Second):
				t.Fatal("available queue did not receive input")
			}
			cleanup()
			send()
			checkRefusal("session_not_found")
		})
	}
}
