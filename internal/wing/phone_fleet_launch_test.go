package wing

import (
	"encoding/json"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/localmcp"
)

func TestPhoneFleetLaunchUsesExistingRuntimeAndEncryptedOwnerGates(t *testing.T) {
	f := newSessionTransportFixture(t)
	if _, err := config.EnsureLocalOwner(f.cfg.Dir, "alice"); err != nil {
		t.Fatal(err)
	}
	listener, err := localmcp.ListenLocalWingControl(f.ctx, "fixture", f.sessions, "alice", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	h := &phoneSessions{service: f.sessions, owner: "alice"}
	args := map[string]any{"origin_wing_id": "fixture-wing", "operation": "terminal_start", "arguments": map[string]any{"command": []string{"fixture-command"}, "cwd": f.work, "label": "phone-launch"}}
	raw, _ := json.Marshal(args)
	result, err := h.Execute(f.ctx, "phone", "launch", "", string(raw))
	if err != nil {
		t.Fatal(err)
	}
	receipt := result.(map[string]any)
	id, ok := receipt["session_id"].(string)
	if !ok || receipt["origin_wing_id"] != "fixture-wing" || f.egg(id) == nil {
		t.Fatal("launch did not reach existing runtime")
	}
	args["arguments"].(map[string]any)["label"] = "browser-launch"
	encrypted := f.tunnel("alice", map[string]any{"type": "session.control", "operation": "session_launch", "arguments": args})
	if encrypted["error"] != nil || encrypted["session_id"] == nil {
		t.Fatalf("encrypted launch failed: %v", encrypted["error"])
	}
	foreign := f.tunnel("bob", map[string]any{"type": "session.control", "operation": "session_launch", "arguments": args})
	if foreign["error"] == nil {
		t.Fatal("foreign browser inherited personal launch")
	}
	h.owner = "bob"
	if _, err := h.Execute(f.ctx, "phone", "launch", "", string(raw)); err == nil {
		t.Fatal("foreign phone launched")
	}
	h.owner = "alice"
	f.wc.Locked = true
	if _, err := h.Execute(f.ctx, "phone", "launch", "", string(raw)); err == nil {
		t.Fatal("locked phone launched")
	}
	f.wc.Locked = false
	args["origin_wing_id"] = "remote-origin"
	raw, _ = json.Marshal(args)
	if _, err := h.Execute(f.ctx, "phone", "launch", "", string(raw)); err == nil {
		t.Fatal("disabled delegation launched remote")
	}
	args["origin_wing_id"] = "fixture-wing"
	args["operation"] = "agent_start"
	args["arguments"] = map[string]any{"agent": "claude", "cwd": f.work}
	raw, _ = json.Marshal(args)
	if _, err := h.Execute(f.ctx, "phone", "launch", "", string(raw)); err == nil {
		t.Fatal("phone bypassed linked-conversation browser launch restriction")
	}
}
