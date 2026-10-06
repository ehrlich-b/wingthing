//go:build integration

package wing

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/ws"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The browser bridge, encryption and egg RPC are real. Only the network relay
// and browser DOM are replaced by in-process typed message queues.
func TestPreviewBrowserLeaseOnRealEgg(t *testing.T) {
	binary := os.Getenv("WT_TEST_PREVIEW_BINARY")
	if binary == "" {
		t.Skip("run make test-preview-input")
	}
	oldChannel := config.ReleaseChannel
	config.ReleaseChannel = "preview"
	defer func() { config.ReleaseChannel = oldChannel }()
	root, err := os.MkdirTemp("/tmp", "wt-browser-real-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	home, work, stateDir := filepath.Join(root, "home"), filepath.Join(root, "work"), filepath.Join(root, "state")
	for _, dir := range []string{home, work} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join(work, "fixture.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'ready\\n'\nwhile IFS= read -r line; do printf 'result:%s\\n' \"$line\"; done\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "terminal", "--cwd", work, "--json", "--", "/bin/sh", script)
	cmd.Env = append(os.Environ(), "HOME="+home, "WINGTHING_DIR="+stateDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("start: %v %s", err, output)
	}
	var started struct {
		Session string `json:"session"`
	}
	if err := json.Unmarshal(output, &started); err != nil || started.Session == "" {
		t.Fatalf("start: %s %v", output, err)
	}
	eggDir := filepath.Join(stateDir, "eggs", started.Session)
	dial := func() *egg.Client {
		t.Helper()
		client, err := egg.Dial(filepath.Join(eggDir, "egg.sock"), filepath.Join(eggDir, "egg.token"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client.Close() })
		return client
	}
	client, bridgeClient := dial(), dial()
	stopped := false
	defer func() {
		if !stopped {
			stopCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			if err := client.Kill(stopCtx, started.Session); err != nil {
				t.Errorf("cleanup: %v", err)
			}
		}
	}()
	// Keys exist only in this fixture's disposable state; no real trust or
	// credential is read, installed, changed or connected.
	if _, err := auth.EnsureKeyPair(stateDir); err != nil {
		t.Fatal(err)
	}
	wingKey, err := auth.LoadPrivateKey(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	firstKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	secondKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub := func(key *ecdh.PrivateKey) string { return base64.StdEncoding.EncodeToString(key.PublicKey().Bytes()) }
	input := make(chan []byte, 32)
	messages := make(chan []byte, 256)
	done := make(chan struct{})
	write := func(value any) error {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		select {
		case messages <- data:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	go func() {
		defer close(done)
		handleReclaimedPTY(ctx, &config.Config{Dir: stateDir}, bridgeClient, started.Session, eggDir, write, input, &config.WingConfig{Spectate: true}, nil, auth.NewAuthCache(), auth.PasskeyPolicy{}, 0, nil)
	}()
	send := func(value any) {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		select {
		case input <- data:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	var backlog [][]byte
	await := func(kind, controllerID, viewerID string) []byte {
		t.Helper()
		matches := func(data []byte) bool {
			var envelope struct {
				Type         string `json:"type"`
				ControllerID string `json:"controller_id"`
				ViewerID     string `json:"viewer_id"`
			}
			_ = json.Unmarshal(data, &envelope)
			return envelope.Type == kind && (controllerID == "" || envelope.ControllerID == controllerID) && (viewerID == "" || envelope.ViewerID == viewerID)
		}
		for index, data := range backlog {
			if matches(data) {
				backlog = append(backlog[:index], backlog[index+1:]...)
				return data
			}
		}
		for {
			select {
			case data := <-messages:
				if matches(data) {
					return data
				}
				var envelope ws.Envelope
				_ = json.Unmarshal(data, &envelope)
				if envelope.Type == ws.TypePTYExited && kind != ws.TypePTYExited {
					t.Fatalf("unexpected provider exit: %s", data)
				}
				backlog = append(backlog, data)
			case <-ctx.Done():
				t.Fatalf("wait %s/%s/%s: %v", kind, controllerID, viewerID, ctx.Err())
			}
		}
	}

	before, err := client.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mcp, mcpCancel := context.WithCancel(ctx)
	mcpWriter, err := client.AttachSessionWithOptions(mcp, started.Session, egg.AttachOptions{Claim: true, Owner: "mcp:fixture"})
	if err != nil {
		t.Fatal(err)
	}
	send(ws.PTYAttach{Type: ws.TypePTYAttach, SessionID: started.Session, ControllerID: "browser-one", PublicKey: pub(firstKey), UserID: "fixture", OrgRole: "admin", Rows: 35, Cols: 110})
	if diagnostic := await(ws.TypeError, "browser-one", ""); !strings.Contains(string(diagnostic), "terminal input owned") {
		t.Fatalf("busy browser error: %s", diagnostic)
	}
	send(ws.PTYAttach{Type: ws.TypePTYAttach, SessionID: started.Session, ControllerID: "browser-one", PublicKey: pub(firstKey), UserID: "fixture", OrgRole: "admin", Takeover: true, Rows: 35, Cols: 110})
	await(ws.TypePTYStarted, "browser-one", "")
	dimensions := eggclient.ReadEggMetaValues(eggDir)
	if dimensions["rows"] != "35" || dimensions["cols"] != "110" {
		t.Fatalf("snapshot acknowledged before initial dimensions: rows=%s cols=%s", dimensions["rows"], dimensions["cols"])
	}
	for {
		_, err := mcpWriter.Recv()
		if err != nil {
			if status.Code(err) != codes.Aborted {
				t.Fatalf("MCP writer not revoked: %v", err)
			}
			break
		}
	}
	mcpCancel()
	if err := resizeBrowserInput(ctx, started.Session, "browser-one", pub(firstKey), "fixture", 35, 110); err != nil {
		t.Fatalf("acknowledged browser resize: %v", err)
	}
	for _, viewerID := range []string{"viewer-a", "viewer-b"} {
		send(ws.PTYAttach{Type: ws.TypePTYAttach, SessionID: started.Session, Spectate: true, ViewerID: viewerID, PublicKey: pub(firstKey), UserID: "fixture", OrgRole: "admin"})
		await(ws.TypePTYStarted, "", viewerID)
	}
	send(ws.PTYAttach{Type: ws.TypePTYAttach, SessionID: started.Session, ControllerID: "browser-two", PublicKey: pub(secondKey), UserID: "fixture", OrgRole: "admin"})
	await(ws.TypeError, "browser-two", "")
	send(ws.PTYAttach{Type: ws.TypePTYAttach, SessionID: started.Session, ControllerID: "browser-two", PublicKey: pub(secondKey), UserID: "fixture", OrgRole: "admin", Takeover: true})
	await(ws.TypePTYStarted, "browser-two", "")
	if err := resizeBrowserInput(ctx, started.Session, "browser-one", pub(firstKey), "fixture", 40, 120); err == nil {
		t.Fatal("old browser resized replacement")
	}
	gcm, err := auth.DeriveSharedKey(secondKey, pub(wingKey), "wt-pty")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := auth.Encrypt(gcm, []byte("browser-input\n"))
	if err != nil {
		t.Fatal(err)
	}
	send(ws.PTYInput{Type: ws.TypePTYInput, SessionID: started.Session, Data: encoded})
	for _, viewerID := range []string{"viewer-a", "viewer-b"} {
		var text strings.Builder
		for !strings.Contains(text.String(), "result:browser-input") {
			var frame ws.PTYOutput
			_ = json.Unmarshal(await(ws.TypePTYOutput, "", viewerID), &frame)
			viewerGCM, _ := auth.DeriveSharedKey(firstKey, pub(wingKey), "wt-pty")
			decoded, err := auth.Decrypt(viewerGCM, frame.Data)
			if err != nil {
				t.Fatal(err)
			}
			if frame.Compressed {
				reader, err := gzip.NewReader(bytes.NewReader(decoded))
				if err != nil {
					t.Fatal(err)
				}
				decoded, err = io.ReadAll(reader)
				_ = reader.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			text.Write(decoded)
		}
	}
	send(ws.PTYDetach{Type: ws.TypePTYDetach, SessionID: started.Session, ControllerID: "browser-one"})
	if err := resizeBrowserInput(ctx, started.Session, "browser-two", pub(secondKey), "fixture", 40, 120); err != nil {
		t.Fatalf("stale detach removed replacement: %v", err)
	}
	send(ws.PTYDetach{Type: ws.TypePTYDetach, SessionID: started.Session, ControllerID: "browser-two"})
	for {
		state, err := client.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if state.WriterId == "" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(5 * time.Millisecond):
		}
	}
	reattach, err := client.AttachSessionWithOptions(ctx, started.Session, egg.AttachOptions{Claim: true, Owner: "cli:reattach"})
	if err != nil {
		t.Fatal(err)
	}
	defer reattach.CloseSend()
	after, err := client.Status(ctx)
	if err != nil || after.ProcessPid != before.ProcessPid || after.Readers < 4 {
		t.Fatalf("provider/observers not retained: %v %v", after, err)
	}
	send(ws.PTYAttach{Type: ws.TypePTYAttach, SessionID: started.Session, ControllerID: "browser-three", PublicKey: pub(secondKey), UserID: "fixture", OrgRole: "admin", Takeover: true})
	await(ws.TypePTYStarted, "browser-three", "")
	close(input) // loss of relay session channel releases this bridge's writer.
	for {
		state, err := client.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if state.WriterId == "" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(5 * time.Millisecond):
		}
	}
	final, err := client.AttachSessionWithOptions(ctx, started.Session, egg.AttachOptions{Claim: true, Owner: "cli:after-disconnect"})
	if err != nil {
		t.Fatal(err)
	}
	defer final.CloseSend()
	last, err := client.Status(ctx)
	if err != nil || last.ProcessPid != before.ProcessPid {
		t.Fatalf("disconnect stopped provider: %v %v", last, err)
	}
	if err := client.Kill(ctx, started.Session); err != nil {
		t.Fatal(err)
	}
	stopped = true
	await(ws.TypePTYExited, "", "")
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("background observer lost provider exit")
	}
	t.Logf("provider PID%d survived MCP/browser ownership and two encrypted observers; stale resize/detach refused; independent observer reported actual exit", before.ProcessPid)
}
