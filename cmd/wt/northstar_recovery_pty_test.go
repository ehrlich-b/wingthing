package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
)

// The authenticated wing seam supplies metadata-only fake run records. The
// standalone egg, CLI, SSH argv and terminal are real, independently owned
// fixtures; killing the parent navigator does not own or stop the egg.
func TestNorthstarPTYRecovery(t *testing.T) {
	t.Run("symlinked-TMPDIR-coordinator-loss", func(t *testing.T) {
		f := northstarNewFixture(t)
		f.evidence = filepath.Join(f.repo, ".scratch", "evidence", "09", "pty")
		if err := os.MkdirAll(f.evidence, 0700); err != nil {
			t.Fatal(err)
		}
		binary := northstarBuild(t, f, "stable")
		if info, err := os.Lstat(f.tmp); err != nil || info.Mode()&os.ModeSymlink == 0 || !strings.HasPrefix(binary, f.tmp+string(filepath.Separator)) {
			t.Fatal("fixture did not launch through a test-created TMPDIR alias")
		}
		for _, dir := range []string{f.local, f.remote, filepath.Join(f.home, "mailbox")} {
			if !strings.HasPrefix(dir, f.home+string(filepath.Separator)) || strings.HasPrefix(dir, f.work+string(filepath.Separator)) {
				t.Fatal("state/mailbox outside protected fixture HOME")
			}
		}
		local := northstarStandaloneEgg(t, f, f.local, "same", "surviving-parent", "working", "RECOVERY_LOCAL_LIVE")
		forge := northstarStandaloneEgg(t, f, f.remote, "same", "forge-parent", "working", "RECOVERY_FORGE_LIVE")
		// Preserve the existing fake SSH path; the aggregate wing socket owns all
		// history routing, with identities that collide only before qualification.
		northstarWrite(t, filepath.Join(f.local, "remotes.yaml"), fmt.Sprintf("remotes:\n  forge:\n    ssh_target: ns-forge\n    wing_id: wing-forge\n    wingthing_dir: %s\n    control_socket: %s/control.sock\n    control_version: v1\n  legacy:\n    ssh_target: ns-offline\n", f.remote, f.remote), 0600)
		f.env = append(f.env, "WT_MCP_CLIENT=recovery-fixture")
		var mutations atomic.Int32
		var mu sync.Mutex
		mode := "normal"
		callCount := map[string]int{}
		socket, err := controlsocket.Path(f.local)
		if err != nil {
			t.Fatal(err)
		}
		northstarSocket(t, socket)
		for _, state := range []string{f.local, f.remote} {
			p, err := controlsocket.Path(state)
			if err != nil {
				t.Fatal(err)
			}
			northstarSocket(t, p)
		}
		server, err := controlsocket.Listen(t.Context(), f.local, "wing-local", func(hello controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
			if hello.Client != "recovery-fixture" {
				return controlsocket.Welcome{}, nil, fmt.Errorf("fixture rejects missing aggregate/client authority")
			}
			return controlsocket.Welcome{Principal: "recovery-fixture"}, func(_ context.Context, req control.DirectRequest) control.DirectResponse {
				mu.Lock()
				defer mu.Unlock()
				callCount[req.Tool]++
				response := control.DirectResponse{Version: control.ContractVersion, ID: req.ID}
				if !hello.Aggregate {
					response.Error = "fixture rejects non-aggregate history call"
					return response
				}
				var args map[string]any
				if err := json.Unmarshal(req.Arguments, &args); err != nil {
					response.Error = err.Error()
					return response
				}
				wing, _ := args["wing_id"].(string)
				switch req.Tool {
				case "wing_list":
					response.Result = map[string]any{"count": 3, "wings": []any{map[string]any{"name": "local", "wing_id": "wing-local", "mcp_control": true}, map[string]any{"name": "forge", "wing_id": "wing-forge", "hostname": "ns-forge", "wingthing_dir": f.remote, "mcp_control": true}, map[string]any{"name": "legacy", "mcp_control": false}}}
				case "agent_list":
					if mode == "denied" {
						response.Error = "policy denied history"
						break
					}
					if mode == "incomplete" {
						response.Result = map[string]any{"wing_id": wing, "count": 0, "snapshot_revision": "fixture"}
						break
					}
					if args["limit"] != float64(100) || (wing != "wing-local" && wing != "wing-forge") {
						response.Error = "unbounded or unqualified request"
						break
					}
					cursor, _ := args["cursor"].(string)
					var rows []any
					row := func(id, status, failure string) map[string]any {
						created := "2026-10-11T12:00:00Z"
						if wing == "wing-local" && id == "same-run" {
							created = "2026-10-11T13:00:00Z"
						}
						return map[string]any{"wing_id": wing, "run_id": id, "session_id": "same", "agent": "fixture", "cwd": f.work, "status": status, "failure_kind": failure, "terminal": status != "running", "created_at": created, "output_availability": "available", "parent_id": "parent-run", "cleanup": "unknown"}
					}
					next := ""
					if wing == "wing-forge" {
						rows = []any{row("same-run", "running", "")}
					} else if cursor == "" {
						rows = []any{row("same-run", "running", ""), row("complete-record", "done", ""), row("failed-record", "failed", "provider_error"), row("cancelled-record", "stopped", "stopped"), row("unknown-record", "failed", "unknown_outcome")}
						for i := 0; len(rows) < 100; i++ {
							rows = append(rows, row(fmt.Sprintf("filler-%03d", i), "done", ""))
						}
						next = "page-two"
					} else if cursor == "page-two" {
						rows = []any{row("older-record", "done", "")}
					} else {
						response.Error = "unexpected cursor"
						break
					}
					response.Result = map[string]any{"wing_id": wing, "count": len(rows), "runs": rows, "snapshot_revision": "fixture", "next_cursor": next}
				case "terminal_list":
					state := f.local
					if wing == "wing-forge" {
						state = f.remote
					} else if wing != "wing-local" {
						response.Error = "unknown wing"
						break
					}
					live := []any{}
					if pid, alive := eggclient.ReadAliveEggPID(filepath.Join(state, "eggs", "same")); alive {
						live = append(live, map[string]any{"id": "same", "pid": pid, "status": "working"})
					}
					response.Result = map[string]any{"wing_id": wing, "sessions": live}
				case "agent_result":
					if mode == "denied" {
						response.Error = "policy denied result"
						break
					}
					if args["max_chars"] != float64(8192) {
						response.Error = "result bound missing"
						break
					}
					response.Result = map[string]any{"wing_id": wing, "run_id": args["run_id"], "status": "stopped", "ready": true, "output": "METADATA_ONLY_RESULT\n\x1b]52;fake\a\u202e"}
				default:
					mutations.Add(1)
					response.Error = "fixture forbids history mutation"
				}
				return response
			}, nil
		}, northstarRecoveryAttach(t, f, f.local))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = server.Close() })
		remoteServer, err := controlsocket.Listen(t.Context(), f.remote, "wing-forge", func(hello controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
			if hello.Client != "recovery-fixture" {
				return controlsocket.Welcome{}, nil, fmt.Errorf("fixture client mismatch")
			}
			return controlsocket.Welcome{}, func(_ context.Context, req control.DirectRequest) control.DirectResponse {
				return control.DirectResponse{Version: control.ContractVersion, ID: req.ID, Error: "no history read bypass on remote fixture"}
			}, nil
		}, northstarRecoveryAttach(t, f, f.remote))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = remoteServer.Close() })

		h := northstarStartPTY(t, f, binary, 160, 36)
		h.expect("original parent / surviving egg", 0, "surviving-parent")
		h.expect("find durable runs without remembered IDs", h.send("h"), "Recent runs", "same-run", "legacy SSH-only profile", "N loads the next page")
		h.expect("captured qualified local run", h.send("/wing-local:same-run\r"), "> running wing-local:same-run")
		h.expect("attach surviving exact local egg", h.send("\r"), "RECOVERY_LOCAL_LIVE")
		select {
		case id := <-local.attached:
			if id != "same" {
				t.Fatal(id)
			}
		case <-time.After(time.Second):
			t.Fatal("local run attachment did not execute")
		}
		northstarRecoveryDetach(t, h, "detach preserves qualified run", "> running wing-local:same-run")
		// Simulate coordinator/terminal loss with the exact owned child, while the
		// wing metadata fixture and verified egg identities remain independent.
		if err := syscall.Kill(-h.cmd.Process.Pid, syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		select {
		case <-h.done:
		case <-time.After(3 * time.Second):
			t.Fatal("lost parent not reaped")
		}
		if _, alive := eggclient.ReadAliveEggPID(local.dir); !alive {
			t.Fatal("parent loss killed surviving egg")
		}
		fresh := northstarStartPTY(t, f, binary, 160, 36)
		fresh.expect("fresh parent inventory", 0, "surviving-parent")
		fresh.expect("fresh wt recovers same durable IDs", fresh.send("h"), "wing-local:same-run", "wing-forge:same-run")
		fresh.expect("same ID on another wing", fresh.send("/wing-forge:same-run\r"), "> running wing-forge:same-run")
		fresh.expect("saved exact forge route attach", fresh.send("\r"), "RECOVERY_FORGE_LIVE")
		select {
		case id := <-forge.attached:
			if id != "same" {
				t.Fatal(id)
			}
		case <-time.After(time.Second):
			t.Fatal("qualified forge attachment did not execute")
		}
		northstarRecoveryDetach(t, fresh, "forge detach", "> running wing-forge:same-run")
		if len(local.attached) != 0 {
			t.Fatal("forge attach hit colliding local egg")
		}
		// Remove active egg metadata; durable terminal records remain in the wing
		// response and cannot become attachable simply because their ID survives.
		for _, state := range []string{f.local, f.remote} {
			if err := os.Remove(filepath.Join(state, "eggs", "same", "egg.pid")); err != nil {
				t.Fatal(err)
			}
		}
		refreshAfter := fresh.send("/\x15record\rR")
		fresh.expect("terminal records after egg metadata loss", refreshAfter, "done wing-local:complete-record", "failed wing-local:failed-record", "stopped wing-local:cancelled-record", "unknown wing-local:unknown-record")
		fresh.waitScreen("completed refresh confirms live metadata gone", refreshAfter, func(screen string) bool {
			return strings.Contains(screen, "4 visible") && strings.Contains(screen, "unknown-record") && !strings.Contains(screen, "cleanup:unknown live")
		})
		fresh.expect("cancelled run filter", fresh.send("/\x15cancelled-record\r"), "> stopped wing-local:cancelled-record")
		fresh.expect("terminal Enter opens metadata", fresh.send("\r"), "Run details: wing-local:cancelled-record", "Live session verified: false", "wt agent recover", "--snapshot PATH")
		fresh.expect("explicit bounded result / sanitized controls", fresh.send("o"), "Result: wing-local:cancelled-record", "METADATA_ONLY_RESULT", "?]52;fake??", "cancellation is not completion")
		fresh.expect("unknown admission never launches", fresh.send("\x1b/\x15unknown-record\r\rn"), "Run details: wing-local:unknown-record", "Status: unknown", "never resend automatically")
		fresh.expect("pagination target first page", fresh.send("\x1b/\x15wing-local:same-run\r"), "same-run", "N loads the next page")
		mu.Lock()
		beforePages := callCount["agent_list"]
		mu.Unlock()
		fresh.expect("explicit next page", fresh.send("/\x15\rN"), "older-record")
		mu.Lock()
		afterPages := callCount["agent_list"]
		mu.Unlock()
		if afterPages != beforePages+1 {
			t.Fatalf("next page made %d inventory calls; want one", afterPages-beforePages)
		}
		fresh.expect("restart first pages", fresh.send("R/\x15unknown-record\r"), "unknown-record")
		mu.Lock()
		mode = "incomplete"
		mu.Unlock()
		fresh.expect("incomplete projection retains stale known row", fresh.send("R"), "history unavailable; stale", "unknown-record")
		fresh.expect("incomplete Enter cannot attach", fresh.send("\r"), "Run details: wing-local:unknown-record", "Stale history", "Live session verified: false")
		fresh.expect("return from stale metadata", fresh.send("\x1b"), "Recent runs")
		mu.Lock()
		mode = "denied"
		mu.Unlock()
		fresh.expect("policy denial remains unavailable", fresh.send("R"), "policy denied history", "unknown-record")
		fresh.expect("result denial shown without cached content", fresh.send("o"), "Result unavailable: policy denied result")
		screen, _ := fresh.screen()
		if strings.Contains(screen, "METADATA_ONLY_RESULT") {
			t.Fatal("prior result cached across policy denial")
		}
		fresh.expect("return from denied result", fresh.send("\x1b"), "Recent runs")
		fresh.exit("q")
		if mutations.Load() != 0 || local.killed.Load() != 0 || forge.killed.Load() != 0 {
			t.Fatal("recovery admitted/stopped/steered a run")
		}
		northstarWrite(t, filepath.Join(f.evidence, "recovery-receipt.txt"), "same wing/run/session IDs survive parent loss; metadata-only outputs; explicit page calls=1; no mutations\n", 0600)
	})
}

