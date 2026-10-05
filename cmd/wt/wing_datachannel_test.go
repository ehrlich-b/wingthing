package main

import (
	"sync"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
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
	handler := browserDataChannelInputHandler(&sessions, "session", dc, controller, func(sessionID string, data []byte) bool {
		if sessionID != "session" {
			t.Fatalf("input reached wrong session: %s", sessionID)
		}
		inputs = append(inputs, string(data))
		return true
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
