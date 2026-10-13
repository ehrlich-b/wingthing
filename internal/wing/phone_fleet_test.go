package wing

import (
	"encoding/json"
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
