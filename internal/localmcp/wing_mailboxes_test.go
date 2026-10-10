package localmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/sshcontrol"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/testssh"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
	"gopkg.in/yaml.v3"
)

func scopedRunFixture(t *testing.T, root, name string) (*runWingFixture, *controlsocket.Server) {
	t.Helper()
	state := filepath.Join(root, name)
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Dir: state, WingID: "wing-" + name, DefaultAgent: "codex"}
	wc := &config.WingConfig{WingID: cfg.WingID, Paths: config.PathList{{Path: root}}}
	if err := config.SaveWingConfig(state, wc); err != nil {
		t.Fatal(err)
	}
	service := &wingsession.Service{Config: cfg, Home: root, Policy: func() wingsession.Policy { return wingsession.Policy{Wing: wc, Egg: egg.DefaultEggConfig()} }, Register: func(string) error { return nil }}
	s, err := resolveLocalWingClient("test", service, "fixture-owner", NewMCPAdmissionState(), controlsocket.Hello{})
	if err != nil {
		t.Fatal(err)
	}
	f := newRunWingFixture(t, s)
	listener, err := ListenLocalWingControl(t.Context(), "test", service, "fixture-owner", NewMCPAdmissionState())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return f, listener
}

func scopedRegistration(t *testing.T, f *runWingFixture, wings map[string][]string, session string) conversationBrokerRegistration {
	t.Helper()
	db, err := store.Open(f.server.Cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	workspace := wingpolicy.CanonicalPolicyPath(f.service.Home)
	c, _, err := db.ReserveConversation(store.Conversation{ID: session, OwnerID: f.server.clientPrincipal(), Agent: "claude", CWD: workspace, WingID: f.server.Cfg.WingID, SessionID: session + "-exec", LaunchKey: session, SpecDigest: session})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := brokerChildPolicySnapshot(egg.DefaultEggConfig(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	reg := conversationBrokerRegistration{Version: 1, StateDir: wingpolicy.CanonicalPolicyPath(f.server.Cfg.Dir), ConversationID: c.ID, RootID: c.RootID, SessionID: c.SessionID, Principal: f.server.clientPrincipal(), LauncherActor: "default", LauncherSurface: string(control.SurfaceLocalMCP), UserID: "fixture-owner", Tools: scopedParentTools, MaxSessions: 8, MaxSpawnsPerHour: 60, AllowedPaths: []string{workspace}, EnforcePathBounds: true, Workspace: workspace, Mailbox: filepath.Join(".wingthing-conversations", c.ID, c.SessionID, "mailbox"), EggConfig: policy, Executable: "/fixture/wt", RegisteredAt: time.Now().Unix(), Scoped: true, Wings: wings}
	if err := writeConversationBrokerRegistration(f.server.Cfg, reg); err != nil {
		t.Fatal(err)
	}
	return reg
}

func scopedClient(t *testing.T, f *runWingFixture, reg conversationBrokerRegistration) *controlsocket.Client {
	t.Helper()
	c, err := controlsocket.Dial(t.Context(), f.server.Cfg.Dir, controlsocket.Hello{Conversation: reg.ConversationID, Execution: reg.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}
func scopedCall(t *testing.T, c *controlsocket.Client, tool string, args map[string]any) map[string]any {
	t.Helper()
	wire, _ := json.Marshal(args)
	data, denied, err := c.Call(t.Context(), tool, wire)
	if err != nil || denied {
		t.Fatalf("%s: %v %v", tool, data, err)
	}
	return data
}
func scopedDenied(t *testing.T, c *controlsocket.Client, tool string, args map[string]any) {
	t.Helper()
	wire, _ := json.Marshal(args)
	data, denied, err := c.Call(t.Context(), tool, wire)
	if err == nil && !denied {
		t.Fatalf("%s unexpectedly granted: %v", tool, data)
	}
}

func TestScopedParentLocalAndSSHChildrenSurviveReconnect(t *testing.T) {
	h := testssh.New(t)
	t.Setenv("HOME", h.Root)
	t.Setenv("PATH", h.Root+string(os.PathListSeparator)+os.Getenv("PATH"))
	local, _ := scopedRunFixture(t, h.Root, "l")
	remote, _ := scopedRunFixture(t, h.Root, "r")
	meta, err := sshcontrol.InspectLocal(t.Context(), remote.server.Cfg.Dir, "")
	if err != nil {
		t.Fatal(err)
	}
	h.Host(t, "forge", meta)
	if err := config.SaveRemotes(local.server.Cfg.Dir, map[string]config.Remote{"forge": {SSHTarget: "forge", WingID: meta.WingID, WingthingDir: meta.WingthingDir, ControlSocket: meta.ControlSocket, ControlVersion: meta.Version}}); err != nil {
		t.Fatal(err)
	}
	work := wingpolicy.CanonicalPolicyPath(h.Root)
	reg := scopedRegistration(t, local, map[string][]string{local.server.Cfg.WingID: {work}, remote.server.Cfg.WingID: {work}}, "parent")
	client := scopedClient(t, local, reg)
	data := scopedCall(t, client, "wing_list", map[string]any{})
	if data["count"] != float64(2) {
		t.Fatalf("directory: %v", data)
	}
	localReceipt := scopedCall(t, client, "agent_run", map[string]any{"wing_id": local.server.Cfg.WingID, "agent": "codex", "cwd": work, "prompt": "local request", "idempotency_key": "local"})
	remoteReceipt := scopedCall(t, client, "agent_run", map[string]any{"wing_id": remote.server.Cfg.WingID, "agent": "codex", "cwd": work, "prompt": "remote request", "idempotency_key": "remote"})
	localID, remoteID := localReceipt["run_id"].(string), remoteReceipt["run_id"].(string)
	if localReceipt["wing_id"] != local.server.Cfg.WingID || remoteReceipt["wing_id"] != remote.server.Cfg.WingID {
		t.Fatal("receipt lost owning wing")
	}
	if <-local.submitted != localID || <-remote.submitted != remoteID {
		t.Fatal("wrong provider turn")
	}
	for _, target := range []struct {
		wing, prompt, key, run string
		receipt                map[string]any
	}{{local.server.Cfg.WingID, "local request", "local", localID, localReceipt}, {remote.server.Cfg.WingID, "remote request", "remote", remoteID, remoteReceipt}} {
		if target.receipt["idempotency_key"] != target.key {
			t.Fatalf("receipt exposed an internal admission namespace: %v", target.receipt)
		}
		retry := scopedCall(t, client, "agent_run", map[string]any{"wing_id": target.wing, "agent": "codex", "cwd": work, "prompt": target.prompt, "idempotency_key": target.receipt["idempotency_key"]})
		if retry["run_id"] != target.run || retry["session_id"] != target.receipt["session_id"] {
			t.Fatalf("retry created another child: %v", retry)
		}
	}
	// Both the directory pool and the separately scoped remote pool are owned
	// by the wing. Drop both forwards at an acknowledged admission barrier.
	for range 2 {
		forward := <-h.Started
		forward.Drop()
	}
	_ = client.Close()
	local.finish(t, localID, "result "+localID, "")
	remote.finish(t, remoteID, "result "+remoteID, "")
	client = scopedClient(t, local, reg)
	for _, target := range []struct{ wing, id string }{{local.server.Cfg.WingID, localID}, {remote.server.Cfg.WingID, remoteID}} {
		scopedCall(t, client, "agent_wait", map[string]any{"wing_id": target.wing, "run_id": target.id})
		result := scopedCall(t, client, "agent_result", map[string]any{"wing_id": target.wing, "run_id": target.id})
		if result["run_id"] != target.id || result["ready"] != true || !strings.Contains(result["output"].(string), target.id) {
			t.Fatalf("lost durable child: %v", result)
		}
	}
	children, err := mailboxChildren(local.server.Cfg, &reg, nil)
	if err != nil || children[childKey(meta.WingID, remoteID)].WingID != meta.WingID {
		t.Fatalf("child ownership ledger: %v %v", children, err)
	}
	// A different parent under the same principal cannot claim these children.
	other := scopedRegistration(t, local, reg.Wings, "other-parent")
	scopedDenied(t, scopedClient(t, local, other), "agent_result", map[string]any{"wing_id": meta.WingID, "run_id": remoteID})
	scopedDenied(t, client, "agent_result", map[string]any{"wing_id": local.server.Cfg.WingID, "run_id": remoteID})
	scopedDenied(t, client, "agent_run", map[string]any{"wing_id": "ungranted", "agent": "codex", "cwd": work, "prompt": "denied", "idempotency_key": "denied"})
	scopedDenied(t, client, "agent_run", map[string]any{"wing_id": meta.WingID, "agent": "codex", "cwd": work, "prompt": "overlong key", "idempotency_key": strings.Repeat("x", 201)})
	scopedDenied(t, client, "agent_start", map[string]any{"wing_id": meta.WingID, "agent": "claude", "cwd": work, "request_id": "nested-parent", "conversation_role": "parent", "scoped_mcp": true})
	// Queued steer is also an admission. Its echoed key survives another call
	// and records the follow-up's actual owning wing without spawning twice.
	followup := scopedCall(t, client, "agent_steer", map[string]any{"wing_id": meta.WingID, "run_id": remoteID, "prompt": "new direction", "idempotency_key": "steer"})
	steerID := followup["run_id"].(string)
	if followup["idempotency_key"] != "steer" || <-remote.submitted != steerID {
		t.Fatalf("steer receipt: %v", followup)
	}
	retry := scopedCall(t, client, "agent_steer", map[string]any{"wing_id": meta.WingID, "run_id": remoteID, "prompt": "new direction", "idempotency_key": followup["idempotency_key"]})
	if retry["run_id"] != steerID {
		t.Fatalf("steer retry created another child: %v", retry)
	}
	remote.finish(t, steerID, "followup result", "")
	scopedCall(t, client, "agent_wait", map[string]any{"wing_id": meta.WingID, "run_id": steerID})
	for _, f := range []*runWingFixture{local, remote} {
		select {
		case id := <-f.submitted:
			t.Fatalf("unexpected duplicate run %s", id)
		default:
		}
	}
	// Parent exit changes no run lifetime. A fresh owner client reads the same
	// IDs through the ordinary aggregate API, without a mailbox binding.
	_ = client.Close()
	fresh, err := controlsocket.Dial(t.Context(), local.server.Cfg.Dir, controlsocket.Hello{Aggregate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	for _, target := range []struct{ wing, id string }{{local.server.Cfg.WingID, localID}, {meta.WingID, remoteID}} {
		scopedCall(t, fresh, "agent_result", map[string]any{"wing_id": target.wing, "run_id": target.id})
	}
}

func TestScopedParentRevocationAndReceivingWingPathCeiling(t *testing.T) {
	h := testssh.New(t)
	t.Setenv("HOME", h.Root)
	t.Setenv("PATH", h.Root+string(os.PathListSeparator)+os.Getenv("PATH"))
	local, _ := scopedRunFixture(t, h.Root, "l")
	remote, _ := scopedRunFixture(t, h.Root, "r")
	work := wingpolicy.CanonicalPolicyPath(h.Root)
	narrow := filepath.Join(work, "allowed")
	outside := filepath.Join(work, "outside")
	for _, dir := range []string{narrow, outside} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	meta, err := sshcontrol.InspectLocal(t.Context(), remote.server.Cfg.Dir, "")
	if err != nil {
		t.Fatal(err)
	}
	h.Host(t, "forge", meta)
	if err := config.SaveRemotes(local.server.Cfg.Dir, map[string]config.Remote{"forge": {SSHTarget: "forge", WingID: meta.WingID, WingthingDir: meta.WingthingDir, ControlSocket: meta.ControlSocket, ControlVersion: meta.Version}}); err != nil {
		t.Fatal(err)
	}
	reg := scopedRegistration(t, local, map[string][]string{local.server.Cfg.WingID: {work}, meta.WingID: {narrow}}, "parent")
	client := scopedClient(t, local, reg)
	// The lexical path is inside the grant, but its actual remote destination
	// escapes. The receiving wing checks canonical cwd before spawning.
	alias := filepath.Join(narrow, "escape")
	if err := os.Symlink(outside, alias); err != nil {
		t.Fatal(err)
	}
	ceiling, _ := json.Marshal(wingCallScope{Paths: []string{narrow}, Tools: []string{"agent_run", "agent_result"}, MaxSessions: 8, MaxSpawnsPerHour: 60})
	receiving, err := controlsocket.Dial(t.Context(), remote.server.Cfg.Dir, controlsocket.Hello{Scope: string(ceiling)})
	if err != nil {
		t.Fatal(err)
	}
	defer receiving.Close()
	scopedDenied(t, receiving, "agent_run", map[string]any{"agent": "codex", "cwd": alias, "prompt": "receiving-wing escape", "idempotency_key": "receiving-escape"})
	scopedDenied(t, client, "agent_run", map[string]any{"wing_id": meta.WingID, "agent": "codex", "cwd": alias, "prompt": "must not launch", "idempotency_key": "escape"})
	select {
	case <-remote.spawned:
		t.Fatal("path escape spawned an egg")
	default:
	}
	configData := localMCPClientsConfig{Clients: map[string]localMCPClientConfig{"default": {Grants: []string{"wing.read", "capabilities.read", "agent.run", "agent.read", "agent.stop"}, Wings: map[string][]string{meta.WingID: {narrow}}}}}
	writeClients := func() {
		data, err := yaml.Marshal(configData)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(local.server.Cfg.Dir, "clients.yaml"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeClients()
	configData.Clients["default"] = localMCPClientConfig{Grants: []string{"wing.read", "agent.read"}}
	writeClients()
	scopedDenied(t, client, "agent_run", map[string]any{"wing_id": meta.WingID, "agent": "codex", "cwd": narrow, "prompt": "revoked", "idempotency_key": "revoked"})
	scopedDenied(t, client, "wingthing_capabilities", map[string]any{})
	data := scopedCall(t, client, "wing_list", map[string]any{})
	if data["count"] != float64(1) {
		t.Fatalf("revoked remote remained authorized: %v", data)
	}
	// Restoring a larger current grant never widens the original ceiling.
	configData.Clients["default"] = localMCPClientConfig{Grants: []string{"wing.read", "capabilities.read", "agent.run", "agent.read"}, Wings: map[string][]string{meta.WingID: {work}}}
	writeClients()
	scopedDenied(t, client, "agent_run", map[string]any{"wing_id": meta.WingID, "agent": "codex", "cwd": outside, "prompt": "widened", "idempotency_key": "widened"})
}

func TestScopedParentUnknownRemoteAdmissionPreservesCallerKey(t *testing.T) {
	h := testssh.New(t)
	t.Setenv("HOME", h.Root)
	t.Setenv("PATH", h.Root+string(os.PathListSeparator)+os.Getenv("PATH"))
	local, _ := scopedRunFixture(t, h.Root, "l")
	state, work := filepath.Join(h.Root, "r"), wingpolicy.CanonicalPolicyPath(h.Root)
	if err := config.SaveWingConfig(state, &config.WingConfig{WingID: "wing-r"}); err != nil {
		t.Fatal(err)
	}
	admitted := make(chan string, 2)
	var mu sync.Mutex
	key := ""
	remote, err := controlsocket.Listen(t.Context(), state, "wing-r", func(controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
		return controlsocket.Welcome{}, func(ctx context.Context, r control.DirectRequest) control.DirectResponse {
			var args struct {
				Key string `json:"idempotency_key"`
			}
			_ = json.Unmarshal(r.Arguments, &args)
			mu.Lock()
			first := key == ""
			if first {
				key = args.Key
			}
			same := key == args.Key
			mu.Unlock()
			admitted <- args.Key
			if first {
				<-ctx.Done()
			}
			if !same {
				return control.DirectResponse{Version: control.ContractVersion, ID: r.ID, Error: "retry changed the admitted key"}
			}
			return control.DirectResponse{Version: control.ContractVersion, ID: r.ID, Result: map[string]any{"run_id": "original-run", "session_id": "original-session", "cwd": work}}
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = remote.Close() })
	meta, err := sshcontrol.InspectLocal(t.Context(), state, "")
	if err != nil {
		t.Fatal(err)
	}
	h.Host(t, "forge", meta)
	if err := config.SaveRemotes(local.server.Cfg.Dir, map[string]config.Remote{"forge": {SSHTarget: "forge", WingID: meta.WingID, WingthingDir: meta.WingthingDir, ControlSocket: meta.ControlSocket, ControlVersion: meta.Version}}); err != nil {
		t.Fatal(err)
	}
	reg := scopedRegistration(t, local, map[string][]string{local.server.Cfg.WingID: {work}, meta.WingID: {work}}, "parent")
	client := scopedClient(t, local, reg)
	args := map[string]any{"wing_id": meta.WingID, "agent": "codex", "cwd": work, "prompt": "request", "idempotency_key": "original-key"}
	result := make(chan map[string]any, 1)
	go func() {
		wire, _ := json.Marshal(args)
		data, denied, err := client.Call(t.Context(), "agent_run", wire)
		if err != nil || !denied {
			result <- map[string]any{"unexpected": err}
			return
		}
		result <- data
	}()
	forward := <-h.Started
	namespaced := <-admitted
	h.Offline(t, "forge", true)
	forward.Drop()
	data := <-result
	if data["outcome"] != "unknown" || data["wing_id"] != meta.WingID || data["idempotency_key"] != "original-key" || strings.Contains(fmt.Sprint(data["error"]), namespaced) {
		t.Fatalf("ambiguous admission lost the caller's retry key: %v", data)
	}
	h.Offline(t, "forge", false)
	args["idempotency_key"] = data["idempotency_key"]
	recovered := scopedCall(t, client, "agent_run", args)
	if recovered["run_id"] != "original-run" || recovered["session_id"] != "original-session" || <-admitted != namespaced {
		t.Fatalf("ambiguous admission retry changed child identity: %v", recovered)
	}
}
