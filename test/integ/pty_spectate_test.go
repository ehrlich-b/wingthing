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

// spectateSession sends pty.attach with spectate=true and reads the forwarded attach on wing.
func spectateSession(t *testing.T, browserConn, wingConn *websocket.Conn, sessionID, wingID string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	attach := ws.PTYAttach{
		Type:      ws.TypePTYAttach,
		SessionID: sessionID,
		WingID:    wingID,
		Spectate:  true,
	}
	if err := wsjson.Write(ctx, browserConn, attach); err != nil {
		t.Fatalf("write spectate attach: %v", err)
	}

	var wingAttach ws.PTYAttach
	if err := wsjson.Read(ctx, wingConn, &wingAttach); err != nil {
		t.Fatalf("wing read spectate attach: %v", err)
	}
	if wingAttach.ViewerID == "" {
		t.Fatal("expected relay to assign viewer_id")
	}
	if !wingAttach.Spectate {
		t.Fatal("expected spectate=true on forwarded attach")
	}
	return wingAttach.ViewerID
}

func TestSpectateOutputRouting(t *testing.T) {
	_, ts, store := testRelayAndWS(t)
	token, _ := createTestUser(t, store, "spectate-out")

	wingConn := connectWing(t, wsURL(ts), token, "wing-spec-out", []string{"claude"})
	defer wingConn.CloseNow()

	owner := connectBrowser(t, wsURL(ts), token, "wing-spec-out")
	defer owner.CloseNow()

	sess := startSession(t, owner, wingConn, "claude", "wing-spec-out")

	spectator := connectBrowser(t, wsURL(ts), token, "wing-spec-out")
	defer spectator.CloseNow()

	viewerID := spectateSession(t, spectator, wingConn, sess, "wing-spec-out")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Wing sends controller output (no viewer_id) — owner gets it
	ownerData := base64.StdEncoding.EncodeToString([]byte("owner-output"))
	wsjson.Write(ctx, wingConn, ws.PTYOutput{Type: ws.TypePTYOutput, SessionID: sess, Data: ownerData})

	var ownerOut ws.PTYOutput
	if err := wsjson.Read(ctx, owner, &ownerOut); err != nil {
		t.Fatalf("owner read: %v", err)
	}
	decoded, _ := base64.StdEncoding.DecodeString(ownerOut.Data)
	if string(decoded) != "owner-output" {
		t.Errorf("owner got %q, want %q", decoded, "owner-output")
	}

	// Wing sends spectator output (with viewer_id) — spectator gets it
	specData := base64.StdEncoding.EncodeToString([]byte("spectator-output"))
	wsjson.Write(ctx, wingConn, ws.PTYOutput{Type: ws.TypePTYOutput, SessionID: sess, Data: specData, ViewerID: viewerID})

	var specOut ws.PTYOutput
	if err := wsjson.Read(ctx, spectator, &specOut); err != nil {
		t.Fatalf("spectator read: %v", err)
	}
	decoded, _ = base64.StdEncoding.DecodeString(specOut.Data)
	if string(decoded) != "spectator-output" {
		t.Errorf("spectator got %q, want %q", decoded, "spectator-output")
	}
}

