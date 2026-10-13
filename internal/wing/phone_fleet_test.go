package wing

import (
	"encoding/json"
	"github.com/ehrlich-b/wingthing/internal/sshcontrol"
	"github.com/ehrlich-b/wingthing/internal/testssh"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/fleet"
	"github.com/ehrlich-b/wingthing/internal/localmcp"
)

func TestPhoneFleetInventoryExactActionAndOwnerGates(t *testing.T) {
	f := newSessionTransportFixture(t)
	created := callSessionMCP(t, f.mcp("http", "alice"), "terminal_start", map[string]any{"command": []string{"fixture-command"}, "cwd": f.work})
	id := created["session"].(string)
	if _, e := config.EnsureLocalOwner(f.cfg.Dir, "alice"); e != nil {
		t.Fatal(e)
	}
	listener, e := localmcp.ListenLocalWingControl(f.ctx, "fixture", f.sessions, "alice", nil)
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	h := &phoneSessions{service: f.sessions, owner: "alice"}
	result, e := h.Execute(f.ctx, "phone", "inventory", "", `{}`)
	if e != nil {
		t.Fatal(e)
	}
	data, _ := json.Marshal(result)
	var inv fleet.Inventory
	_ = json.Unmarshal(data, &inv)
	if len(inv.Sessions) != 1 || inv.Sessions[0].SessionID != id || inv.Sessions[0].OriginWingID != "fixture-wing" {
		t.Fatalf("phone origin missing: %s", data)
	}
	raw, _ := json.Marshal(map[string]any{"origin_wing_id": "fixture-wing", "session_id": id, "operation": "terminal_read"})
	if _, e = h.Execute(f.ctx, "phone", "action", "", string(raw)); e != nil {
		t.Fatal(e)
	}
	raw, _ = json.Marshal(map[string]any{"origin_wing_id": "fixture-wing", "session_id": id, "operation": "terminal_send", "arguments": map[string]any{"input": "qualified-input"}})
	if _, e = h.Execute(f.ctx, "phone", "action", "", string(raw)); e != nil {
		t.Fatal(e)
	}
	select {
	case input := <-f.egg(id).input:
		if string(input) != "qualified-input" {
			t.Fatalf("input crossed session: %q", input)
		}
	case <-time.After(time.Second):
		t.Fatal("qualified phone input not delivered")
	}
	if _, e = h.Execute(f.ctx, "phone", "inventory", "", `{"include_remotes":true}`); e == nil {
		t.Fatal("pairing broadened fleet authority")
	}
	h.owner = "other"
	if _, e = h.Execute(f.ctx, "phone", "inventory", "", `{}`); e == nil {
		t.Fatal("foreign owner fleet inherited")
	}
	h.owner = "alice"
	f.wc.Locked = true
	if _, e = h.Execute(f.ctx, "phone", "inventory", "", `{}`); e == nil {
		t.Fatal("locked gate bypass")
	}
	f.wc.Locked = false
	raw, _ = json.Marshal(map[string]any{"origin_wing_id": "fixture-wing", "session_id": id, "operation": "terminal_stop"})
	if _, e = h.Execute(f.ctx, "phone", "action", "", string(raw)); e != nil {
		t.Fatal(e)
	}
	select {
	case <-f.egg(id).killed:
	case <-time.After(time.Second):
		t.Fatal("qualified stop not delivered")
	}

}
func TestPhoneFleetEncryptedSearchAndForeignDenial(t *testing.T) {
	f := newSessionTransportFixture(t)
	created := callSessionMCP(t, f.mcp("http", "alice"), "terminal_start", map[string]any{"command": []string{"fixture-command"}, "cwd": f.work})
	id := created["session"].(string)
	response := f.tunnel("alice", map[string]any{"type": "sessions.inventory", "arguments": map[string]any{"query": id}})
	data, _ := json.Marshal(response)
	var inv fleet.Inventory
	_ = json.Unmarshal(data, &inv)
	if len(inv.Sessions) != 1 || inv.Sessions[0].SessionID != id {
		t.Fatalf("encrypted inventory not dispatched: %s", data)
	}
	foreign := f.tunnel("bob", map[string]any{"type": "sessions.search", "arguments": map[string]any{"query": id}})
	data, _ = json.Marshal(foreign)
	_ = json.Unmarshal(data, &inv)
	if len(inv.Sessions) != 0 {
		t.Fatalf("foreign search exposed session: %s", data)
	}
	response = f.tunnel("alice", map[string]any{"type": "session.control", "operation": "session_action", "arguments": map[string]any{"origin_wing_id": "fixture-wing", "session_id": id, "operation": "terminal_read"}})
	if response["error"] != nil {
		t.Fatalf("encrypted action: %+v", response)
	}
}

