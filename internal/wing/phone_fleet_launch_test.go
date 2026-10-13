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
	// Null/empty and decoder-accepted case variants must deny before either
	// local launch or remote forwarding, on both encrypted user adapters.
	f.wc.PhoneFleet = true
	for _, origin := range []string{"fixture-wing", "unconfigured-remote"} {
		for _, empty := range []map[string]any{
			{"conversation_role": nil}, {"conversation_role": ""},
			{"parent_conversation_id": nil}, {"parent_conversation_id": ""},
			{"CONVERSATION_ROLE": nil}, {"Parent_Conversation_ID": ""},
		} {
			inner := map[string]any{"agent": "codex", "cwd": f.work}
			for key, value := range empty {
				inner[key] = value
			}
			args := map[string]any{"origin_wing_id": origin, "operation": "agent_start", "arguments": inner}
			raw, _ := json.Marshal(args)
			f.mu.Lock()
			before := len(f.eggs)
			f.mu.Unlock()
			if _, err := h.Execute(f.ctx, "phone", "launch", "", string(raw)); err == nil {
				t.Fatal("empty link phone launch accepted")
			}
			response := f.tunnel("alice", map[string]any{"type": "session.control", "operation": "session_launch", "arguments": args})
			if response["error"] != "phone/browser agent launch requires a linked personal conversation" {
				t.Fatal("empty link browser launch dispatched")
			}
			direct := f.tunnel("alice", map[string]any{"type": "session.control", "operation": "agent_start", "arguments": inner})
			if direct["error"] != "phone/browser agent launch requires a linked personal conversation" {
				t.Fatal("legacy browser launch retained empty-link bypass")
			}
			f.mu.Lock()
			count := len(f.eggs)
			f.mu.Unlock()
			if count != before {
				t.Fatal("empty link launched a runtime")
			}
		}
	}
}