func TestSpectateInputBlocked(t *testing.T) {
	_, ts, store := testRelayAndWS(t)
	token, _ := createTestUser(t, store, "spectate-input")

	wingConn := connectWing(t, wsURL(ts), token, "wing-spec-inp", []string{"claude"})
	defer wingConn.CloseNow()

	owner := connectBrowser(t, wsURL(ts), token, "wing-spec-inp")
	defer owner.CloseNow()

	sess := startSession(t, owner, wingConn, "claude", "wing-spec-inp")

	spectator := connectBrowser(t, wsURL(ts), token, "wing-spec-inp")
	defer spectator.CloseNow()

	_ = spectateSession(t, spectator, wingConn, sess, "wing-spec-inp")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Spectator sends pty.input — wing should NOT receive it
	inputData := base64.StdEncoding.EncodeToString([]byte("hacker-input"))
	wsjson.Write(ctx, spectator, ws.PTYInput{Type: ws.TypePTYInput, SessionID: sess, Data: inputData})

	// Owner sends pty.input — wing SHOULD receive it
	ownerInput := base64.StdEncoding.EncodeToString([]byte("legit-input"))
	wsjson.Write(ctx, owner, ws.PTYInput{Type: ws.TypePTYInput, SessionID: sess, Data: ownerInput})

	// Wing reads — should get owner's input, not spectator's
	var wingInput ws.PTYInput
	if err := wsjson.Read(ctx, wingConn, &wingInput); err != nil {
		t.Fatalf("wing read: %v", err)
	}
	decoded, _ := base64.StdEncoding.DecodeString(wingInput.Data)
	if string(decoded) != "legit-input" {
		t.Errorf("wing got %q, want %q", decoded, "legit-input")
	}

	// Spectator sends pty.resize — wing should NOT receive it
	wsjson.Write(ctx, spectator, ws.PTYResize{Type: ws.TypePTYResize, SessionID: sess, Cols: 999, Rows: 999})

	// Short timeout read — wing should get nothing
	shortCtx, shortCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer shortCancel()
	var ghost json.RawMessage
	err := wsjson.Read(shortCtx, wingConn, &ghost)
	if err == nil {
		t.Errorf("expected no message from spectator resize, but got: %s", ghost)
	}
}

func TestSpectateDoesNotDisruptController(t *testing.T) {
	_, ts, store := testRelayAndWS(t)
	token, _ := createTestUser(t, store, "spectate-nodisrupt")

	wingConn := connectWing(t, wsURL(ts), token, "wing-spec-nd", []string{"claude"})
	defer wingConn.CloseNow()

	owner := connectBrowser(t, wsURL(ts), token, "wing-spec-nd")
	defer owner.CloseNow()

	sess := startSession(t, owner, wingConn, "claude", "wing-spec-nd")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Verify controller output works before spectator
	preData := base64.StdEncoding.EncodeToString([]byte("pre-spectator"))
	wsjson.Write(ctx, wingConn, ws.PTYOutput{Type: ws.TypePTYOutput, SessionID: sess, Data: preData})
	var preOut ws.PTYOutput
	if err := wsjson.Read(ctx, owner, &preOut); err != nil {
		t.Fatalf("owner pre-spectator read: %v", err)
	}

	// Spectator joins
	spectator := connectBrowser(t, wsURL(ts), token, "wing-spec-nd")
	defer spectator.CloseNow()
	_ = spectateSession(t, spectator, wingConn, sess, "wing-spec-nd")

	// Controller output still works after spectator joins
	postData := base64.StdEncoding.EncodeToString([]byte("post-spectator"))
	wsjson.Write(ctx, wingConn, ws.PTYOutput{Type: ws.TypePTYOutput, SessionID: sess, Data: postData})
	var postOut ws.PTYOutput
	if err := wsjson.Read(ctx, owner, &postOut); err != nil {
		t.Fatalf("owner post-spectator read: %v", err)
	}
	decoded, _ := base64.StdEncoding.DecodeString(postOut.Data)
	if string(decoded) != "post-spectator" {
		t.Errorf("owner got %q, want %q", decoded, "post-spectator")
	}

	// Spectator disconnects
	spectator.Close(websocket.StatusNormalClosure, "leaving")
	time.Sleep(100 * time.Millisecond)

	// Controller output still works after spectator leaves
	afterData := base64.StdEncoding.EncodeToString([]byte("after-spectator"))
	wsjson.Write(ctx, wingConn, ws.PTYOutput{Type: ws.TypePTYOutput, SessionID: sess, Data: afterData})
	var afterOut ws.PTYOutput
	if err := wsjson.Read(ctx, owner, &afterOut); err != nil {
		t.Fatalf("owner after-spectator read: %v", err)
	}
	decoded, _ = base64.StdEncoding.DecodeString(afterOut.Data)
	if string(decoded) != "after-spectator" {
		t.Errorf("owner got %q, want %q", decoded, "after-spectator")
	}
}

