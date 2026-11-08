package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/testsocket"
	"testing"
)

func TestTerminalReadHumanOutput(t *testing.T) {
	cfg := &config.Config{Dir: testsocket.Dir(t)}
	server, err := controlsocket.Listen(context.Background(), cfg.Dir, "sample-wing", func(controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
		return controlsocket.Welcome{Principal: "sample-owner"}, func(_ context.Context, r control.DirectRequest) control.DirectResponse {
			if r.Tool != "session_action" {
				t.Errorf("unexpected operation: %s", r.Tool)
			}
			return control.DirectResponse{Version: control.ContractVersion, ID: r.ID, Result: map[string]any{"route": map[string]any{"kind": "direct"}, "result": map[string]any{"session": "pairing", "ansi": "\x1b[2J\x1b[HTests passed\r\nReview complete\x1b]52;c;sample\x07"}}}
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	t.Setenv("WINGTHING_DIR", cfg.Dir)
	for _, jsonMode := range []bool{false, true} {
		cmd := sessionActionCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		args := []string{"--wing", "sample-wing", "--session", "pairing", "--operation", "terminal_read"}
		if jsonMode {
			args = append(args, "--json")
		}
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		if jsonMode {
			var data map[string]any
			if err := json.Unmarshal(out.Bytes(), &data); err != nil {
				t.Fatal(err)
			}
		} else {
			for _, word := range []string{"Tests passed", "Review complete", "sample-wing / pairing", "route direct"} {
				if !bytes.Contains(out.Bytes(), []byte(word)) {
					t.Errorf("missing %q: %s", word, out.String())
				}
			}
			for _, word := range []string{"base64", "\\r", "\x1b", "52;c;", "\"ansi\""} {
				if bytes.Contains(out.Bytes(), []byte(word)) {
					t.Errorf("unsafe envelope/control in text: %q", out.String())
				}
			}
		}
	}
}

func TestTerminalSnapshotRejectsInvalidEncodingAndHandlesCursorUpdates(t *testing.T) {
	var out bytes.Buffer
	if err := writeTerminalSnapshot(&out, map[string]any{"base64": "invalid!"}); err == nil {
		t.Fatal("invalid base64 admitted")
	}
	if err := writeTerminalSnapshot(&out, map[string]any{}); err == nil {
		t.Fatal("missing snapshot admitted")
	}
	if err := writeTerminalSnapshot(&out, map[string]any{"ansi": "old\rnew\r\nnext"}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "new\nnext\n" {
		t.Fatalf("rendered output: %q", out.String())
	}
}
func TestStoryboardTerminalReadPTY(t *testing.T) {
	f := northstarNewFixture(t)
	binary := northstarBuild(t, f, "stable")
	server, err := controlsocket.Listen(t.Context(), f.local, "sample-wing", func(controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
		return controlsocket.Welcome{Principal: "sample-owner"}, func(_ context.Context, r control.DirectRequest) control.DirectResponse {
			if r.Tool != "session_action" {
				t.Error("unexpected mutation")
			}
			var args map[string]any
			json.Unmarshal(r.Arguments, &args)
			if args["operation"] != "terminal_read" || args["origin_wing_id"] != "sample-wing" || args["session_id"] != "pairing" {
				t.Errorf("wrong target: %v", args)
			}
			return control.DirectResponse{Version: control.ContractVersion, ID: r.ID, Result: map[string]any{"route": map[string]any{"kind": "direct"}, "result": map[string]any{"session": "pairing", "ansi": "\x1b[2J\x1b[Hpairing tests\r\n12 passed, 0 failed\r\nReady for review."}}}
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	h := northstarStartPTY(t, f, binary, 120, 36, "session", "action", "--wing", "sample-wing", "--session", "pairing", "--operation", "terminal_read")
	h.expect("readable terminal snapshot", 0, "sample-wing / pairing", "route direct", "12 passed, 0 failed", "Ready for review.")
	<-h.done
	storyboardCapture(t, h, "13-5-terminal-read")
}
