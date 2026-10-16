package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
	"github.com/ehrlich-b/wingthing/internal/testprovider"
	"github.com/google/uuid"
)

type scopedNewLaunch struct {
	Agent, CWD, Label string
	Args              []string
	Role              string `json:"conversation_role"`
	RequestID         string `json:"request_id"`
	ScopedMCP         bool   `json:"scoped_mcp"`
}

const scopedNewClaudeProfileCheck = `with open(os.path.join(os.environ.get('CLAUDE_CONFIG_DIR',os.path.join(os.environ['HOME'],'.claude')),'settings.json')) as f: inherited=json.load(f)
assert inherited['model']=='fake-parent' and inherited['effortLevel']=='high' and inherited['theme']=='dark'`

func scopedNewNativeFixture(t *testing.T, f *northstarFixture, state, cwd string) string {
	t.Helper()
	// Match the default policy's protected SSH root; HOME itself is readable.
	sshDir := filepath.Join(f.home, ".ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(sshDir, "fixture-ssh-key")
	northstarWrite(t, key, "fake key; not a credential", 0600)
	controlPath, err := controlsocket.Path(state)
	if err != nil {
		t.Fatal(err)
	}
	// Reuse the protected-mailbox provider and its denial assertions. This
	// topology has one wing per receiving process, with one gated child.
	fixtureArgs, err := json.Marshal([]string{"--fixture-control", controlPath, "--fixture-state-file", filepath.Join(state, "wing.yaml"), "--fixture-ssh-key", key})
	if err != nil {
		t.Fatal(err)
	}
	script := strings.Replace(fakeScopedClaude, "assert args.count('--mcp-config')", "args += "+string(fixtureArgs)+"\nassert args.count('--mcp-config')", 1)
	// Personal stable sessions read their existing provider profile. The
	// agent_settings policy source applies to isolated users, not this launch.
	script = strings.Replace(script, "assert args[args.index('--model')+1]=='fake-parent'", scopedNewClaudeProfileCheck, 1)
	script = strings.Replace(script, "assert len(wings)==2", "assert len(wings)==1", 1)
	script = strings.Replace(script, "'receipts':receipts,", "'cwd':os.getcwd(),'argv':args,'receipts':receipts,", 1)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	northstarWrite(t, filepath.Join(f.bin, "claude"), "#!"+python+"\n"+script, 0700)
	northstarWrite(t, filepath.Join(f.bin, "codex"), "#!"+python+"\n"+testprovider.Codex, 0700)
	profile := filepath.Join(f.home, ".claude")
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	northstarWrite(t, filepath.Join(profile, "settings.json"), `{"model":"fake-parent","effortLevel":"high","theme":"dark"}`, 0600)
	yaml, err := testprovider.ScopedHomePolicy(f.home).YAML()
	if err != nil {
		t.Fatal(err)
	}
	northstarWrite(t, filepath.Join(state, "egg.yaml"), yaml, 0600)
	if err := config.SaveWingConfig(state, &config.WingConfig{Paths: config.PathList{{Path: cwd}}, Roost: "http://127.0.0.1:1", HostedRelay: config.HostedRelayDeny}); err != nil {
		t.Fatal(err)
	}
	return python
}

// Exercise the native provider's preference check even on hosts that cannot
// apply Seatbelt. A misplaced agent_settings source must fail before native QA.
func TestNorthstarScopedNewFixturePersonalProfile(t *testing.T) {
	for _, machine := range []string{"local", "devbox"} {
		t.Run(machine, func(t *testing.T) {
			f := northstarNewFixture(t)
			state := f.local
			if machine == "devbox" {
				state = f.remote
			}
			cwd := filepath.Join(f.work, machine+" project space")
			if err := os.Mkdir(cwd, 0700); err != nil {
				t.Fatal(err)
			}
			python := scopedNewNativeFixture(t, f, state, cwd)
			check := func() ([]byte, error) {
				cmd := exec.CommandContext(t.Context(), python, "-c", "import json, os\n"+scopedNewClaudeProfileCheck)
				cmd.Env, cmd.Dir = f.env, cwd
				return cmd.CombinedOutput()
			}
			if output, err := check(); err != nil {
				t.Fatalf("provider could not inherit the personal fixture profile: %v\n%s", err, output)
			}
			// Retain the strict check: neither missing nor mismatched preferences
			// can be mistaken for a valid provider launch.
			settings := filepath.Join(f.home, ".claude", "settings.json")
			northstarWrite(t, settings, `{"model":"wrong","effortLevel":"high","theme":"dark"}`, 0600)
			if output, err := check(); err == nil || !strings.Contains(string(output), "AssertionError") {
				t.Fatalf("provider accepted mismatched preferences: %v\n%s", err, output)
			}
			if err := os.Remove(settings); err != nil {
				t.Fatal(err)
			}
			if output, err := check(); err == nil || !strings.Contains(string(output), "FileNotFoundError") {
				t.Fatalf("provider accepted a missing personal profile: %v\n%s", err, output)
			}
		})
	}
}

func scopedNewMachine(t *testing.T, f *northstarFixture, built, machine string) (binary, state, cwd string) {
	t.Helper()
	binary = coldInstallBinary(t, f, built)
	state = f.local
	cwd = filepath.Join(f.work, "local project space")
	if machine == "devbox" {
		state = f.remote
		cwd = filepath.Join(f.root, "devbox project space")
	}
	if err := os.Mkdir(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	// A saved custom executable with spaces must survive dashboard routing.
	custom := filepath.Join(f.home, "wt custom")
	if err := os.Symlink(filepath.Join(f.home, "wt"), custom); err != nil {
		t.Fatal(err)
	}
	profiles := map[string]config.Remote{}
	if machine == "devbox" {
		profiles["devbox"] = config.Remote{SSHTarget: "ns-devbox", WTBinary: custom, WingthingDir: state}
	}
	if err := config.SaveRemotes(f.local, profiles); err != nil {
		t.Fatal(err)
	}
	// The fixture transport rejects any real host and records the actual TTY
	// mode and shell command. The receiving CLI alone resolves the cwd flag.
	northstarWrite(t, filepath.Join(f.bin, "ssh"), `#!/bin/sh
mode=$1
case "$mode" in -t|-T) shift ;; *) exit 90 ;; esac
[ "$1" = -o ] && shift 2
[ "$1" = ns-devbox ] || exit 91
shift
printf '%s\n%s\n' "$mode" "$1" > "$NORTHSTAR_SCOPED_SSH_LOG/$$"
exec /bin/sh -c "$1"
`, 0700)
	logDir := filepath.Join(f.home, "ssh-launches")
	if err := os.Mkdir(logDir, 0700); err != nil {
		t.Fatal(err)
	}
	f.env = append(f.env, "NORTHSTAR_SCOPED_SSH_LOG="+logDir)
	path, err := controlsocket.Path(state)
	if err != nil {
		t.Fatal(err)
	}
	northstarSocket(t, path)
	reserve := filepath.Join(state, "eggs", cmdutil.NewRuntimeID())
	if err := os.MkdirAll(reserve, 0700); err != nil {
		t.Fatal(err)
	}
	northstarSocket(t, filepath.Join(reserve, "egg.sock"))
	if err := os.Remove(reserve); err != nil {
		t.Fatal(err)
	}
	return binary, state, cwd
}

func scopedNewFromDashboard(t *testing.T, f *northstarFixture, binary, machine, cwd, ready string) *northstarPTY {
	t.Helper()
	h := northstarStartPTY(t, f, binary, 120, 30)
	h.expect("new scoped parent / dashboard", 0, "wt   ", "n new", "m machines")
	if machine == "devbox" {
		h.expect("select exact saved devbox", h.send("mjj\r"), "devbox", "No sessions yet")
	}
	h.expect("n / provider prompt", h.send("n"), "Agent (blank = shell)")
	h.expect("Claude / receiving cwd prompt", h.send("\x15claude\r"), "Machine: "+func() string {
		if machine == "local" {
			return "This machine"
		}
		return machine
	}())
	h.expect("scoped parent attached", h.send("\x15"+cwd+"\r"), ready)
	return h
}

func scopedNewAssertSSH(t *testing.T, f *northstarFixture, state, cwd string) {
	t.Helper()
	logDir := filepath.Join(f.home, "ssh-launches")
	entries, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatal(err)
	}
	want := "-t\nWINGTHING_DIR=" + remotepkg.ShellQuote(state) + " WINGTHING_PREVIEW_DIR=" + remotepkg.ShellQuote(state) + " " +
		remotepkg.ShellQuote(filepath.Join(f.home, "wt custom")) + " '--expected-channel' 'stable' 'claude' '--cwd' " + remotepkg.ShellQuote(cwd) + "\n"
	var logs strings.Builder
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(logDir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) == want {
			return
		}
		logs.Write(data)
	}
	t.Fatalf("saved interactive Claude route missing: want %q, got %s", want, logs.String())
}

