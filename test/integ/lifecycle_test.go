//go:build e2e

package integ

import (
	"context"
	"encoding/base64"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/ehrlich-b/wingthing/internal/ws"
)

func TestPTYSessionLifecycle(t *testing.T) {
	_, ts, store := testRelayAndWS(t)
	token, _ := createTestUser(t, store, "pty1")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Connect wing WebSocket and register
	wingConn, _, err := websocket.Dial(ctx, wsURL(ts)+"/ws/wing?token="+token, nil)
	if err != nil {
		t.Fatalf("dial wing ws: %v", err)
	}
	defer wingConn.CloseNow()

	reg := ws.WingRegister{
		Type:     ws.TypeWingRegister,
		WingID:   "pty-wing-1",
		Hostname: "testhost",
		Agents:   []string{"claude"},
	}
	if err := wsjson.Write(ctx, wingConn, reg); err != nil {
		t.Fatalf("write wing.register: %v", err)
	}

	var ack ws.RegisteredMsg
	if err := wsjson.Read(ctx, wingConn, &ack); err != nil {
		t.Fatalf("read registered ack: %v", err)
	}

	// Connect browser WebSocket
	browserConn, _, err := websocket.Dial(ctx, wsURL(ts)+"/ws/pty?token="+token+"&wing_id=pty-wing-1", nil)
	if err != nil {
		t.Fatalf("dial browser ws: %v", err)
	}
	defer browserConn.CloseNow()

	// Browser sends pty.start
	start := ws.PTYStart{
		Type:   ws.TypePTYStart,
		Agent:  "claude",
		WingID: "pty-wing-1",
		Cols:   80,
		Rows:   24,
	}
	if err := wsjson.Write(ctx, browserConn, start); err != nil {
		t.Fatalf("write pty.start: %v", err)
	}

	// Wing receives pty.start (relay assigns session_id and injects user info)
	var wingStart ws.PTYStart
	if err := wsjson.Read(ctx, wingConn, &wingStart); err != nil {
		t.Fatalf("wing read pty.start: %v", err)
	}
	if wingStart.Type != ws.TypePTYStart {
		t.Fatalf("expected pty.start, got %s", wingStart.Type)
	}
	if wingStart.SessionID == "" {
		t.Fatal("expected relay to assign session_id")
	}
	if wingStart.Agent != "claude" {
		t.Errorf("expected agent claude, got %s", wingStart.Agent)
	}
	sessionID := wingStart.SessionID

	// Wing sends pty.started
	started := ws.PTYStarted{
		Type:      ws.TypePTYStarted,
		SessionID: sessionID,
		Agent:     "claude",
	}
	if err := wsjson.Write(ctx, wingConn, started); err != nil {
		t.Fatalf("write pty.started: %v", err)
	}

	// Browser receives pty.started
	var browserStarted ws.PTYStarted
	if err := wsjson.Read(ctx, browserConn, &browserStarted); err != nil {
		t.Fatalf("browser read pty.started: %v", err)
	}
	if browserStarted.Type != ws.TypePTYStarted {
		t.Fatalf("expected pty.started, got %s", browserStarted.Type)
	}
	if browserStarted.SessionID != sessionID {
		t.Errorf("session ID mismatch: want %s, got %s", sessionID, browserStarted.SessionID)
	}

	// Wing sends pty.output
	outputData := base64.StdEncoding.EncodeToString([]byte("hello from mock agent\n"))
	output := ws.PTYOutput{
		Type:      ws.TypePTYOutput,
		SessionID: sessionID,
		Data:      outputData,
	}
	if err := wsjson.Write(ctx, wingConn, output); err != nil {
		t.Fatalf("write pty.output: %v", err)
	}

	// Browser receives pty.output
	var browserOutput ws.PTYOutput
	if err := wsjson.Read(ctx, browserConn, &browserOutput); err != nil {
		t.Fatalf("browser read pty.output: %v", err)
	}
	if browserOutput.Type != ws.TypePTYOutput {
		t.Fatalf("expected pty.output, got %s", browserOutput.Type)
	}
	decoded, err := base64.StdEncoding.DecodeString(browserOutput.Data)
	if err != nil {
		t.Fatalf("decode output data: %v", err)
	}
	if string(decoded) != "hello from mock agent\n" {
		t.Errorf("expected %q, got %q", "hello from mock agent\n", string(decoded))
	}

	// Browser sends pty.input
	inputData := base64.StdEncoding.EncodeToString([]byte("test input\n"))
	input := ws.PTYInput{
		Type:      ws.TypePTYInput,
		SessionID: sessionID,
		Data:      inputData,
	}
	if err := wsjson.Write(ctx, browserConn, input); err != nil {
		t.Fatalf("write pty.input: %v", err)
	}

	// Wing receives pty.input
	var wingInput ws.PTYInput
	if err := wsjson.Read(ctx, wingConn, &wingInput); err != nil {
		t.Fatalf("wing read pty.input: %v", err)
	}
	if wingInput.Type != ws.TypePTYInput {
		t.Fatalf("expected pty.input, got %s", wingInput.Type)
	}
	decodedInput, err := base64.StdEncoding.DecodeString(wingInput.Data)
	if err != nil {
		t.Fatalf("decode input data: %v", err)
	}
	if string(decodedInput) != "test input\n" {
		t.Errorf("expected %q, got %q", "test input\n", string(decodedInput))
	}

	// Wing sends pty.exited
	exited := ws.PTYExited{
		Type:      ws.TypePTYExited,
		SessionID: sessionID,
		ExitCode:  0,
	}
	if err := wsjson.Write(ctx, wingConn, exited); err != nil {
		t.Fatalf("write pty.exited: %v", err)
	}

	// Browser receives pty.exited
	var browserExited ws.PTYExited
	if err := wsjson.Read(ctx, browserConn, &browserExited); err != nil {
		t.Fatalf("browser read pty.exited: %v", err)
	}
	if browserExited.Type != ws.TypePTYExited {
		t.Fatalf("expected pty.exited, got %s", browserExited.Type)
	}
	if browserExited.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", browserExited.ExitCode)
	}
}
