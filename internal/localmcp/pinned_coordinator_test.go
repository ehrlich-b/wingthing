package localmcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/sessionrole"
	"github.com/ehrlich-b/wingthing/internal/sshcontrol"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/testssh"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
)

func TestRoleStartPinsAndAutomaticallyBindsFleetMCP(t *testing.T) {
	for _, role := range []string{"", "coordinator"} {
		t.Run("role="+role, func(t *testing.T) {
			f := parentLifecycleWing(t)
			spawn := f.service.Spawn
			f.service.Spawn = func(l *wingsession.Launch, o wingsession.StartOptions) (*egg.Client, error) {
				if _, err := spawn(l, o); err != nil {
					return nil, err
				}
				token := filepath.Join(f.server.Cfg.Dir, "fixture.token")
				if err := os.WriteFile(token, []byte("fixture"), 0600); err != nil {
					return nil, err
				}
				return egg.Dial(filepath.Join(f.server.Cfg.Dir, "unused.sock"), token)
			}

			var registration conversationBrokerRegistration
			f.server.startMailbox = func(reg conversationBrokerRegistration) error { registration = reg; return nil }
			f.server.captureWings = func(*Server) (map[string][]string, error) {
				return map[string][]string{f.server.Cfg.WingID: {f.service.Home}}, nil
			}
			wire, _ := json.Marshal(map[string]any{"agent": "claude", "cwd": f.service.Home, "label": "parent", "role": role, "scoped_mcp": true, "conversation_role": "parent", "request_id": "start"})
			result, err := f.server.toolAgentStart(wire)
			if err != nil {
				t.Fatal(err)
			}
			opts := <-f.spawned
			if opts.Egg.RoleMetadata.Role != role || opts.Egg.RoleMetadata.Pinned != (role != "") {
				t.Fatalf("metadata=%v", opts.Egg.RoleMetadata)
			}
			args := strings.Join(opts.Egg.AgentArgs, " ")
			if strings.Count(args, "--mcp-config") != 1 || !strings.Contains(args, "--strict-mcp-config") || !registration.Scoped || registration.Role != role {
				t.Fatalf("MCP=%s registration=%v", args, registration)
			}
			tools := strings.Join(registration.Tools, " ")
			if strings.Contains(tools, "terminal_list") != (role == "coordinator") {
				t.Fatalf("scope=%s", tools)
			}
			// Replay keeps one admission and one mailbox registration.
			replay, err := f.server.toolAgentStart(wire)
			if err != nil || replay["session"] != result["session"] {
				t.Fatalf("replay=%v %v", replay, err)
			}
			select {
			case <-f.spawned:
				t.Fatal("replay spawned another parent")
			default:
			}
			if opts.Egg.Principal == "" || len(opts.Egg.ProtectedWriteTargets) == 0 {
				t.Fatal("role bypassed parent authority")
			}
		})
	}
}

