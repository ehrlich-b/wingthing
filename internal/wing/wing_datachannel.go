package wing

import (
	"encoding/json"
	"log"
	"sync"

	"github.com/ehrlich-b/wingthing/internal/ws"

	pionwebrtc "github.com/pion/webrtc/v4"
)

func browserDataChannelInputHandler(sessions *sync.Map, sessionID string, dc *pionwebrtc.DataChannel, controller *browserInputController, pushInput func(string, []byte) *ws.ErrorMsg) func(pionwebrtc.DataChannelMessage) {
	dc.OnClose(func() { releaseDataChannelInputOrigins(dc) })
	return func(msg pionwebrtc.DataChannelMessage) {
		if !currentDataChannel(sessions, sessionID, dc) {
			refuseDataChannelInput(dc, sessionID, "controller_replaced", "terminal connection changed; input was not delivered")
			return
		}
		if !currentBrowserInput(sessionID, controller) {
			refuseDataChannelInput(dc, sessionID, "controller_replaced", "terminal is controlled elsewhere; input was not delivered")
			// Reattachment can replace the input lease while WebRTC survives.
			// Closing this stale binding lets the browser send via the relay.
			if err := dc.Close(); err != nil {
				log.Printf("[P2P] close stale controller channel for session %s: %v", sessionID, err)
			}
			return
		}
		var input queuedBrowserInput
		if json.Unmarshal(msg.Data, &input) != nil || input.Type != ws.TypePTYInput {
			// Other messages still use their existing session queue handling.
			if denied := pushInput(sessionID, msg.Data); denied != nil {
				sendDataChannelRefusal(dc, denied)
			}
			return
		}
		input.SessionID = sessionID
		input.ControllerID = controller.id
		input.Origin = retainInputOrigin(sessionID, controller, dc)
		if input.Origin == "" {
			refuseDataChannelInput(dc, sessionID, "session_input_busy", "session input is busy; message was not delivered")
			return
		}
		data, _ := json.Marshal(input)
		if denied := pushInput(sessionID, data); denied != nil {
			takeInputOrigin(input.Origin)
			denied.ControllerID = controller.id
			sendDataChannelRefusal(dc, denied)
		}
	}
}

func refuseDataChannelInput(dc *pionwebrtc.DataChannel, sessionID, code, message string) {
	sendDataChannelRefusal(dc, &ws.ErrorMsg{Type: ws.TypeError, SessionID: sessionID, Operation: ws.TypePTYInput, Code: code, Message: message})
}

func sendDataChannelRefusal(dc *pionwebrtc.DataChannel, denied *ws.ErrorMsg) {
	data, err := json.Marshal(denied)
	if err == nil {
		err = dc.SendText(string(data))
	}
	if err != nil {
		log.Printf("[P2P] report input refusal for session %s: %v", denied.SessionID, err)
		// A failed refusal must be visible as a transport loss at the browser.
		_ = dc.Close()
	}
}