func TestSpectateRelayInjection(t *testing.T) {
	_, ts, store := testRelayAndWS(t)
	token, _ := createTestUser(t, store, "spectate-inject")

	wingConn := connectWing(t, wsURL(ts), token, "wing-spec-inj", []string{"claude"})
	defer wingConn.CloseNow()

	owner := connectBrowser(t, wsURL(ts), token, "wing-spec-inj")
	defer owner.CloseNow()

	sess := startSession(t, owner, wingConn, "claude", "wing-spec-inj")

	spectator := connectBrowser(t, wsURL(ts), token, "wing-spec-inj")
	defer spectator.CloseNow()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Send spectate attach
	attach := ws.PTYAttach{
		Type:      ws.TypePTYAttach,
		SessionID: sess,
		WingID:    "wing-spec-inj",
		Spectate:  true,
	}
	wsjson.Write(ctx, spectator, attach)

	// Wing reads forwarded attach
	var wingAttach ws.PTYAttach
	wsjson.Read(ctx, wingConn, &wingAttach)

	if wingAttach.ViewerID == "" {
		t.Error("expected relay to assign viewer_id")
	}
	if wingAttach.UserID == "" {
		t.Error("expected relay to inject user_id")
	}
	if !wingAttach.Spectate {
		t.Error("expected spectate=true on forwarded attach")
	}
}

