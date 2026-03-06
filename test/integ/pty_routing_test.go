//go:build e2e

package integ

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/ehrlich-b/wingthing/internal/ws"
)

// connectWing creates and registers a wing WebSocket. Returns the conn and assigned connection ID.
func connectWing(t *testing.T, tsURL, token, wingID string, agents []string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, tsURL+"/ws/wing?token="+token, nil)
	if err != nil {
		t.Fatalf("dial wing ws: %v", err)
	}

	reg := ws.WingRegister{
		Type:     ws.TypeWingRegister,
		WingID:   wingID,
		Hostname: "testhost",
		Agents:   agents,
	}
	if err := wsjson.Write(ctx, conn, reg); err != nil {
		conn.CloseNow()
		t.Fatalf("write wing.register: %v", err)
	}

	var ack ws.RegisteredMsg
	if err := wsjson.Read(ctx, conn, &ack); err != nil {
		conn.CloseNow()
		t.Fatalf("read registered ack: %v", err)
	}

	return conn
}

// connectBrowser creates a browser WebSocket connected to a wing.
func connectBrowser(t *testing.T, tsURL, token, wingID string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, tsURL+"/ws/pty?token="+token+"&wing_id="+wingID, nil)
	if err != nil {
		t.Fatalf("dial browser ws: %v", err)
	}
	return conn
}

// startSession sends pty.start from browser and reads pty.start on wing.
// Returns the relay-assigned session ID.
func startSession(t *testing.T, browserConn, wingConn *websocket.Conn, agent, wingID string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := ws.PTYStart{
		Type:   ws.TypePTYStart,
		Agent:  agent,
		WingID: wingID,
		Cols:   80,
		Rows:   24,
	}
	if err := wsjson.Write(ctx, browserConn, start); err != nil {
		t.Fatalf("write pty.start: %v", err)
	}

	var wingStart ws.PTYStart
	if err := wsjson.Read(ctx, wingConn, &wingStart); err != nil {
		t.Fatalf("wing read pty.start: %v", err)
	}
	if wingStart.SessionID == "" {
		t.Fatal("expected relay to assign session_id")
	}
	return wingStart.SessionID
}

