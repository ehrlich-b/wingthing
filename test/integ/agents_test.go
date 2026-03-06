//go:build e2e

package integ

import (
	"context"
	"encoding/base64"
	"testing"
	"time"

	"github.com/coder/websocket/wsjson"

	"github.com/ehrlich-b/wingthing/internal/ws"
)

// TestAllAgentsPTYLifecycle verifies the full PTY lifecycle for every supported agent.
// Each agent goes through: start → started → output → input → exited.
func TestAllAgentsPTYLifecycle(t *testing.T) {
	agents := []string{"claude", "codex", "cursor", "gemini", "ollama", "opencode"}

	for _, agent := range agents {
		t.Run(agent, func(t *testing.T) {
			_, ts, store := testRelayAndWS(t)
			token, _ := createTestUser(t, store, "agent-"+agent)

			wingConn := connectWing(t, wsURL(ts), token, "wing-"+agent, []string{agent})
			defer wingConn.CloseNow()
			browser := connectBrowser(t, wsURL(ts), token, "wing-"+agent)
			defer browser.CloseNow()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			// Browser sends pty.start
			sess := startSession(t, browser, wingConn, agent, "wing-"+agent)

			// Verify wing received the correct agent name
			// (startSession already validated session_id; re-read on wing isn't needed)

			// Wing sends pty.started
			wsjson.Write(ctx, wingConn, ws.PTYStarted{
				Type:      ws.TypePTYStarted,
				SessionID: sess,
				Agent:     agent,
			})

			var started ws.PTYStarted
			if err := wsjson.Read(ctx, browser, &started); err != nil {
				t.Fatalf("browser read pty.started: %v", err)
			}
			if started.Agent != agent {
				t.Errorf("started.agent = %q, want %q", started.Agent, agent)
			}

			// Wing sends output
			outputText := "hello from " + agent
			outData := base64.StdEncoding.EncodeToString([]byte(outputText))
			wsjson.Write(ctx, wingConn, ws.PTYOutput{
				Type:      ws.TypePTYOutput,
				SessionID: sess,
				Data:      outData,
			})

			var output ws.PTYOutput
			if err := wsjson.Read(ctx, browser, &output); err != nil {
				t.Fatalf("browser read pty.output: %v", err)
			}
			decoded, _ := base64.StdEncoding.DecodeString(output.Data)
			if string(decoded) != outputText {
				t.Errorf("output = %q, want %q", decoded, outputText)
			}

			// Browser sends input
			inputText := "input to " + agent
			inputData := base64.StdEncoding.EncodeToString([]byte(inputText))
			wsjson.Write(ctx, browser, ws.PTYInput{
				Type:      ws.TypePTYInput,
				SessionID: sess,
				Data:      inputData,
			})

			var wingInput ws.PTYInput
			if err := wsjson.Read(ctx, wingConn, &wingInput); err != nil {
				t.Fatalf("wing read pty.input: %v", err)
			}
			decodedInput, _ := base64.StdEncoding.DecodeString(wingInput.Data)
			if string(decodedInput) != inputText {
				t.Errorf("input = %q, want %q", decodedInput, inputText)
			}

			// Wing sends exited
			wsjson.Write(ctx, wingConn, ws.PTYExited{
				Type:      ws.TypePTYExited,
				SessionID: sess,
				ExitCode:  0,
			})

			var exited ws.PTYExited
			if err := wsjson.Read(ctx, browser, &exited); err != nil {
				t.Fatalf("browser read pty.exited: %v", err)
			}
			if exited.ExitCode != 0 {
				t.Errorf("exit code = %d, want 0", exited.ExitCode)
			}
		})
	}
}

// TestAgentCWDPassthrough verifies working directory is forwarded to wing.
func TestAgentCWDPassthrough(t *testing.T) {
	_, ts, store := testRelayAndWS(t)
	token, _ := createTestUser(t, store, "cwd")

	wingConn := connectWing(t, wsURL(ts), token, "wing-cwd", []string{"claude"})
	defer wingConn.CloseNow()
	browser := connectBrowser(t, wsURL(ts), token, "wing-cwd")
	defer browser.CloseNow()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := ws.PTYStart{
		Type:   ws.TypePTYStart,
		Agent:  "claude",
		WingID: "wing-cwd",
		CWD:    "/home/user/project",
		Cols:   80,
		Rows:   24,
	}
	wsjson.Write(ctx, browser, start)

	var wingStart ws.PTYStart
	wsjson.Read(ctx, wingConn, &wingStart)

	if wingStart.CWD != "/home/user/project" {
		t.Errorf("cwd = %q, want %q", wingStart.CWD, "/home/user/project")
	}
}
