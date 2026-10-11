package localmcp

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/store"
	webrtcpkg "github.com/ehrlich-b/wingthing/internal/webrtc"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
)

func committedInventoryServer(t *testing.T, wingID string, seed func(*store.Store, *config.Config)) (*Server, *config.WingConfig) {
	t.Helper()
	cfg := &config.Config{Dir: t.TempDir(), WingID: wingID}
	if _, err := auth.EnsureKeyPair(cfg.Dir); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	seed(db, cfg)
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	wc := &config.WingConfig{WingID: wingID}
	service := &wingsession.Service{Config: cfg, Home: cfg.Dir, Policy: func() wingsession.Policy { return wingsession.Policy{Wing: wc, Egg: egg.DefaultEggConfig()} }}
	service.Spawn = func(*wingsession.Launch, wingsession.StartOptions) (*egg.Client, error) {
		t.Error("inventory/terminal recovery launched an egg")
		return nil, os.ErrPermission
	}
	if err = service.StartRuns(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.RunManager.Close() })
	return &Server{Version: "fixture", Cfg: cfg, Sessions: service, Principal: wingsession.UserPrincipal("wing-user"), Logs: io.Discard, identity: eggclient.EggIdentity{UserID: "wing-user"}}, wc
}

func commitInventoryRun(t *testing.T, db *store.Store, id, principal, user, cwd, status, output string, at time.Time, task *wingsession.MCPTaskRecord) {
	t.Helper()
	run := wingsession.Run{ID: id, SessionID: "session-" + id, Agent: "codex", CWD: cwd, CreatedAt: at, Phase: "terminal", Launch: wingsession.RunLaunch{Authority: wingsession.Authority{Principal: principal, UserID: user}}, Result: egg.RunTurnResult{RunID: id, SessionID: "session-" + id, Status: status, Text: output, EndedAt: at}, MCPTask: task}
	if status == "failed" {
		run.Result.FailureKind = agent.UnknownOutcome
	}
	record, _ := json.Marshal(run)
	projection := &store.Task{ID: id, Principal: principal, CWD: cwd, Agent: "codex", Status: status, RunAt: at, CreatedAt: at, FinishedAt: &at, Output: &output}
	row, _, err := db.AdmitAgentRun(&store.AgentRun{ID: id, SessionID: run.SessionID, Principal: principal, SpecHash: id, Record: record}, projection)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.SaveAgentRun(row, projection, "", ""); err != nil {
		t.Fatal(err)
	}
}