func TestPhoneFleetAttachmentExactOriginReadOnlyAndRevocation(t *testing.T) {
	f := newSessionTransportFixture(t)
	created := callSessionMCP(t, f.mcp("http", "alice"), "terminal_start", map[string]any{"command": []string{"fixture-command"}, "cwd": f.work})
	id := created["session"].(string)
	if _, err := config.EnsureLocalOwner(f.cfg.Dir, "alice"); err != nil {
		t.Fatal(err)
	}
	listener, err := localmcp.ListenLocalWingControl(f.ctx, "fixture", f.sessions, "alice", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	h := &phoneSessions{service: f.sessions, owner: "alice"}
	t.Cleanup(func() { h.Close("phone") })
	wire := func(origin, session, input string, readOnly bool) string {
		raw, _ := json.Marshal(map[string]any{"origin_wing_id": origin, "session_id": session, "input": input, "read_only": readOnly})
		return string(raw)
	}
	for _, raw := range []string{`null`, `{}`, `{"origin_wing_id":"fixture-wing","session_id":"x","extra":1}`} {
		if _, err := h.Execute(f.ctx, "phone", "fleet_attach", "", raw); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if _, err := h.Execute(f.ctx, "phone", "fleet_attach", "", wire("other-wing", id, "", false)); err == nil {
		t.Fatal("foreign origin attached locally")
	}
	if _, err := h.Execute(f.ctx, "phone", "fleet_attach", "", wire("fixture-wing", id[:6], "", true)); err == nil {
		t.Fatal("short ID attached")
	}
	result, err := h.Execute(f.ctx, "phone", "fleet_attach", "", wire("fixture-wing", id, "", true))
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["route"].(fleet.Route).Kind != "direct" {
		t.Fatal("direct route missing")
	}
	if _, err := h.Execute(f.ctx, "phone", "fleet_input", "", wire("fixture-wing", id, "never", false)); err == nil {
		t.Fatal("read-only attachment wrote")
	}
	h.Close("phone")
	if _, err := h.Execute(f.ctx, "phone", "fleet_attach", "", wire("fixture-wing", id, "", false)); err != nil {
		t.Fatal(err)
	}
	result, err = h.Execute(f.ctx, "phone", "fleet_read", "", wire("fixture-wing", id, "", false))
	if err != nil || !strings.Contains(result.(map[string]any)["screen"].(string), "fixture ready") {
		t.Fatalf("snapshot: %+v %v", result, err)
	}
	if _, err := h.Execute(f.ctx, "phone", "fleet_input", "", wire("other-wing", id, "never", false)); err == nil {
		t.Fatal("same-ID foreign input accepted")
	}
	if _, err := h.Execute(f.ctx, "phone", "input", id, "legacy-never"); err == nil {
		t.Fatal("legacy input crossed qualified attachment")
	}
	if _, err := h.Execute(f.ctx, "phone", "fleet_input", "", wire("fixture-wing", id, "qualified-pty-input", false)); err != nil {
		t.Fatal(err)
	}
	select {
	case data := <-f.egg(id).input:
		if string(data) != "qualified-pty-input" {
			t.Fatalf("input=%q", data)
		}
	case <-time.After(time.Second):
		t.Fatal("qualified input not delivered")
	}
	if err := os.WriteFile(filepath.Join(f.cfg.Dir, "clients.yaml"), []byte("require_client: true\nclients: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Execute(f.ctx, "phone", "fleet_read", "", wire("fixture-wing", id, "", false)); err == nil {
		t.Fatal("revoked client read buffered content")
	}
	if _, err := h.Execute(f.ctx, "phone", "fleet_input", "", wire("fixture-wing", id, "never", false)); err == nil {
		t.Fatal("revocation retained attachment")
	}
}

func TestPhoneFleetAttachmentGatewayCollidingIDsPinAndScope(t *testing.T) {
	f := newSessionTransportFixture(t)
	remote := newSessionTransportFixture(t)
	localCreated := callSessionMCP(t, f.mcp("http", "alice"), "terminal_start", map[string]any{"command": []string{"fixture-command"}, "cwd": f.work})
	peerCreated := callSessionMCP(t, remote.mcp("http", "alice"), "terminal_start", map[string]any{"command": []string{"fixture-command"}, "cwd": remote.work})
	localID, peerID := localCreated["session"].(string), peerCreated["session"].(string)
	// The fake egg accepts the local ID while its physical state is copied under
	// the colliding ID. Both source directories remain disposable fixtures.
	peerDir := filepath.Join(remote.cfg.Dir, "eggs", peerID)
	if err := os.Rename(peerDir, filepath.Join(remote.cfg.Dir, "eggs", localID)); err != nil {
		t.Fatal(err)
	}
	remote.mu.Lock()
	peerEgg := remote.eggs[peerID]
	delete(remote.eggs, peerID)
	remote.eggs[localID] = peerEgg
	peerEgg.dir = filepath.Join(remote.cfg.Dir, "eggs", localID)
	remote.mu.Unlock()
	remote.cfg.WingID = "peer-wing"
	f.wc.Paths, remote.wc.Paths = nil, nil
	f.wc.PhoneFleet = true
	for _, fixture := range []*sessionTransportFixture{f, remote} {
		if _, err := config.EnsureLocalOwner(fixture.cfg.Dir, "alice"); err != nil {
			t.Fatal(err)
		}
		listener, err := localmcp.ListenLocalWingControl(fixture.ctx, "fixture", fixture.sessions, "alice", nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = listener.Close() })
	}
	ssh := testssh.New(t)
	t.Setenv("PATH", ssh.Root+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WT_FAKE_SSH_ROOT", ssh.Root)
	meta, err := sshcontrol.InspectLocal(f.ctx, remote.cfg.Dir, "")
	if err != nil {
		t.Fatal(err)
	}
	ssh.Host(t, "peer-fixture", meta)
	saved := config.Remote{SSHTarget: "peer-fixture", WingID: meta.WingID, WingthingDir: meta.WingthingDir, ControlSocket: meta.ControlSocket, ControlVersion: meta.Version}
	if err := config.SaveRemotes(f.cfg.Dir, map[string]config.Remote{"peer": saved}); err != nil {
		t.Fatal(err)
	}
	h := &phoneSessions{service: f.sessions, owner: "alice"}
	t.Cleanup(func() { h.Close("phone") })
	wire := func(input string) string {
		raw, _ := json.Marshal(map[string]any{"origin_wing_id": "peer-wing", "session_id": localID, "input": input})
		return string(raw)
	}
	attach := func() {
		t.Helper()
		result, err := h.Execute(f.ctx, "phone", "fleet_attach", "", wire(""))
		if err != nil {
			t.Fatal(err)
		}
		route := result.(map[string]any)["route"].(fleet.Route)
		if route.Kind != "via" || route.Remote != "peer" {
			t.Fatalf("route=%+v", route)
		}
	}
	attach()
	if _, err := h.Execute(f.ctx, "phone", "fleet_input", "", wire("remote-only")); err != nil {
		t.Fatal(err)
	}
	select {
	case input := <-peerEgg.input:
		if string(input) != "remote-only" {
			t.Fatalf("input=%q", input)
		}
	case <-time.After(time.Second):
		t.Fatal("remote PTY input missing")
	}
	select {
	case input := <-f.egg(localID).input:
		t.Fatalf("local collision received %q", input)
	default:
	}
	f.wc.PhoneFleet = false
	if _, err := h.Execute(f.ctx, "phone", "fleet_read", "", wire("")); err == nil {
		t.Fatal("revoked delegation retained content")
	}
	f.wc.PhoneFleet = true
	attach()
	changed := saved
	changed.WingID = "replacement-wing"
	if err := config.SaveRemotes(f.cfg.Dir, map[string]config.Remote{"peer": changed}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Execute(f.ctx, "phone", "fleet_input", "", wire("never")); err == nil {
		t.Fatal("changed pin retained input")
	}
	if err := config.SaveRemotes(f.cfg.Dir, map[string]config.Remote{"peer": saved}); err != nil {
		t.Fatal(err)
	}
	attach()
	h.mu.Lock()
	quiet := h.fleetAttachments["phone"]
	h.mu.Unlock()
	if err := config.SaveRemotes(f.cfg.Dir, map[string]config.Remote{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-quiet.done:
	case <-time.After(4 * time.Second):
		t.Fatal("quiet attachment retained stream after saved pin removal")
	}
	if err := config.SaveRemotes(f.cfg.Dir, map[string]config.Remote{"peer": saved}); err != nil {
		t.Fatal(err)
	}
	attach()
	if err := os.WriteFile(filepath.Join(remote.cfg.Dir, "clients.yaml"), []byte("require_client: true\nclients: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Execute(f.ctx, "phone", "fleet_read", "", wire("")); err == nil {
		t.Fatal("revoked execution policy exposed buffered content")
	}
}
