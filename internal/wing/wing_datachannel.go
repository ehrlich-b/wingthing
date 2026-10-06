package wing

import (
	"log"
	"sync"

	pionwebrtc "github.com/pion/webrtc/v4"
)

func browserDataChannelInputHandler(sessions *sync.Map, sessionID string, dc *pionwebrtc.DataChannel, controller *browserInputController, pushInput func(string, []byte) bool) func(pionwebrtc.DataChannelMessage) {
	return func(msg pionwebrtc.DataChannelMessage) {
		if !currentDataChannel(sessions, sessionID, dc) {
			return
		}
		if !currentBrowserInput(sessionID, controller) {
			// Reattachment can replace the input lease while WebRTC survives.
			// Closing this stale binding lets the browser send via the relay.
			if err := dc.Close(); err != nil {
				log.Printf("[P2P] close stale controller channel for session %s: %v", sessionID, err)
			}
			return
		}
		pushInput(sessionID, msg.Data)
	}
}