func TestNorthstarPTYScopedNew(t *testing.T) {
	build := northstarNewFixture(t)
	built := northstarBuild(t, build, "stable")
	if !strings.HasPrefix(built, build.tmp+string(filepath.Separator)) {
		t.Fatal("built CLI did not traverse the fixture TMPDIR alias")
	}
	for _, machine := range []string{"local", "devbox"} {
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
			northstarWrite(t, filepath.Join(f.bin, "claude"), "#!/bin/sh\nexit 0\n", 0700)
			h := scopedNewFromDashboard(t, f, binary, machine, cwd, marker)
			launch := <-launches
			if launch.Agent != "claude" || launch.CWD != cwd || launch.Role != "parent" || !launch.ScopedMCP || len(launch.Args) != 0 {
				t.Fatalf("TUI bypassed scoped launch: %+v", launch)
			}
			if _, err := uuid.Parse(launch.RequestID); err != nil {
				t.Fatal(err)
			}
			h.expect("detach / back to navigator", h.send("\x02q"), "wt   ", "n new")
			h.exit("q")
			ref := id
			if machine == "devbox" {
				scopedNewAssertSSH(t, f, state, cwd)
				ref = "devbox:" + id
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
			scopedNewNativeFixture(t, f, state, cwd)
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
			h.expect("native detach / parent and child retained", h.send("\x02q"), "wt   ", "n new")
			h.exit("q")
			ref := captured.ParentSessionID
			if machine == "devbox" {
				scopedNewAssertSSH(t, f, state, cwd)
				ref = "devbox:" + ref
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