func TestRoleCoordinatorCaptureUsesOwnerBoundsAndLegacyEggOwnership(t *testing.T) {
	h := testssh.New(t)
	t.Setenv("HOME", h.Root)
	t.Setenv("PATH", h.Root+string(os.PathListSeparator)+os.Getenv("PATH"))
	local, _ := scopedRunFixture(t, h.Root, "l")
	remote, _ := scopedRunFixture(t, h.Root, "r")
	for _, f := range []*runWingFixture{local, remote} {
		wc := f.service.Policy().Wing
		wc.Paths = nil
		if err := config.SaveWingConfig(f.server.Cfg.Dir, wc); err != nil {
			t.Fatal(err)
		}
	}
	meta, err := sshcontrol.InspectLocal(t.Context(), remote.server.Cfg.Dir, "")
	if err != nil {
		t.Fatal(err)
	}
	h.Host(t, "peer", meta)
	if err := config.SaveRemotes(local.server.Cfg.Dir, map[string]config.Remote{"peer": {SSHTarget: "peer", WingID: meta.WingID, WingthingDir: meta.WingthingDir, ControlSocket: meta.ControlSocket, ControlVersion: meta.Version}}); err != nil {
		t.Fatal(err)
	}
	remembered := newRememberedWings(t.Context(), local.server.Cfg.Dir, local.server.Cfg.WingID)
	t.Cleanup(func() { remembered.Close() })
	m := newWingMailboxes(t.Context(), "test", "fixture-owner", local.service, NewMCPAdmissionState(), remembered)
	t.Cleanup(func() { m.Close() })
	s, err := resolveLocalWingClient("test", local.service, "fixture-owner", NewMCPAdmissionState(), controlsocket.Hello{})
	if err != nil {
		t.Fatal(err)
	}
	s.parentRole = "coordinator"
	if _, err := s.loadSessionLaunchConfig(h.Root); err != nil {
		t.Fatal(err)
	}
	wings, err := m.capture(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{local.server.Cfg.WingID, meta.WingID} {
		if len(wings[id]) != 1 || wings[id][0] != "/" {
			t.Fatalf("unbounded capture=%v", wings)
		}
	}
	s.enforcePathBounds = true
	s.allowedPaths = nil
	if _, err := m.capture(s); err == nil {
		t.Fatal("enforced empty owner bounds widened")
	}
	s.enforcePathBounds = false
	// A legacy standalone egg belongs to the same authenticated personal owner.
	for _, target := range []struct {
		f         *runWingFixture
		id, owner string
	}{{local, "legacy", ""}, {local, "foreign", "different-owner"}, {remote, "remote-legacy", ""}} {
		dir := filepath.Join(target.f.server.Cfg.Dir, "eggs", target.id)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "egg.pid"), []byte(fmt.Sprint(os.Getpid())), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "egg.meta"), []byte("kind=agent\nagent=claude\ncwd="+filepath.Join(h.Root, "other-project")+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if target.owner != "" {
			if err := eggclient.WriteEggOwner(dir, target.owner, ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	reg := scopedRegistration(t, local, wings, "coordinator", "coordinator")
	client := scopedClient(t, local, reg)
	for _, target := range []struct{ wing, id string }{{local.server.Cfg.WingID, "legacy"}, {meta.WingID, "remote-legacy"}} {
		data := scopedCall(t, client, "terminal_list", map[string]any{"wing_id": target.wing})
		wire, _ := json.Marshal(data)
		if !strings.Contains(string(wire), target.id) || strings.Contains(string(wire), `"id":"foreign"`) {
			t.Fatalf("legacy fleet=%s", wire)
		}
	}
	scopedDenied(t, client, "session_pin", map[string]any{"wing_id": local.server.Cfg.WingID, "session": "foreign", "pinned": true})
}

func TestRoleCoordinatorContinuationKeepsGrantedFleetToolsAndProtection(t *testing.T) {
	s, _, root, source := continuationFixture(t)
	s.Unsandboxed = false
	previous := conversationBrokerProtection
	conversationBrokerProtection = func(*config.Config, *egg.EggConfig, string, string, string, eggclient.EggIdentity, []string) error {
		return nil
	}
	t.Cleanup(func() { conversationBrokerProtection = previous })
	if err := sessionrole.Write(source, sessionrole.Metadata{Role: "coordinator", Pinned: true}); err != nil {
		t.Fatal(err)
	}
	s.captureWings = func(*Server) (map[string][]string, error) { return map[string][]string{s.Cfg.WingID: {root.CWD}}, nil }
	var registration conversationBrokerRegistration
	s.startMailbox = func(reg conversationBrokerRegistration) error { registration = reg; return nil }
	var launched eggclient.SpawnEggOpts
	s.startContinuation = func(_ *store.Conversation, _ *egg.EggConfig, opts eggclient.SpawnEggOpts) error {
		launched = opts
		return nil
	}
	result, err := s.toolAgentStart(continuationRequest("source", "Resume the coordinator", "role-resume"))
	if err != nil {
		t.Fatal(err)
	}
	if result["launch_state"] != "started" || registration.Role != "coordinator" || !registration.Scoped || launched.RoleMetadata.Role != "coordinator" || !launched.RoleMetadata.Pinned {
		t.Fatalf("resume lost role: %v %v %v", result, registration, launched)
	}
	if len(launched.ProtectedWriteTargets) == 0 || !launched.OmitBrowserBridge {
		t.Fatal("resume lost protected parent contract")
	}
	args := strings.Join(launched.AgentArgs, " ")
	for _, tool := range coordinatorParentTools {
		if !strings.Contains(args, "mcp__wingthing__"+tool) {
			t.Fatalf("headless role denied %s: %s", tool, args)
		}
	}
	for _, flag := range []string{"--strict-mcp-config", "--restricted", "--permission-mode dontAsk", "--permission-prompts none", "--max-turns 40"} {
		if !strings.Contains(args, flag) {
			t.Fatalf("resume lost %s", flag)
		}
	}
	// Approval allowlists narrow with the launcher's grants; they never grant MCP authority.
	s.Grants = map[string]bool{"terminal.read": true}
	tools, _, _ := s.brokerToolCeiling()
	limited := strings.Join(headlessCoordinatorArgs(continuationModel, "message", tools), " ")
	if strings.Contains(limited, "mcp__wingthing__agent_run") || strings.Contains(limited, "mcp__wingthing__session_pin") {
		t.Fatal("approval list ignored grant ceiling")
	}
	legacy := strings.Join(headlessCoordinatorArgs(continuationModel, "message"), " ")
	if strings.Contains(legacy, "mcp__wingthing__terminal_list") {
		t.Fatal("ordinary continuation acquired fleet approvals")
	}
}

func TestCoordinatorRoleFleetIncludesExistingLocalAndSSHWork(t *testing.T) {
	h := testssh.New(t)
	t.Setenv("HOME", h.Root)
	t.Setenv("PATH", h.Root+string(os.PathListSeparator)+os.Getenv("PATH"))
	local, _ := scopedRunFixture(t, h.Root, "l")
	remote, _ := scopedRunFixture(t, h.Root, "r")
	for _, f := range []*runWingFixture{local, remote} {
		if _, err := auth.EnsureKeyPair(f.server.Cfg.Dir); err != nil {
			t.Fatal(err)
		}
	}
	meta, err := sshcontrol.InspectLocal(t.Context(), remote.server.Cfg.Dir, "")
	if err != nil {
		t.Fatal(err)
	}
	h.Host(t, "peer", meta)
	if err := config.SaveRemotes(local.server.Cfg.Dir, map[string]config.Remote{"peer": {SSHTarget: "peer", WingID: meta.WingID, WingthingDir: meta.WingthingDir, ControlSocket: meta.ControlSocket, ControlVersion: meta.Version}}); err != nil {
		t.Fatal(err)
	}
	work := h.Root
	// These jobs precede the coordinator and have no parent ledger entry.
	localRun := local.admit(t, "existing local request")
	remoteRun := remote.admit(t, "existing remote request")
	localOpts := <-local.spawned
	remoteOpts := <-remote.spawned
	for _, target := range []struct{ state, session string }{{local.server.Cfg.Dir, localOpts.SessionID}, {remote.server.Cfg.Dir, remoteOpts.SessionID}} {
		if err := os.WriteFile(filepath.Join(target.state, "eggs", target.session, "egg.pid"), []byte(fmt.Sprint(os.Getpid())), 0600); err != nil {
			t.Fatal(err)
		}
	}
	reg := scopedRegistration(t, local, map[string][]string{local.server.Cfg.WingID: {work}, meta.WingID: {work}}, "coordinator", "coordinator")
	client := scopedClient(t, local, reg)
	for _, target := range []struct{ wing, run, session string }{{local.server.Cfg.WingID, localRun, localOpts.SessionID}, {meta.WingID, remoteRun, remoteOpts.SessionID}} {
		data := scopedCall(t, client, "agent_list", map[string]any{"wing_id": target.wing})
		wire, _ := json.Marshal(data)
		if !strings.Contains(string(wire), target.run) {
			t.Fatalf("fleet run absent: %s", wire)
		}
		data = scopedCall(t, client, "terminal_list", map[string]any{"wing_id": target.wing})
		wire, _ = json.Marshal(data)
		if !strings.Contains(string(wire), target.session) {
			t.Fatalf("fleet session absent: %s", wire)
		}
		scopedCall(t, client, "session_read", map[string]any{"wing_id": target.wing, "session": target.session})
		scopedCall(t, client, "agent_status", map[string]any{"wing_id": target.wing, "run_id": target.run})
		scopedCall(t, client, "session_pin", map[string]any{"wing_id": target.wing, "session": target.session, "pinned": true})
		// Native readiness is deliberately absent in this inventory fixture.
		// A coordinator message reaches that guard instead of the child-tree guard.
		wire, _ = json.Marshal(map[string]any{"wing_id": target.wing, "session": target.session, "request_id": "message", "input": "hello", "timeout_seconds": 0.1})
		data, denied, err := client.Call(t.Context(), "session_prompt", wire)
		if err == nil && !denied {
			t.Fatal("missing native readiness accepted a message")
		}
		if strings.Contains(fmt.Sprint(data), "recorded children") || strings.Contains(fmt.Sprint(data), "bound task tree") {
			t.Fatalf("message still child-bound: %v %v", data, err)
		}
	}
	// Ordinary parents retain their narrower scope even after a coordinator exists.
	ordinary := scopedRegistration(t, local, reg.Wings, "ordinary")
	scopedDenied(t, scopedClient(t, local, ordinary), "agent_status", map[string]any{"wing_id": meta.WingID, "run_id": remoteRun})
	scopedDenied(t, client, "agent_list", map[string]any{"wing_id": "ungranted"})
	// Reconnecting rereads protected registration; it keeps the exact role ceiling.
	client.Close()
	client = scopedClient(t, local, reg)
	scopedCall(t, client, "agent_status", map[string]any{"wing_id": meta.WingID, "run_id": remoteRun})
	scopedDenied(t, client, "terminal_send", map[string]any{"wing_id": meta.WingID, "session": remoteOpts.SessionID, "input": "raw command"})
}