func northstarRecoveryAttach(t *testing.T, f *northstarFixture, state string) controlsocket.AttachBind {
	t.Helper()
	return func(ctx context.Context, hello controlsocket.Hello) (controlsocket.Welcome, controlsocket.AttachHandler, error) {
		if hello.Client != "recovery-fixture" || hello.Attach == nil || hello.Attach.Session != "same" {
			return controlsocket.Welcome{}, nil, fmt.Errorf("fixture attachment identity mismatch")
		}
		if _, alive := eggclient.ReadAliveEggPID(filepath.Join(state, "eggs", "same")); !alive {
			return controlsocket.Welcome{}, nil, fmt.Errorf("fixture egg is not live")
		}
		return controlsocket.Welcome{}, func(ctx context.Context, stream *controlsocket.SessionStream) error {
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			defer stream.Close()
			_, client, err := eggclient.OpenLocalEgg(ctx, &config.Config{Dir: state}, "same")
			if err != nil {
				return err
			}
			defer client.Close()
			session, err := client.AttachSessionWithOptions(ctx, "same", egg.AttachOptions{NativeTerminal: hello.Attach.NativeTerminal})
			if err != nil {
				return err
			}
			done := make(chan error, 2)
			go func() {
				for {
					msg, err := session.Recv()
					if err != nil {
						done <- err
						return
					}
					if err := stream.Send(msg); err != nil {
						done <- err
						return
					}
				}
			}()
			go func() {
				for {
					msg, err := stream.Recv()
					if err != nil {
						done <- err
						return
					}
					if err := session.Send(msg); err != nil {
						done <- err
						return
					}
				}
			}()
			err = <-done
			cancel()
			_ = stream.Close()
			_ = client.Close()
			<-done
			if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}, nil
	}
}

// A native reset may expose an old alternate screen while the child is still
// unwinding. Require the new dashboard entry and its complete render after it.
func northstarRecoveryDetach(t *testing.T, h *northstarPTY, label, selected string) {
	t.Helper()
	const entry = "\x1b[?1049h\x1b[?25l\x1b[?7l\x1b[?2004h"
	h.mu.Lock()
	before := bytes.Count(h.transcript.Bytes(), []byte(entry))
	h.mu.Unlock()
	after := h.send("\x02q")
	h.waitScreen(label, after, func(screen string) bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		data := h.transcript.Bytes()
		at := bytes.LastIndex(data, []byte(entry))
		return bytes.Count(data, []byte(entry)) > before && at >= 0 && bytes.Contains(data[at:], []byte("\x1b[36;1H")) && strings.Contains(screen, "Recent runs") && strings.Contains(screen, selected)
	})
}