func TestPTYRoutingMultipleSessions(t *testing.T) {
	_, ts, store := testRelayAndWS(t)
	token, _ := createTestUser(t, store, "multi-sess")

	wingConn := connectWing(t, wsURL(ts), token, "wing-multi", []string{"claude"})
	defer wingConn.CloseNow()

	// Start two sessions from two different browsers
	browser1 := connectBrowser(t, wsURL(ts), token, "wing-multi")
	defer browser1.CloseNow()
	browser2 := connectBrowser(t, wsURL(ts), token, "wing-multi")
	defer browser2.CloseNow()

	sess1 := startSession(t, browser1, wingConn, "claude", "wing-multi")
	sess2 := startSession(t, browser2, wingConn, "claude", "wing-multi")

	if sess1 == sess2 {
		t.Fatal("sessions should have different IDs")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Wing sends output to session 1 — only browser 1 should get it
	out1Data := base64.StdEncoding.EncodeToString([]byte("session-1-output"))
	wsjson.Write(ctx, wingConn, ws.PTYOutput{Type: ws.TypePTYOutput, SessionID: sess1, Data: out1Data})

	// Wing sends output to session 2 — only browser 2 should get it
	out2Data := base64.StdEncoding.EncodeToString([]byte("session-2-output"))
	wsjson.Write(ctx, wingConn, ws.PTYOutput{Type: ws.TypePTYOutput, SessionID: sess2, Data: out2Data})

	// Browser 1 reads output — should be session 1
	var b1out ws.PTYOutput
	if err := wsjson.Read(ctx, browser1, &b1out); err != nil {
		t.Fatalf("browser1 read: %v", err)
	}
	b1decoded, _ := base64.StdEncoding.DecodeString(b1out.Data)
	if string(b1decoded) != "session-1-output" {
		t.Errorf("browser1 got %q, want %q", b1decoded, "session-1-output")
	}

	// Browser 2 reads output — should be session 2
	var b2out ws.PTYOutput
	if err := wsjson.Read(ctx, browser2, &b2out); err != nil {
		t.Fatalf("browser2 read: %v", err)
	}
	b2decoded, _ := base64.StdEncoding.DecodeString(b2out.Data)
	if string(b2decoded) != "session-2-output" {
		t.Errorf("browser2 got %q, want %q", b2decoded, "session-2-output")
	}
}

func TestPTYRoutingWingOffline(t *testing.T) {
	_, ts, store := testRelayAndWS(t)
	token, _ := createTestUser(t, store, "offline")

	wingConn := connectWing(t, wsURL(ts), token, "wing-offline", []string{"claude"})
	browser := connectBrowser(t, wsURL(ts), token, "wing-offline")
	defer browser.CloseNow()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sess := startSession(t, browser, wingConn, "claude", "wing-offline")
	_ = sess

	// Wing sends pty.started so the session is fully active
	wsjson.Write(ctx, wingConn, ws.PTYStarted{
		Type: ws.TypePTYStarted, SessionID: sess, Agent: "claude",
	})
	var started ws.PTYStarted
	wsjson.Read(ctx, browser, &started)

	// Wing disconnects
	wingConn.Close(websocket.StatusNormalClosure, "going offline")

	// Browser should receive wing.offline
	var msg ws.Envelope
	if err := wsjson.Read(ctx, browser, &msg); err != nil {
		t.Fatalf("browser read after wing disconnect: %v", err)
	}
	if msg.Type != ws.TypeWingOffline {
		t.Errorf("expected wing.offline, got %s", msg.Type)
	}
}

func TestPTYRoutingNoWingConnected(t *testing.T) {
	_, ts, store := testRelayAndWS(t)
	token, _ := createTestUser(t, store, "no-wing")

	browser := connectBrowser(t, wsURL(ts), token, "nonexistent-wing")
	defer browser.CloseNow()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Try to start a session — should get error
	start := ws.PTYStart{
		Type:   ws.TypePTYStart,
		Agent:  "claude",
		WingID: "nonexistent-wing",
		Cols:   80,
		Rows:   24,
	}
	if err := wsjson.Write(ctx, browser, start); err != nil {
		t.Fatalf("write pty.start: %v", err)
	}

	var errMsg ws.ErrorMsg
	if err := wsjson.Read(ctx, browser, &errMsg); err != nil {
		t.Fatalf("read error: %v", err)
	}
	if errMsg.Type != ws.TypeError {
		t.Fatalf("expected error, got %s", errMsg.Type)
	}
	if errMsg.Message != "no wing connected" {
		t.Errorf("message = %q, want %q", errMsg.Message, "no wing connected")
	}
}

func TestPTYRoutingExitCleanup(t *testing.T) {
	_, ts, store := testRelayAndWS(t)
	token, _ := createTestUser(t, store, "exit-cleanup")

	wingConn := connectWing(t, wsURL(ts), token, "wing-exit", []string{"claude"})
	defer wingConn.CloseNow()
	browser := connectBrowser(t, wsURL(ts), token, "wing-exit")
	defer browser.CloseNow()

	sess := startSession(t, browser, wingConn, "claude", "wing-exit")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Send pty.started
	wsjson.Write(ctx, wingConn, ws.PTYStarted{Type: ws.TypePTYStarted, SessionID: sess, Agent: "claude"})
	var started ws.PTYStarted
	wsjson.Read(ctx, browser, &started)

	// Send pty.exited with exit code 1
	wsjson.Write(ctx, wingConn, ws.PTYExited{Type: ws.TypePTYExited, SessionID: sess, ExitCode: 1})

	var exited ws.PTYExited
	if err := wsjson.Read(ctx, browser, &exited); err != nil {
		t.Fatalf("read pty.exited: %v", err)
	}
	if exited.ExitCode != 1 {
		t.Errorf("exit code = %d, want 1", exited.ExitCode)
	}

	// After exit, output to the same session should be dropped (no route)
	// Send another output — browser should NOT receive it (route was cleaned up)
	outData := base64.StdEncoding.EncodeToString([]byte("ghost-output"))
	wsjson.Write(ctx, wingConn, ws.PTYOutput{Type: ws.TypePTYOutput, SessionID: sess, Data: outData})

	// Try to read with short timeout — should timeout (no message)
	shortCtx, shortCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer shortCancel()
	var ghost ws.PTYOutput
	err := wsjson.Read(shortCtx, browser, &ghost)
	if err == nil {
		t.Error("expected no message after exit, but got one")
	}
}

func TestPTYRoutingDetach(t *testing.T) {
	_, ts, store := testRelayAndWS(t)
	token, _ := createTestUser(t, store, "detach")

	wingConn := connectWing(t, wsURL(ts), token, "wing-detach", []string{"claude"})
	defer wingConn.CloseNow()
	browser := connectBrowser(t, wsURL(ts), token, "wing-detach")
	defer browser.CloseNow()

	sess := startSession(t, browser, wingConn, "claude", "wing-detach")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Send pty.started
	wsjson.Write(ctx, wingConn, ws.PTYStarted{Type: ws.TypePTYStarted, SessionID: sess, Agent: "claude"})
	var started ws.PTYStarted
	wsjson.Read(ctx, browser, &started)

	// Browser sends pty.detach
	detach := ws.PTYDetach{Type: ws.TypePTYDetach, SessionID: sess}
	wsjson.Write(ctx, browser, detach)

	// Small delay for relay to process detach
	time.Sleep(100 * time.Millisecond)

	// Wing sends output — browser should NOT get it (detached)
	outData := base64.StdEncoding.EncodeToString([]byte("detached-output"))
	wsjson.Write(ctx, wingConn, ws.PTYOutput{Type: ws.TypePTYOutput, SessionID: sess, Data: outData})

	shortCtx, shortCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer shortCancel()
	var ghost ws.PTYOutput
	err := wsjson.Read(shortCtx, browser, &ghost)
	if err == nil {
		t.Error("expected no message after detach, but got one")
	}
}

func TestPTYRoutingInputForwarding(t *testing.T) {
	_, ts, store := testRelayAndWS(t)
	token, _ := createTestUser(t, store, "input-fwd")

	wingConn := connectWing(t, wsURL(ts), token, "wing-input", []string{"claude"})
	defer wingConn.CloseNow()
	browser := connectBrowser(t, wsURL(ts), token, "wing-input")
	defer browser.CloseNow()

	sess := startSession(t, browser, wingConn, "claude", "wing-input")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Browser sends pty.input
	inputData := base64.StdEncoding.EncodeToString([]byte("hello world"))
	wsjson.Write(ctx, browser, ws.PTYInput{Type: ws.TypePTYInput, SessionID: sess, Data: inputData})

	// Wing reads pty.input
	var wingInput ws.PTYInput
	if err := wsjson.Read(ctx, wingConn, &wingInput); err != nil {
		t.Fatalf("wing read pty.input: %v", err)
	}
	decoded, _ := base64.StdEncoding.DecodeString(wingInput.Data)
	if string(decoded) != "hello world" {
		t.Errorf("input = %q, want %q", decoded, "hello world")
	}

	// Browser sends pty.resize
	wsjson.Write(ctx, browser, ws.PTYResize{Type: ws.TypePTYResize, SessionID: sess, Cols: 120, Rows: 40})

	var wingResize ws.PTYResize
	if err := wsjson.Read(ctx, wingConn, &wingResize); err != nil {
		t.Fatalf("wing read pty.resize: %v", err)
	}
	if wingResize.Cols != 120 || wingResize.Rows != 40 {
		t.Errorf("resize = %dx%d, want 120x40", wingResize.Cols, wingResize.Rows)
	}
}

func TestPTYRoutingReattach(t *testing.T) {
	_, ts, store := testRelayAndWS(t)
	token, _ := createTestUser(t, store, "reattach")

	wingConn := connectWing(t, wsURL(ts), token, "wing-reattach", []string{"claude"})
	defer wingConn.CloseNow()
	browser1 := connectBrowser(t, wsURL(ts), token, "wing-reattach")
	defer browser1.CloseNow()

	sess := startSession(t, browser1, wingConn, "claude", "wing-reattach")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Send pty.started
	wsjson.Write(ctx, wingConn, ws.PTYStarted{Type: ws.TypePTYStarted, SessionID: sess, Agent: "claude"})
	var started ws.PTYStarted
	wsjson.Read(ctx, browser1, &started)

	// Browser 1 disconnects
	browser1.Close(websocket.StatusNormalClosure, "navigating away")

	// Browser 2 connects and reattaches
	browser2 := connectBrowser(t, wsURL(ts), token, "wing-reattach")
	defer browser2.CloseNow()

	attach := ws.PTYAttach{
		Type:      ws.TypePTYAttach,
		SessionID: sess,
		WingID:    "wing-reattach",
	}
	if err := wsjson.Write(ctx, browser2, attach); err != nil {
		t.Fatalf("write pty.attach: %v", err)
	}

	// Wing receives pty.attach
	var wingAttach ws.PTYAttach
	if err := wsjson.Read(ctx, wingConn, &wingAttach); err != nil {
		t.Fatalf("wing read pty.attach: %v", err)
	}
	if wingAttach.SessionID != sess {
		t.Errorf("attach session = %s, want %s", wingAttach.SessionID, sess)
	}

	// Wing sends output — browser 2 should get it
	outData := base64.StdEncoding.EncodeToString([]byte("reattached-output"))
	wsjson.Write(ctx, wingConn, ws.PTYOutput{Type: ws.TypePTYOutput, SessionID: sess, Data: outData})

	var b2out ws.PTYOutput
	if err := wsjson.Read(ctx, browser2, &b2out); err != nil {
		t.Fatalf("browser2 read: %v", err)
	}
	decoded, _ := base64.StdEncoding.DecodeString(b2out.Data)
	if string(decoded) != "reattached-output" {
		t.Errorf("browser2 got %q, want %q", decoded, "reattached-output")
	}
}

func TestPTYRoutingKill(t *testing.T) {
	_, ts, store := testRelayAndWS(t)
	token, _ := createTestUser(t, store, "kill")

	wingConn := connectWing(t, wsURL(ts), token, "wing-kill", []string{"claude"})
	defer wingConn.CloseNow()
	browser := connectBrowser(t, wsURL(ts), token, "wing-kill")
	defer browser.CloseNow()

	sess := startSession(t, browser, wingConn, "claude", "wing-kill")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Browser sends pty.kill
	kill := ws.PTYKill{Type: ws.TypePTYKill, SessionID: sess}
	if err := wsjson.Write(ctx, browser, kill); err != nil {
		t.Fatalf("write pty.kill: %v", err)
	}

	// Wing receives pty.kill
	var wingKill ws.PTYKill
	if err := wsjson.Read(ctx, wingConn, &wingKill); err != nil {
		t.Fatalf("wing read pty.kill: %v", err)
	}
	if wingKill.SessionID != sess {
		t.Errorf("kill session = %s, want %s", wingKill.SessionID, sess)
	}
}

func TestPTYRoutingUserInjection(t *testing.T) {
	_, ts, store := testRelayAndWS(t)
	token, _ := createTestUser(t, store, "inject")

	wingConn := connectWing(t, wsURL(ts), token, "wing-inject", []string{"claude"})
	defer wingConn.CloseNow()
	browser := connectBrowser(t, wsURL(ts), token, "wing-inject")
	defer browser.CloseNow()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Send pty.start
	start := ws.PTYStart{
		Type:   ws.TypePTYStart,
		Agent:  "claude",
		WingID: "wing-inject",
		Cols:   80,
		Rows:   24,
	}
	wsjson.Write(ctx, browser, start)

	// Wing reads pty.start — should have relay-injected user info
	var wingStart ws.PTYStart
	wsjson.Read(ctx, wingConn, &wingStart)

	if wingStart.UserID != "user-inject" {
		t.Errorf("user_id = %q, want %q", wingStart.UserID, "user-inject")
	}
	// DevMode with personal wing → owner role
	if wingStart.OrgRole != "owner" {
		t.Errorf("org_role = %q, want %q", wingStart.OrgRole, "owner")
	}
}

func TestPTYRoutingMigrateSignaling(t *testing.T) {
	_, ts, store := testRelayAndWS(t)
	token, _ := createTestUser(t, store, "p2p-signal")

	wingConn := connectWing(t, wsURL(ts), token, "wing-p2p", []string{"claude"})
	defer wingConn.CloseNow()
	browser := connectBrowser(t, wsURL(ts), token, "wing-p2p")
	defer browser.CloseNow()

	sess := startSession(t, browser, wingConn, "claude", "wing-p2p")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Browser sends pty.migrate
	migrate := ws.PTYMigrate{Type: ws.TypePTYMigrate, SessionID: sess, AuthToken: "test-token"}
	wsjson.Write(ctx, browser, migrate)

	// Wing receives pty.migrate
	var wingMigrate ws.PTYMigrate
	if err := wsjson.Read(ctx, wingConn, &wingMigrate); err != nil {
		t.Fatalf("wing read pty.migrate: %v", err)
	}
	if wingMigrate.SessionID != sess {
		t.Errorf("migrate session = %s, want %s", wingMigrate.SessionID, sess)
	}
	if wingMigrate.AuthToken != "test-token" {
		t.Errorf("auth_token = %q, want %q", wingMigrate.AuthToken, "test-token")
	}

	// Wing sends pty.migrated
	wsjson.Write(ctx, wingConn, ws.PTYMigrated{Type: ws.TypePTYMigrated, SessionID: sess})

	// Browser receives pty.migrated
	var browserMigrated ws.PTYMigrated
	if err := wsjson.Read(ctx, browser, &browserMigrated); err != nil {
		t.Fatalf("browser read pty.migrated: %v", err)
	}
	if browserMigrated.SessionID != sess {
		t.Errorf("migrated session = %s, want %s", browserMigrated.SessionID, sess)
	}

	// Wing sends pty.fallback
	wsjson.Write(ctx, wingConn, ws.PTYFallback{Type: ws.TypePTYFallback, SessionID: sess})

	// Browser receives pty.fallback
	var browserFallback ws.PTYFallback
	if err := wsjson.Read(ctx, browser, &browserFallback); err != nil {
		t.Fatalf("browser read pty.fallback: %v", err)
	}
	if browserFallback.SessionID != sess {
		t.Errorf("fallback session = %s, want %s", browserFallback.SessionID, sess)
	}
}

func TestPTYRoutingPreviewAndBrowserOpen(t *testing.T) {
	_, ts, store := testRelayAndWS(t)
	token, _ := createTestUser(t, store, "preview")

	wingConn := connectWing(t, wsURL(ts), token, "wing-preview", []string{"claude"})
	defer wingConn.CloseNow()
	browser := connectBrowser(t, wsURL(ts), token, "wing-preview")
	defer browser.CloseNow()

	sess := startSession(t, browser, wingConn, "claude", "wing-preview")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Wing sends pty.preview
	wsjson.Write(ctx, wingConn, ws.PTYPreview{Type: ws.TypePTYPreview, SessionID: sess, Data: "dGVzdA=="})

	var preview ws.PTYPreview
	if err := wsjson.Read(ctx, browser, &preview); err != nil {
		t.Fatalf("browser read pty.preview: %v", err)
	}
	if preview.SessionID != sess {
		t.Errorf("preview session = %s, want %s", preview.SessionID, sess)
	}

	// Wing sends pty.browser_open
	openMsg, _ := json.Marshal(ws.PTYBrowserOpen{Type: ws.TypePTYBrowserOpen, SessionID: sess, URL: "https://example.com"})
	wingConn.Write(ctx, websocket.MessageText, openMsg)

	var browserOpen ws.PTYBrowserOpen
	if err := wsjson.Read(ctx, browser, &browserOpen); err != nil {
		t.Fatalf("browser read pty.browser_open: %v", err)
	}
	if browserOpen.URL != "https://example.com" {
		t.Errorf("url = %q, want %q", browserOpen.URL, "https://example.com")
	}
}