func TestSpectateMultipleViewers(t *testing.T) {
	_, ts, store := testRelayAndWS(t)
	token, _ := createTestUser(t, store, "spectate-multi")

	wingConn := connectWing(t, wsURL(ts), token, "wing-spec-multi", []string{"claude"})
	defer wingConn.CloseNow()

	owner := connectBrowser(t, wsURL(ts), token, "wing-spec-multi")
	defer owner.CloseNow()

	sess := startSession(t, owner, wingConn, "claude", "wing-spec-multi")

	spec1 := connectBrowser(t, wsURL(ts), token, "wing-spec-multi")
	defer spec1.CloseNow()
	viewer1 := spectateSession(t, spec1, wingConn, sess, "wing-spec-multi")

	spec2 := connectBrowser(t, wsURL(ts), token, "wing-spec-multi")
	defer spec2.CloseNow()
	viewer2 := spectateSession(t, spec2, wingConn, sess, "wing-spec-multi")

	if viewer1 == viewer2 {
		t.Fatal("viewer IDs should be unique")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Output for viewer1 goes to spec1 only
	v1Data := base64.StdEncoding.EncodeToString([]byte("for-viewer1"))
	wsjson.Write(ctx, wingConn, ws.PTYOutput{Type: ws.TypePTYOutput, SessionID: sess, Data: v1Data, ViewerID: viewer1})

	var s1out ws.PTYOutput
	if err := wsjson.Read(ctx, spec1, &s1out); err != nil {
		t.Fatalf("spec1 read: %v", err)
	}
	decoded, _ := base64.StdEncoding.DecodeString(s1out.Data)
	if string(decoded) != "for-viewer1" {
		t.Errorf("spec1 got %q, want %q", decoded, "for-viewer1")
	}

	// Output for viewer2 goes to spec2 only
	v2Data := base64.StdEncoding.EncodeToString([]byte("for-viewer2"))
	wsjson.Write(ctx, wingConn, ws.PTYOutput{Type: ws.TypePTYOutput, SessionID: sess, Data: v2Data, ViewerID: viewer2})

	var s2out ws.PTYOutput
	if err := wsjson.Read(ctx, spec2, &s2out); err != nil {
		t.Fatalf("spec2 read: %v", err)
	}
	decoded, _ = base64.StdEncoding.DecodeString(s2out.Data)
	if string(decoded) != "for-viewer2" {
		t.Errorf("spec2 got %q, want %q", decoded, "for-viewer2")
	}
}

func TestSpectateDetach(t *testing.T) {
	_, ts, store := testRelayAndWS(t)
	token, _ := createTestUser(t, store, "spectate-detach")

	wingConn := connectWing(t, wsURL(ts), token, "wing-spec-det", []string{"claude"})
	defer wingConn.CloseNow()

	owner := connectBrowser(t, wsURL(ts), token, "wing-spec-det")
	defer owner.CloseNow()

	sess := startSession(t, owner, wingConn, "claude", "wing-spec-det")

	spectator := connectBrowser(t, wsURL(ts), token, "wing-spec-det")
	defer spectator.CloseNow()
	viewerID := spectateSession(t, spectator, wingConn, sess, "wing-spec-det")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Spectator detaches
	wsjson.Write(ctx, spectator, ws.PTYDetach{Type: ws.TypePTYDetach, SessionID: sess})
	time.Sleep(100 * time.Millisecond)

	// Output for this viewer should be dropped
	specData := base64.StdEncoding.EncodeToString([]byte("after-detach"))
	wsjson.Write(ctx, wingConn, ws.PTYOutput{Type: ws.TypePTYOutput, SessionID: sess, Data: specData, ViewerID: viewerID})

	shortCtx, shortCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer shortCancel()
	var ghost ws.PTYOutput
	err := wsjson.Read(shortCtx, spectator, &ghost)
	if err == nil {
		t.Error("expected no message after spectator detach")
	}

	// Owner still gets output
	ownerData := base64.StdEncoding.EncodeToString([]byte("owner-still-ok"))
	wsjson.Write(ctx, wingConn, ws.PTYOutput{Type: ws.TypePTYOutput, SessionID: sess, Data: ownerData})

	var ownerOut ws.PTYOutput
	if err := wsjson.Read(ctx, owner, &ownerOut); err != nil {
		t.Fatalf("owner read after spectator detach: %v", err)
	}
}

func TestSpectateExitNotification(t *testing.T) {
	_, ts, store := testRelayAndWS(t)
	token, _ := createTestUser(t, store, "spectate-exit")

	wingConn := connectWing(t, wsURL(ts), token, "wing-spec-exit", []string{"claude"})
	defer wingConn.CloseNow()

	owner := connectBrowser(t, wsURL(ts), token, "wing-spec-exit")
	defer owner.CloseNow()

	sess := startSession(t, owner, wingConn, "claude", "wing-spec-exit")

	spectator := connectBrowser(t, wsURL(ts), token, "wing-spec-exit")
	defer spectator.CloseNow()
	viewerID := spectateSession(t, spectator, wingConn, sess, "wing-spec-exit")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Wing sends spectator exit (with viewer_id)
	wsjson.Write(ctx, wingConn, ws.PTYExited{Type: ws.TypePTYExited, SessionID: sess, ExitCode: 0, ViewerID: viewerID})

	var specExit ws.PTYExited
	if err := wsjson.Read(ctx, spectator, &specExit); err != nil {
		t.Fatalf("spectator read exit: %v", err)
	}
	if specExit.ExitCode != 0 {
		t.Errorf("spectator exit code = %d, want 0", specExit.ExitCode)
	}

	// Wing sends controller exit (no viewer_id) — owner gets it
	wsjson.Write(ctx, wingConn, ws.PTYExited{Type: ws.TypePTYExited, SessionID: sess, ExitCode: 1})

	var ownerExit ws.PTYExited
	if err := wsjson.Read(ctx, owner, &ownerExit); err != nil {
		t.Fatalf("owner read exit: %v", err)
	}
	if ownerExit.ExitCode != 1 {
		t.Errorf("owner exit code = %d, want 1", ownerExit.ExitCode)
	}
}

func TestSpectateWingOffline(t *testing.T) {
	_, ts, store := testRelayAndWS(t)
	token, _ := createTestUser(t, store, "spectate-offline")

	wingConn := connectWing(t, wsURL(ts), token, "wing-spec-off", []string{"claude"})

	owner := connectBrowser(t, wsURL(ts), token, "wing-spec-off")
	defer owner.CloseNow()

	sess := startSession(t, owner, wingConn, "claude", "wing-spec-off")

	// Send pty.started so session is fully active
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsjson.Write(ctx, wingConn, ws.PTYStarted{Type: ws.TypePTYStarted, SessionID: sess, Agent: "claude"})
	var started ws.PTYStarted
	wsjson.Read(ctx, owner, &started)

	spectator := connectBrowser(t, wsURL(ts), token, "wing-spec-off")
	defer spectator.CloseNow()
	_ = spectateSession(t, spectator, wingConn, sess, "wing-spec-off")

	// Wing disconnects
	wingConn.Close(websocket.StatusNormalClosure, "going offline")

	// Both owner and spectator should get wing.offline
	var ownerMsg ws.Envelope
	if err := wsjson.Read(ctx, owner, &ownerMsg); err != nil {
		t.Fatalf("owner read offline: %v", err)
	}
	if ownerMsg.Type != ws.TypeWingOffline {
		t.Errorf("owner got %s, want wing.offline", ownerMsg.Type)
	}

	var specMsg ws.Envelope
	if err := wsjson.Read(ctx, spectator, &specMsg); err != nil {
		t.Fatalf("spectator read offline: %v", err)
	}
	if specMsg.Type != ws.TypeWingOffline {
		t.Errorf("spectator got %s, want wing.offline", specMsg.Type)
	}
}

func TestSpectateAccessControl(t *testing.T) {
	_, ts, store := testRelayAndWS(t)
	ownerToken, _ := createTestUser(t, store, "spec-owner")
	outsiderToken, _ := createTestUser(t, store, "spec-outsider")

	wingConn := connectWing(t, wsURL(ts), ownerToken, "wing-spec-acl", []string{"claude"})
	defer wingConn.CloseNow()

	owner := connectBrowser(t, wsURL(ts), ownerToken, "wing-spec-acl")
	defer owner.CloseNow()

	sess := startSession(t, owner, wingConn, "claude", "wing-spec-acl")

	// Outsider tries to spectate — should fail (canAccessWing)
	outsider := connectBrowser(t, wsURL(ts), outsiderToken, "wing-spec-acl")
	defer outsider.CloseNow()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	attach := ws.PTYAttach{
		Type:      ws.TypePTYAttach,
		SessionID: sess,
		WingID:    "wing-spec-acl",
		Spectate:  true,
	}
	wsjson.Write(ctx, outsider, attach)

	var errMsg ws.ErrorMsg
	if err := wsjson.Read(ctx, outsider, &errMsg); err != nil {
		t.Fatalf("outsider read: %v", err)
	}
	if errMsg.Type != ws.TypeError {
		t.Errorf("expected error, got %s", errMsg.Type)
	}
}

func TestSpectateClearBrowser(t *testing.T) {
	_, ts, store := testRelayAndWS(t)
	token, _ := createTestUser(t, store, "spectate-clear")

	wingConn := connectWing(t, wsURL(ts), token, "wing-spec-clr", []string{"claude"})
	defer wingConn.CloseNow()

	owner := connectBrowser(t, wsURL(ts), token, "wing-spec-clr")
	defer owner.CloseNow()

	sess := startSession(t, owner, wingConn, "claude", "wing-spec-clr")

	spectator := connectBrowser(t, wsURL(ts), token, "wing-spec-clr")
	viewerID := spectateSession(t, spectator, wingConn, sess, "wing-spec-clr")

	// Spectator WebSocket closes
	spectator.Close(websocket.StatusNormalClosure, "closing")
	time.Sleep(200 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Output to that viewer_id should be silently dropped (no crash)
	specData := base64.StdEncoding.EncodeToString([]byte("to-closed-viewer"))
	wsjson.Write(ctx, wingConn, ws.PTYOutput{Type: ws.TypePTYOutput, SessionID: sess, Data: specData, ViewerID: viewerID})

	// Owner still works
	ownerData := base64.StdEncoding.EncodeToString([]byte("owner-ok"))
	wsjson.Write(ctx, wingConn, ws.PTYOutput{Type: ws.TypePTYOutput, SessionID: sess, Data: ownerData})

	var ownerOut ws.PTYOutput
	if err := wsjson.Read(ctx, owner, &ownerOut); err != nil {
		t.Fatalf("owner read after spectator close: %v", err)
	}
	decoded, _ := base64.StdEncoding.DecodeString(ownerOut.Data)
	if string(decoded) != "owner-ok" {
		t.Errorf("owner got %q, want %q", decoded, "owner-ok")
	}
}

func TestSpectateKillBlocked(t *testing.T) {
	_, ts, store := testRelayAndWS(t)
	token, _ := createTestUser(t, store, "spectate-kill")

	wingConn := connectWing(t, wsURL(ts), token, "wing-spec-kill", []string{"claude"})
	defer wingConn.CloseNow()

	owner := connectBrowser(t, wsURL(ts), token, "wing-spec-kill")
	defer owner.CloseNow()

	sess := startSession(t, owner, wingConn, "claude", "wing-spec-kill")

	spectator := connectBrowser(t, wsURL(ts), token, "wing-spec-kill")
	defer spectator.CloseNow()
	_ = spectateSession(t, spectator, wingConn, sess, "wing-spec-kill")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Spectator sends pty.kill — should be silently dropped
	wsjson.Write(ctx, spectator, ws.PTYKill{Type: ws.TypePTYKill, SessionID: sess})

	// Owner sends normal input to verify wing is still alive
	ownerInput := base64.StdEncoding.EncodeToString([]byte("still-alive"))
	wsjson.Write(ctx, owner, ws.PTYInput{Type: ws.TypePTYInput, SessionID: sess, Data: ownerInput})

	var wingInput ws.PTYInput
	if err := wsjson.Read(ctx, wingConn, &wingInput); err != nil {
		t.Fatalf("wing read: %v", err)
	}
	decoded, _ := base64.StdEncoding.DecodeString(wingInput.Data)
	if string(decoded) != "still-alive" {
		t.Errorf("wing got %q, want %q", decoded, "still-alive")
	}
}

func TestSpectateStartedRouting(t *testing.T) {
	_, ts, store := testRelayAndWS(t)
	token, _ := createTestUser(t, store, "spectate-started")

	wingConn := connectWing(t, wsURL(ts), token, "wing-spec-str", []string{"claude"})
	defer wingConn.CloseNow()

	owner := connectBrowser(t, wsURL(ts), token, "wing-spec-str")
	defer owner.CloseNow()

	sess := startSession(t, owner, wingConn, "claude", "wing-spec-str")

	spectator := connectBrowser(t, wsURL(ts), token, "wing-spec-str")
	defer spectator.CloseNow()
	viewerID := spectateSession(t, spectator, wingConn, sess, "wing-spec-str")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Wing sends pty.started with viewer_id — spectator gets it, owner does NOT
	wsjson.Write(ctx, wingConn, ws.PTYStarted{Type: ws.TypePTYStarted, SessionID: sess, Agent: "claude", PublicKey: "fakepub", ViewerID: viewerID})

	var specStarted ws.PTYStarted
	if err := wsjson.Read(ctx, spectator, &specStarted); err != nil {
		t.Fatalf("spectator read started: %v", err)
	}
	if specStarted.PublicKey != "fakepub" {
		t.Errorf("spectator got public_key=%q, want %q", specStarted.PublicKey, "fakepub")
	}

	// Owner should NOT get this message
	shortCtx, shortCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer shortCancel()
	var ghost json.RawMessage
	if err := wsjson.Read(shortCtx, owner, &ghost); err == nil {
		t.Errorf("owner should not receive spectator's pty.started, got: %s", ghost)
	}
}

func TestSpectateControllerExitFansOut(t *testing.T) {
	_, ts, store := testRelayAndWS(t)
	token, _ := createTestUser(t, store, "spectate-fanout")

	wingConn := connectWing(t, wsURL(ts), token, "wing-spec-fan", []string{"claude"})
	defer wingConn.CloseNow()

	owner := connectBrowser(t, wsURL(ts), token, "wing-spec-fan")
	defer owner.CloseNow()

	sess := startSession(t, owner, wingConn, "claude", "wing-spec-fan")

	spectator := connectBrowser(t, wsURL(ts), token, "wing-spec-fan")
	defer spectator.CloseNow()
	_ = spectateSession(t, spectator, wingConn, sess, "wing-spec-fan")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Controller exit (no viewer_id) — both owner AND spectator should get it
	wsjson.Write(ctx, wingConn, ws.PTYExited{Type: ws.TypePTYExited, SessionID: sess, ExitCode: 42})

	var ownerExit ws.PTYExited
	if err := wsjson.Read(ctx, owner, &ownerExit); err != nil {
		t.Fatalf("owner read exit: %v", err)
	}
	if ownerExit.ExitCode != 42 {
		t.Errorf("owner exit code = %d, want 42", ownerExit.ExitCode)
	}

	var specExit ws.PTYExited
	if err := wsjson.Read(ctx, spectator, &specExit); err != nil {
		t.Fatalf("spectator read exit: %v", err)
	}
	if specExit.ExitCode != 42 {
		t.Errorf("spectator exit code = %d, want 42", specExit.ExitCode)
	}
}

func TestSpectatePreviewRouting(t *testing.T) {
	_, ts, store := testRelayAndWS(t)
	token, _ := createTestUser(t, store, "spectate-preview")

	wingConn := connectWing(t, wsURL(ts), token, "wing-spec-prv", []string{"claude"})
	defer wingConn.CloseNow()

	owner := connectBrowser(t, wsURL(ts), token, "wing-spec-prv")
	defer owner.CloseNow()

	sess := startSession(t, owner, wingConn, "claude", "wing-spec-prv")

	spectator := connectBrowser(t, wsURL(ts), token, "wing-spec-prv")
	defer spectator.CloseNow()
	viewerID := spectateSession(t, spectator, wingConn, sess, "wing-spec-prv")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Wing sends pty.preview with viewer_id — spectator gets it
	previewData := base64.StdEncoding.EncodeToString([]byte("preview-content"))
	wsjson.Write(ctx, wingConn, ws.PTYPreview{Type: ws.TypePTYPreview, SessionID: sess, Data: previewData, ViewerID: viewerID})

	var specPreview ws.PTYPreview
	if err := wsjson.Read(ctx, spectator, &specPreview); err != nil {
		t.Fatalf("spectator read preview: %v", err)
	}
	decoded, _ := base64.StdEncoding.DecodeString(specPreview.Data)
	if string(decoded) != "preview-content" {
		t.Errorf("spectator got %q, want %q", decoded, "preview-content")
	}

	// Owner should NOT get it
	shortCtx, shortCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer shortCancel()
	var ghost json.RawMessage
	if err := wsjson.Read(shortCtx, owner, &ghost); err == nil {
		t.Errorf("owner should not receive spectator's preview, got: %s", ghost)
	}
}

func TestSpectateNonexistentRoute(t *testing.T) {
	_, ts, store := testRelayAndWS(t)
	token, _ := createTestUser(t, store, "spectate-noroute")

	wingConn := connectWing(t, wsURL(ts), token, "wing-spec-nr", []string{"claude"})
	defer wingConn.CloseNow()

	browser := connectBrowser(t, wsURL(ts), token, "wing-spec-nr")
	defer browser.CloseNow()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Spectate a session that doesn't exist — attach still forwards to wing,
	// AddViewer silently no-ops (route is nil). No crash.
	attach := ws.PTYAttach{
		Type:      ws.TypePTYAttach,
		SessionID: "nonexistent-session",
		WingID:    "wing-spec-nr",
		Spectate:  true,
	}
	wsjson.Write(ctx, browser, attach)

	// Wing receives the forwarded attach (relay forwards regardless)
	var wingAttach ws.PTYAttach
	if err := wsjson.Read(ctx, wingConn, &wingAttach); err != nil {
		t.Fatalf("wing read: %v", err)
	}
	if !wingAttach.Spectate {
		t.Error("expected spectate=true on forwarded attach")
	}
	if wingAttach.ViewerID == "" {
		t.Error("expected relay-assigned viewer_id")
	}
}