func TestRunInventoryTwoWingsIdenticalIDsRespectLiveOwnerAndPathPolicies(t *testing.T) {
	keys := map[string]bool{}
	var foreignCursor string
	for _, wingID := range []string{"wing-one", "wing-two"} {
		s, wc := committedInventoryServer(t, wingID, func(db *store.Store, cfg *config.Config) {
			allowed := filepath.Join(cfg.Dir, "allowed")
			if err := os.MkdirAll(allowed, 0700); err != nil {
				t.Fatal(err)
			}
			principal := wingsession.UserPrincipal("wing-user")
			at := time.Now().UTC()
			commitInventoryRun(t, db, "same-id", principal, "wing-user", allowed, "done", "owned result", at, nil)
			commitInventoryRun(t, db, "next-owned", principal, "wing-user", allowed, "done", "next result", at, nil)
			commitInventoryRun(t, db, "0-foreign-owner", "other", "wing-user", allowed, "done", "SECRET OWNER", at, nil)
			commitInventoryRun(t, db, "0-foreign-user", principal, "other-user", allowed, "done", "SECRET USER", at, nil)
			commitInventoryRun(t, db, "0-foreign-cwd", principal, "wing-user", filepath.Join(cfg.Dir, "outside"), "done", "SECRET PATH", at, nil)
		})
		path := filepath.Join(s.Cfg.Dir, "allowed")
		wc.Paths = config.PathList{{Path: path, Members: []string{"member@example.test"}}}
		wc.DirectMCP = &config.DirectMCPConfig{AllowGrants: []string{"agent.read"}}
		identity := webrtcpkg.PeerIdentity{UserID: "wing-user", Email: "member@example.test", OrgRole: "member"}
		policy, err := resolveDirectMCPPolicy(wc, s.Cfg.Dir, false, identity)
		if err != nil {
			t.Fatal(err)
		}
		direct := &Server{Version: s.Version, Cfg: s.Cfg, Logs: s.Logs, Sessions: s.Sessions, Principal: s.Principal,
			Surface: control.SurfaceDirectMCP, Grants: policy.grants, allowedPaths: policy.allowedPaths,
			enforcePathBounds: policy.enforcePathBounds, identity: policy.identity, sessionRole: policy.role}
		data, denied, err := serializedInventoryCall(t, direct, "agent_list", json.RawMessage(`{"limit":1}`))
		if err != nil || denied || data["count"] != float64(1) {
			t.Fatalf("restrictive first page: %v %v", data, err)
		}
		var first struct {
			Runs []struct {
				ID   string `json:"run_id"`
				Wing string `json:"wing_id"`
			}
			Cursor string `json:"next_cursor"`
		}
		wire, _ := json.Marshal(data)
		_ = json.Unmarshal(wire, &first)
		if first.Runs[0].ID != "next-owned" || first.Cursor == "" {
			t.Fatalf("hidden rows occupied page: %v", data)
		}
		if foreignCursor != "" {
			args, _ := json.Marshal(map[string]any{"cursor": foreignCursor})
			_, denied, err = serializedInventoryCall(t, direct, "agent_list", args)
			if !denied || err == nil {
				t.Fatal("other wing's cursor accepted")
			}
		}
		foreignCursor = first.Cursor
		args, _ := json.Marshal(map[string]any{"cursor": first.Cursor, "limit": 1})
		data, denied, err = serializedInventoryCall(t, direct, "agent_list", args)
		if err != nil || denied {
			t.Fatalf("restrictive continuation: %v %v", data, err)
		}
		var second struct {
			Runs []struct {
				ID   string `json:"run_id"`
				Wing string `json:"wing_id"`
			}
		}
		wire, _ = json.Marshal(data)
		_ = json.Unmarshal(wire, &second)
		if len(second.Runs) != 1 || second.Runs[0].ID != "same-id" || second.Runs[0].Wing != wingID {
			t.Fatalf("identity collision: %v", data)
		}
		keys[second.Runs[0].Wing+"/"+second.Runs[0].ID] = true
		// The receiving socket scope and client-owner mapping reproduce the same
		// restriction without granting inventory to a legacy scoped mailbox.
		if err = os.WriteFile(filepath.Join(s.Cfg.Dir, "clients.yaml"), []byte("clients:\n  reader:\n    owner: default\n    grants: [agent.read]\n"), 0600); err != nil {
			t.Fatal(err)
		}
		scope, _ := json.Marshal(wingCallScope{Paths: []string{path}, Tools: []string{"agent_list"}, MaxSessions: 1, MaxSpawnsPerHour: 1})
		local, err := resolveLocalWingClient("fixture", s.Sessions, "wing-user", NewMCPAdmissionState(), controlsocket.Hello{Client: "reader", Scope: string(scope)})
		if err != nil {
			t.Fatal(err)
		}
		data, denied, err = serializedInventoryCall(t, local, "agent_list", json.RawMessage(`{}`))
		if err != nil || denied || data["count"] != float64(2) {
			t.Fatalf("receiving scope widened/narrowed: %v %v", data, err)
		}
		wc.DirectMCP.DenyGrants = []string{"agent.read"}
		revoked, err := resolveDirectMCPPolicy(wc, s.Cfg.Dir, false, identity)
		if err != nil {
			t.Fatal(err)
		}
		direct.Grants = revoked.grants
		_, denied, _ = serializedInventoryCall(t, direct, "agent_list", json.RawMessage(`{}`))
		if !denied {
			t.Fatal("revoked read grant retained")
		}
	}
	if len(keys) != 2 {
		t.Fatalf("same local ID collapsed wings: %v", keys)
	}
	t.Logf("two-wing oracle: %v; foreign owner/user/cwd hidden before 1-row pages, cross-wing cursor and revoked grant denied", keys)
}
