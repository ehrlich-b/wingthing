package main

// Linux fixture adaptation of TestNorthstarPTYScopedNew. All journey assertions are retained;
// original Northstar tests are unchanged. See test/storyboard/README.md.
import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/testprovider"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func storyboardScopedNew(t *testing.T) {
	build := northstarNewFixture(t)
	built := storyboardBinary(t, build)
	if !strings.HasPrefix(built, build.tmp+string(filepath.Separator)) {
		t.Fatal("built CLI did not traverse the fixture TMPDIR alias")
	}
	for _, machine := range []string{"local", "forge"} {
		// This transport-only subcase remains runnable on restricted hosts. It
		// checks actual TUI/CLI argv and attachment; it is not a sandbox pass.
		t.Run("routing/"+machine, func(t *testing.T) {
			f := northstarNewFixture(t)
			binary, state, cwd := scopedNewMachine(t, f, built, machine)
			launches := make(chan scopedNewLaunch, 2)
			var attachments, detaches atomic.Int32
			const id = "scoped-parent"
			const marker = "SCOPED_PARENT_ROUTE_READY"
			server, err := controlsocket.Listen(t.Context(), state, "fixture-wing", func(controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
				return controlsocket.Welcome{}, func(_ context.Context, request control.DirectRequest) control.DirectResponse {
					if request.Tool != "agent_start" {
						t.Errorf("unexpected scoped creation tool: %s", request.Tool)
					}
					var launch scopedNewLaunch
					if err := json.Unmarshal(request.Arguments, &launch); err != nil {
						t.Error(err)
					}
					launches <- launch
					return control.DirectResponse{Version: control.ContractVersion, ID: request.ID, Result: map[string]any{"session": id}}
				}, nil
			}, func(_ context.Context, hello controlsocket.Hello) (controlsocket.Welcome, controlsocket.AttachHandler, error) {
				if hello.Attach.Session != id {
					return controlsocket.Welcome{}, nil, fmt.Errorf("wrong parent: %s", hello.Attach.Session)
				}
				attachments.Add(1)
				return controlsocket.Welcome{}, func(_ context.Context, stream *controlsocket.SessionStream) error {
					if err := stream.Send(&pb.SessionMsg{SessionId: id, Payload: &pb.SessionMsg_Output{Output: []byte(marker + "\r\n")}}); err != nil {
						return err
					}
					for {
						msg, err := stream.Recv()
						if err != nil {
							return err
						}
						if msg.GetDetach() {
							detaches.Add(1)
							return nil
						}
					}
				}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = server.Close() })
			h := scopedNewFromDashboard(t, f, binary, machine, cwd, marker)
			launch := <-launches
			if launch.Agent != "claude" || launch.CWD != cwd || launch.Role != "parent" || !launch.ScopedMCP || len(launch.Args) != 0 {
				t.Fatalf("TUI bypassed scoped launch: %+v", launch)
			}
			if _, err := uuid.Parse(launch.RequestID); err != nil {
				t.Fatal(err)
			}
			h.expect("detach / back to navigator", h.send("\x02q"), "wt |", "n new")
			h.exit("q")
			ref := id
			if machine == "forge" {
				scopedNewAssertSSH(t, f, state, cwd)
				ref = "forge:" + id
			}
			fresh := northstarStartPTY(t, f, binary, 120, 30, "attach", "--", ref)
			fresh.expect("fresh client / same parent", 0, marker)
			fresh.exit("\x02q")
			if attachments.Load() != 2 || detaches.Load() != 2 || len(launches) != 0 {
				t.Fatalf("detach relaunched/stopped parent: attaches=%d detaches=%d launches=%d", attachments.Load(), detaches.Load(), len(launches))
			}
		})
		t.Run("native/"+machine, func(t *testing.T) {
			coldRequireSandbox(t)
			f := northstarNewFixture(t)
			binary, state, cwd := scopedNewMachine(t, f, built, machine)
			receiver := *f
			receiver.local, receiver.work = state, cwd
			receiver.env = append(append([]string{}, f.env...), "WINGTHING_DIR="+state)
			coldCleanup(t, &receiver, binary)
			storyboardStateEvidence(t, f, state)
			scopedNewNativeFixture(t, f, state, cwd)
			storyboardWriteLinuxPolicy(t, f, state)
			coldPolicyGuard(t, &receiver)
			gate := testprovider.NewCompletionGate(t, cwd)
			started := time.Now()
			h := scopedNewFromDashboard(t, f, binary, machine, cwd, "PARENT_READY")
			t.Logf("%s n-to-native-scoped-parent: %s", machine, time.Since(started))
			var captured struct {
				CWD             string           `json:"cwd"`
				Receipts        []map[string]any `json:"receipts"`
				Mailbox         string           `json:"mailbox"`
				ParentSessionID string           `json:"parent_session_id"`
			}
			data, err := os.ReadFile(filepath.Join(cwd, "receipts.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(data, &captured); err != nil || captured.CWD != cwd || len(captured.Receipts) != 1 || captured.ParentSessionID == "" {
				t.Fatalf("incorrect native launch metadata: %s %v", data, err)
			}
			if !strings.HasPrefix(captured.Mailbox, filepath.Join(cwd, ".wingthing-conversations")+string(filepath.Separator)) {
				t.Fatalf("mailbox exchange outside selected cwd: %s", captured.Mailbox)
			}
			// Exchange queues are writable; broker authority and wing state remain
			// protected under HOME, never in the provider's writable workspace.
			if !strings.HasPrefix(state, f.home+string(filepath.Separator)) || strings.HasPrefix(state, cwd+string(filepath.Separator)) {
				t.Fatal("host authority was placed in the provider workspace")
			}
			cfg := &config.Config{Dir: state}
			link := eggclient.SessionConversationLink(cfg, captured.ParentSessionID)
			if link.ConversationRole != "parent" || link.ConversationID == "" || link.ParentConversationID != "" {
				t.Fatalf("visible Claude has no parent conversation: %+v", link)
			}
			parentDir := filepath.Join(state, "eggs", captured.ParentSessionID)
			registrationPath := filepath.Join(state, "conversation-brokers", captured.ParentSessionID, "registration.json")
			data, err = os.ReadFile(registrationPath)
			if err != nil {
				t.Fatal(err)
			}
			var registration struct {
				Scoped    bool   `json:"scoped"`
				StateDir  string `json:"state_dir"`
				Workspace string `json:"workspace"`
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(data, &registration); err != nil || !registration.Scoped || registration.StateDir != state || registration.Workspace != cwd || registration.SessionID != captured.ParentSessionID {
				t.Fatalf("scoped mailbox authority changed: %s %v", data, err)
			}
			northstarWrite(t, filepath.Join(f.evidence, "scoped-new-"+machine+"-registration.json"), string(data), 0600)
			meta := eggclient.ReadEggMetaValues(parentDir)
			if meta["agent"] != "claude" || meta["cwd"] != cwd {
				t.Fatalf("wrong visible parent metadata: %v", meta)
			}
			receipt := captured.Receipts[0]
			childID := receipt["session_id"].(string)
			childDir := filepath.Join(state, "eggs", childID)
			northstarSocket(t, filepath.Join(parentDir, "egg.sock"))
			northstarSocket(t, filepath.Join(childDir, "egg.sock"))
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			if err := gate.WaitReady(ctx); err != nil {
				t.Fatal(err)
			}
			parentPID, parentAlive := eggclient.ReadAliveEggPID(parentDir)
			childPID, childAlive := eggclient.ReadAliveEggPID(childDir)
			if !parentAlive || !childAlive {
				t.Fatal("native parent/child did not become ready")
			}
			h.expect("native detach / parent and child retained", h.send("\x02q"), "wt |", "n new")
			h.exit("q")
			ref := captured.ParentSessionID
			if machine == "forge" {
				scopedNewAssertSSH(t, f, state, cwd)
				ref = "forge:" + ref
				if _, err := os.Stat(filepath.Join(f.local, "wing.pid")); !os.IsNotExist(err) {
					t.Fatalf("remote cold launch started a local wing: %v", err)
				}
			}
			freshPTY := northstarStartPTY(t, f, binary, 120, 30, "attach", "--", ref)
			freshPTY.expect("native fresh viewer / original parent", 0, "PARENT_READY")
			freshPTY.expect("native mailbox reconnect", freshPTY.send("reconnect\r"), "MAILBOX_RECONNECTED")
			for dir, originalPID := range map[string]int{parentDir: parentPID, childDir: childPID} {
				if pid, alive := eggclient.ReadAliveEggPID(dir); !alive || pid != originalPID {
					t.Fatalf("detach changed owned process: %s %d/%d alive=%t", dir, originalPID, pid, alive)
				}
			}
			fresh, err := controlsocket.Dial(ctx, state, controlsocket.Hello{Aggregate: true})
			if err != nil {
				t.Fatal(err)
			}
			defer fresh.Close()
			args := map[string]any{"wing_id": receipt["wing_id"], "run_id": receipt["run_id"]}
			pending, denied, err := fresh.Call(ctx, "agent_result", mustSliceJSON(args))
			if err != nil || denied || pending["ready"] != false || pending["status"] != "pending" {
				t.Fatalf("fresh client lost gated child: %v %v", pending, err)
			}
			if err := gate.Release(); err != nil {
				t.Fatal(err)
			}
			waitArgs := map[string]any{"wing_id": receipt["wing_id"], "run_id": receipt["run_id"], "timeout_seconds": 30}
			if result, denied, err := fresh.Call(ctx, "agent_wait", mustSliceJSON(waitArgs)); err != nil || denied || result["status"] != "done" || result["timed_out"] == true {
				t.Fatalf("fresh client wait: %v %v", result, err)
			}
			result, denied, err := fresh.Call(ctx, "agent_result", mustSliceJSON(args))
			if err != nil || denied || result["ready"] != true || result["run_id"] != receipt["run_id"] || result["session_id"] != childID || result["wing_id"] != receipt["wing_id"] || !strings.Contains(fmt.Sprint(result["output"]), "Fake Codex fixture-model") {
				t.Fatalf("fresh client changed child identity/result: %v %v", result, err)
			}
			freshPTY.expect("original parent / same child results", freshPTY.send("recover\r"), "RECOVERED")
			var recovered []map[string]any
			data, err = os.ReadFile(filepath.Join(cwd, "results.json"))
			if err != nil || json.Unmarshal(data, &recovered) != nil || len(recovered) != 1 || recovered[0]["run_id"] != receipt["run_id"] || recovered[0]["session_id"] != childID || recovered[0]["wing_id"] != receipt["wing_id"] || recovered[0]["ready"] != true {
				t.Fatalf("reattached parent lost original run: %s %v", data, err)
			}
			freshPTY.exit("\x02q")
			coldOneDaemon(t, &receiver)
		})
	}
}
