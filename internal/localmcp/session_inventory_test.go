package localmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/fleet"
	"github.com/ehrlich-b/wingthing/internal/sessionrole"
)

func inventoryFixture(t *testing.T, id, label string) *Server {
	t.Helper()
	cfg := &config.Config{Dir: t.TempDir(), WingID: id}
	dir := filepath.Join(cfg.Dir, "eggs", "same-short-id")
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	for file, value := range map[string]string{"egg.meta": "agent=codex\ncwd=/personal/repo\nprovider_home=" + cfg.Dir + "\n", "session.name": "DNS-helper", "lifecycle.jsonl": `{"sequence":1,"source":"codex_transcript","type":"message","text":"fixed DNS helper routing"}` + "\n"} {
		if e := os.WriteFile(filepath.Join(dir, file), []byte(value), 0600); e != nil {
			t.Fatal(e)
		}
	}
	s := testWingServer(t, &Server{Cfg: cfg, Logs: &bytes.Buffer{}, Version: "fixture"})
	s.Sessions.Policy().Wing.WingID = id
	s.Sessions.Policy().Wing.Label = label
	return s
}
func asInventory(t *testing.T, m map[string]any) fleet.Inventory {
	t.Helper()
	data, _ := json.Marshal(m)
	var inv fleet.Inventory
	if e := json.Unmarshal(data, &inv); e != nil {
		t.Fatal(e)
	}
	return inv
}
func TestSessionInventoryTwoMachinesSameIDAndVerifiedRemoteSearch(t *testing.T) {
	mac := inventoryFixture(t, "mac-wing", "EhrlichMac")
	forge := inventoryFixture(t, "forge-wing", "forge")
	if e := sessionrole.Write(filepath.Join(forge.Cfg.Dir, "eggs", "same-short-id"), sessionrole.Metadata{Role: "coordinator", Pinned: true}); e != nil {
		t.Fatal(e)
	}
	if e := config.SaveRemotes(mac.Cfg.Dir, map[string]config.Remote{"forge": {SSHTarget: "fixture-host", WingID: "forge-wing", WingthingDir: "/fixture/state", ControlSocket: "/fixture/state/control.sock", ControlVersion: control.ContractVersion}}); e != nil {
		t.Fatal(e)
	}
	calls := 0
	mac.remoteToolCall = func(ctx context.Context, name, op string, args []byte) (map[string]any, error) {
		calls++
		if name != "forge" || op != "session_inventory" || strings.Contains(string(args), `"include_remotes":true`) {
			t.Fatalf("recursive/wrong remote call: %s %s %s", name, op, args)
		}
		return forge.ToolSessionInventory(ctx, args)
	}
	gateway, e := mac.ToolSessionInventory(t.Context(), json.RawMessage(`{"include_remotes":true,"query":"DNS-helper"}`))
	if e != nil {
		t.Fatal(e)
	}
	direct, e := forge.ToolSessionInventory(t.Context(), json.RawMessage(`{"query":"DNS-helper"}`))
	if e != nil {
		t.Fatal(e)
	}
	inv := fleet.Merge([]fleet.Inventory{asInventory(t, gateway), asInventory(t, direct)})
	if calls != 1 || !inv.Complete || len(inv.Sessions) != 2 || inv.Sessions[0].OriginWingID != "forge-wing" || len(inv.Sessions[0].Routes) != 2 {
		t.Fatalf("merge=%+v calls=%d", inv, calls)
	}
	if _, e := fleet.Resolve(inv.Sessions, fleet.Identity{SessionID: "same-short-id"}); e == nil {
		t.Fatal("unqualified collision acted")
	}
	inv.Sessions[0].Routes[0].Healthy = false
	route, e := fleet.SelectRoute(inv.Sessions[0], time.Now())
	if e != nil || route.Kind != "via" {
		t.Fatalf("direct-down fallback: %+v %v", route, e)
	}
}
func TestSessionInventoryStrictSchemaPrivacyAndRemoteFailureCoverage(t *testing.T) {
	s := inventoryFixture(t, "mac-wing", "EhrlichMac")
	for _, name := range []string{"session_inventory", "session_action", "coordinator_heartbeat"} {
		tool, ok := control.Lookup(name)
		if !ok || tool.InputSchema["additionalProperties"] != false {
			t.Fatalf("open schema %s", name)
		}
	}
	for _, raw := range []string{`{"query":"x","extra":true}`, `{"include_remotes":"yes"}`, `null`} {
		if _, e := s.ToolSessionInventory(t.Context(), json.RawMessage(raw)); e == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if e := eggclient.WriteEggOwner(filepath.Join(s.Cfg.Dir, "eggs", "same-short-id"), "foreign", ""); e != nil {
		t.Fatal(e)
	}
	inv, e := s.ToolSessionInventory(t.Context(), json.RawMessage(`{"query":"DNS"}`))
	if e != nil || len(asInventory(t, inv).Sessions) != 0 {
		t.Fatal("foreign content leaked", e)
	}
	_ = config.SaveRemotes(s.Cfg.Dir, map[string]config.Remote{"forge": {SSHTarget: "fixture-host", WingID: "forge-wing", WingthingDir: "/fixture/state", ControlSocket: "/fixture/control.sock", ControlVersion: control.ContractVersion}})
	s.remoteToolCall = func(context.Context, string, string, []byte) (map[string]any, error) {
		return nil, errors.New("fixture remote unreachable")
	}
	inv, e = s.ToolSessionInventory(t.Context(), json.RawMessage(`{"include_remotes":true}`))
	if e != nil {
		t.Fatal(e)
	}
	parsed := asInventory(t, inv)
	if parsed.Complete || len(parsed.Sources) != 2 || parsed.Sources[1].Healthy {
		t.Fatalf("outage hidden: %+v", parsed)
	}
	for _, bounded := range []bool{true, false} {
		s.enforcePathBounds = bounded
		s.BoundConversation = ""
		if !bounded {
			s.BoundConversation = "bound"
		}
		if _, e = s.ToolSessionInventory(t.Context(), json.RawMessage(`{"include_remotes":true}`)); e == nil {
			t.Fatal("restricted fanout")
		}
	}
}
func TestQualifiedSessionActionExactTargetAndNoReplay(t *testing.T) {
	mac := inventoryFixture(t, "mac-wing", "EhrlichMac")
	forge := inventoryFixture(t, "forge-wing", "forge")
	_ = config.SaveRemotes(mac.Cfg.Dir, map[string]config.Remote{"forge": {SSHTarget: "fixture-host", WingID: "forge-wing", WingthingDir: "/fixture/state", ControlSocket: "/fixture/control.sock", ControlVersion: control.ContractVersion}})
	for _, server := range []*Server{mac, forge} {
		if e := os.WriteFile(filepath.Join(server.Cfg.Dir, "eggs", "same-short-id", "egg.pid"), []byte(strconv.Itoa(os.Getpid())), 0600); e != nil {
			t.Fatal(e)
		}
	}
	calls := 0
	mac.remoteToolCall = func(ctx context.Context, name, op string, args []byte) (map[string]any, error) {
		calls++
		return forge.ToolSessionAction(ctx, args)
	}
	raw := json.RawMessage(`{"origin_wing_id":"forge-wing","session_id":"same-short-id","operation":"terminal_rename","arguments":{"name":"remote-only"}}`)
	result, e := mac.ToolSessionAction(t.Context(), raw)
	if e != nil {
		t.Fatal(e)
	}
	// Optional arguments survive forwarding without becoming JSON null.
	if _, err := mac.ToolSessionAction(t.Context(), json.RawMessage(`{"origin_wing_id":"forge-wing","session_id":"same-short-id","operation":"session_read"}`)); err != nil {
		t.Fatal(err)
	}
	calls--
	data, _ := json.Marshal(result)
	if calls != 1 || !strings.Contains(string(data), `"kind":"via"`) {
		t.Fatalf("route receipt %s", data)
	}
	if eggclient.ReadSessionName(filepath.Join(mac.Cfg.Dir, "eggs", "same-short-id")) == "remote-only" || eggclient.ReadSessionName(filepath.Join(forge.Cfg.Dir, "eggs", "same-short-id")) != "remote-only" {
		t.Fatal("action crossed origin")
	}
	for _, raw := range []string{`{"origin_wing_id":"mac-wing","session_id":"same","operation":"terminal_rename","arguments":{"name":"never"}}`, `{"session_id":"same-short-id","operation":"session_read"}`, `{"origin_wing_id":"mac-wing","session_id":"same-short-id","operation":"terminal_rename","arguments":{"session":"other","name":"never"}}`, `{"origin_wing_id":"mac-wing","session_id":"same-short-id","operation":"session_read","extra":1}`} {
		if _, e := mac.ToolSessionAction(t.Context(), json.RawMessage(raw)); e == nil {
			t.Fatalf("unsafe selector accepted %s", raw)
		}
	}
	mac.remoteToolCall = func(context.Context, string, string, []byte) (map[string]any, error) {
		calls++
		return nil, errors.New("lost acknowledgement")
	}
	calls = 0
	if _, e = mac.ToolSessionAction(t.Context(), raw); e == nil || calls != 1 {
		t.Fatalf("unknown mutation replayed: %v %d", e, calls)
	}
	mac.Grants = map[string]bool{"terminal.read": true}
	calls = 0
	if _, e = mac.ToolSessionAction(t.Context(), raw); e == nil || calls != 0 {
		t.Fatal("read-only grant dispatched rename")
	}
}
func TestCoordinatorHeartbeatUsesRoleAndPreservesWakeTimes(t *testing.T) {
	s := inventoryFixture(t, "origin", "machine")
	dir := filepath.Join(s.Cfg.Dir, "eggs", "same-short-id")
	raw := json.RawMessage(`{"session":"same-short-id","state":"asleep","last_wake":"2026-10-12T20:00:00Z","next_wake":"2026-10-13T06:00:00Z"}`)
	if _, e := s.ToolCoordinatorHeartbeat(t.Context(), raw); e == nil {
		t.Fatal("ordinary session heartbeat")
	}
	_ = sessionrole.Write(dir, sessionrole.Metadata{Pinned: true, Role: "coordinator"})
	if _, e := s.ToolCoordinatorHeartbeat(t.Context(), raw); e != nil {
		t.Fatal(e)
	}
	inv, e := s.ToolSessionInventory(t.Context(), json.RawMessage(`{}`))
	if e != nil {
		t.Fatal(e)
	}
	row := asInventory(t, inv).Sessions[0]
	if !row.Pinned || row.Presence == nil || row.Presence.State != "dead" || row.Presence.NextWake == "" {
		t.Fatalf("dead coordinator lost schedule %+v", row)
	}
}
