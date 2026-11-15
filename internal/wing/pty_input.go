package wing

import (
	"context"
	"crypto/cipher"
	"encoding/json"
	"fmt"

	"github.com/ehrlich-b/wingthing/internal/auth"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

// Queued input retains its sender across a rekey. Refusals must carry that
// sender's reply ID so they cannot be mistaken for the replacement's errors.
func deliverBrowserInput(sessionID, controllerID string, msg queuedBrowserInput, gcm cipher.AEAD, stream pb.Egg_SessionClient, sessions *wingsession.Service, write ws.PTYWriteFunc) error {
	if msg.Origin != "" {
		origin := takeInputOrigin(msg.Origin)
		if origin == nil {
			// Already refused, expired or disconnected; never fall back.
			return nil
		}
		write = func(value any) error {
			if denied, ok := value.(ws.ErrorMsg); ok {
				sendDataChannelRefusal(origin.dc, &denied)
			}
			return nil
		}
		msg.ControllerID = origin.controller.id
		if origin.session != sessionID || !currentBrowserInput(sessionID, origin.controller) {
			return write(ws.ErrorMsg{Type: ws.TypeError, SessionID: sessionID, ControllerID: msg.ControllerID, Operation: ws.TypePTYInput, Code: "controller_replaced", Message: "terminal is controlled elsewhere; input was not delivered"})
		}
	}
	refuse := func(code, message string) error {
		return write(ws.ErrorMsg{Type: ws.TypeError, SessionID: sessionID, ControllerID: msg.ControllerID, RequestID: msg.RequestID, Operation: ws.TypePTYInput, Code: code, Message: message})
	}
	if msg.ControllerID != "" && msg.ControllerID != controllerID {
		return refuse("controller_replaced", "terminal is controlled elsewhere; input was not delivered")
	}
	if gcm == nil || stream == nil {
		return refuse("session_not_ready", "terminal is not ready; input was not delivered")
	}
	decoded, err := auth.Decrypt(gcm, msg.Data)
	if err != nil {
		return refuse("input_decrypt_failed", "terminal key changed; input was not delivered")
	}
	if err := sessions.Input(stream, sessionID, decoded); err != nil {
		if reportErr := refuse("session_input_failed", "terminal refused input; delivery could not be confirmed"); reportErr != nil {
			return fmt.Errorf("report input failure: %w", reportErr)
		}
		if msg.Origin != "" {
			return nil
		}
		return err
	}
	return nil
}

// Retire admission first: no producer may enqueue behind the final drain.
func retirePTYInput(ctx context.Context, sessionID string, input <-chan []byte, write ws.PTYWriteFunc) {
	ws.RetirePTYInput(ctx)
	for {
		select {
		case data, ok := <-input:
			if !ok {
				return
			}
			refusePTYMessage(sessionID, data, write, "session_not_ready", "terminal is not ready; input was not delivered")
		default:
			return
		}
	}
}

// Input and attach refusals keep the original operation and routing fields.
// A missing direct origin never falls back to the current relay subscriber.
func refusePTYMessage(sessionID string, data []byte, write ws.PTYWriteFunc, code, message string) {
	var msg struct {
		queuedBrowserInput
		ViewerID string `json:"viewer_id"`
	}
	if json.Unmarshal(data, &msg) != nil {
		return
	}
	if msg.Type != ws.TypePTYInput && msg.Type != ws.TypePTYAttach {
		return
	}
	denied := ws.ErrorMsg{Type: ws.TypeError, SessionID: sessionID, Operation: msg.Type, ControllerID: msg.ControllerID, RequestID: msg.RequestID, ViewerID: msg.ViewerID, Code: code, Message: message}
	if msg.Origin != "" {
		if origin := takeInputOrigin(msg.Origin); origin != nil {
			denied.ControllerID = origin.controller.id
			denied.RequestID = ""
			sendDataChannelRefusal(origin.dc, &denied)
		}
		return
	}
	ws.WritePTYMessage(write, denied)
}
