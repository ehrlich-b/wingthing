package wing

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/localmcp"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

func TestBrowserFleetAttachmentCapabilityTakeoverHandBackAndIdle(t *testing.T) {
	f := newSessionTransportFixture(t)
	made := callSessionMCP(t, f.mcp("http", "alice"), "terminal_start", map[string]any{"command": []string{"fixture-command"}, "cwd": f.work})
	id := made["session"].(string)
	f.wc.Paths = nil
	if _, err := config.EnsureLocalOwner(f.cfg.Dir, "alice"); err != nil {
		t.Fatal(err)
	}
	listener, err := localmcp.ListenLocalWingControl(f.ctx, "fixture", f.sessions, "alice", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	manager := newBrowserFleetAttachments(f.ctx, f.sessions, "alice")
	defer manager.Close()
	req := ws.TunnelRequest{SenderUserID: "alice", SenderPub: "tab-key"}
	wire := func(session, token, input string, takeover bool, sequence uint64) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{"origin_wing_id": "fixture-wing", "session_id": session, "attachment_id": token, "input": input, "takeover": takeover, "sequence": sequence})
		return raw
	}
	token := "attachment-00000001"
	args := wire(id, token, "", true, 1)
	foreign := req
	foreign.SenderUserID = "bob"
	if _, err := manager.Execute(f.ctx, foreign, "fleet_attach", args); err == nil {
		t.Fatal("foreign owner attached")
	}
	if _, err := manager.Execute(f.ctx, req, "fleet_attach", wire(id[:6], "attachment-short00001", "", true, 1)); err == nil {
		t.Fatal("short ID attached")
	}
	response, err := manager.Execute(f.ctx, req, "fleet_attach", args)
	if err != nil || response["attachment_id"] != token || response["session_id"] != id {
		t.Fatalf("attach=%+v %v", response, err)
	}
	deadline := time.After(time.Second)
	observed := false
	for !observed {
		select {
		case opts := <-f.egg(id).attached:
			if opts.Takeover {
				if opts.ReadOnly {
					t.Fatal("takeover was read-only")
				}
				observed = true
			}
		case <-deadline:
			t.Fatal("takeover attachment not delivered")
		}
	}
	another := req
	another.SenderPub = "other-tab"
	if _, err := manager.Execute(f.ctx, another, "fleet_input", wire(id, token, "never", false, 2)); err == nil {
		t.Fatal("tab stole attachment")
	}
	if _, err := manager.Execute(f.ctx, req, "fleet_detach", wire(id, "attachment-00000002", "", false, 2)); err == nil {
		t.Fatal("stale token detached writer")
	}
	if _, err := manager.Execute(f.ctx, req, "fleet_input", wire(id, token, "typed-once", false, 2)); err != nil {
		t.Fatal(err)
	}
	select {
	case input := <-f.egg(id).input:
		if string(input) != "typed-once" {
			t.Fatalf("input=%q", input)
		}
	case <-time.After(time.Second):
		t.Fatal("missing input")
	}
	if _, err := manager.Execute(f.ctx, req, "fleet_input", wire(id, token, "typed-once", false, 2)); err == nil {
		t.Fatal("duplicate browser input replayed")
	}
	select {
	case input := <-f.egg(id).input:
		t.Fatalf("replay wrote %q", input)
	default:
	}
	response, err = manager.Execute(f.ctx, req, "fleet_detach", wire(id, token, "", false, 3))
	if err != nil || response["session_id"] != id {
		t.Fatalf("hand-back=%+v %v", response, err)
	}
	if _, err := manager.Execute(f.ctx, req, "fleet_input", wire(id, token, "never", false, 2)); err == nil {
		t.Fatal("writer retained after hand-back")
	}
	select {
	case <-f.egg(id).killed:
		t.Fatal("hand-back killed agent")
	default:
	}
	if _, err := manager.Execute(f.ctx, req, "fleet_attach", args); err == nil {
		t.Fatal("consumed takeover replayed after hand-back")
	}
	token = "attachment-new000001"
	args = wire(id, token, "", true, 1)
	if _, err := manager.Execute(f.ctx, req, "fleet_attach", args); err != nil {
		t.Fatal(err)
	}
	manager.expire(time.Now().Add(browserAttachmentIdle + time.Second))
	if _, err := manager.Execute(f.ctx, req, "fleet_read", wire(id, token, "", false, 3)); err == nil {
		t.Fatal("quiet writer retained")
	}
	if len(manager.snapshot()) != 0 {
		t.Fatal("expired attachment leaked")
	}
	if _, err := manager.Execute(f.ctx, req, "fleet_attach", args); err == nil {
		t.Fatal("consumed takeover replayed after expiry")
	}
	manager.Close()
	restarted := newBrowserFleetAttachments(f.ctx, f.sessions, "alice")
	defer restarted.Close()
	if _, err := restarted.Execute(f.ctx, req, "fleet_attach", args); err == nil {
		t.Fatal("consumed takeover replayed after manager restart")
	}

}

func TestPhoneFleetHandBackRequiresExactIdentity(t *testing.T) {
	f := newSessionTransportFixture(t)
	made := callSessionMCP(t, f.mcp("http", "alice"), "terminal_start", map[string]any{"command": []string{"fixture-command"}, "cwd": f.work})
	id := made["session"].(string)
	f.wc.Paths = nil
	if _, err := config.EnsureLocalOwner(f.cfg.Dir, "alice"); err != nil {
		t.Fatal(err)
	}
	listener, err := localmcp.ListenLocalWingControl(f.ctx, "fixture", f.sessions, "alice", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	phone := &phoneSessions{service: f.sessions, owner: "alice"}
	defer phone.Close("phone")
	raw := func(origin, session string) string {
		b, _ := json.Marshal(map[string]any{"origin_wing_id": origin, "session_id": session})
		return string(b)
	}
	if _, err := phone.Execute(f.ctx, "phone", "fleet_attach", "", raw("fixture-wing", id)); err != nil {
		t.Fatal(err)
	}
	if _, err := phone.Execute(f.ctx, "phone", "fleet_detach", "", raw("other-wing", id)); err == nil {
		t.Fatal("foreign same-ID detached")
	}
	response, err := phone.Execute(f.ctx, "phone", "fleet_detach", "", raw("fixture-wing", id))
	if err != nil || response.(map[string]any)["session_id"] != id {
		t.Fatalf("handback=%+v %v", response, err)
	}
	if _, err := phone.Execute(f.ctx, "phone", "fleet_input", "", raw("fixture-wing", id)); err == nil {
		t.Fatal("handback retained input")
	}
	select {
	case <-f.egg(id).killed:
		t.Fatal("handback killed agent")
	default:
	}
}

func TestBrowserFleetTakeoverRejectsMissingEggLeaseAcknowledgement(t *testing.T) {
	f := newSessionTransportFixture(t)
	made := callSessionMCP(t, f.mcp("http", "alice"), "terminal_start", map[string]any{"command": []string{"fixture-command"}, "cwd": f.work})
	id := made["session"].(string)
	f.wc.Paths = nil
	f.egg(id).mu.Lock()
	f.egg(id).omitLeaseAck = true
	f.egg(id).mu.Unlock()
	if _, err := config.EnsureLocalOwner(f.cfg.Dir, "alice"); err != nil {
		t.Fatal(err)
	}
	listener, err := localmcp.ListenLocalWingControl(f.ctx, "fixture", f.sessions, "alice", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	manager := newBrowserFleetAttachments(f.ctx, f.sessions, "alice")
	defer manager.Close()
	req := ws.TunnelRequest{SenderUserID: "alice", SenderPub: "tab-key"}
	args, _ := json.Marshal(map[string]any{"attachment_id": "attachment-00000001", "origin_wing_id": "fixture-wing", "session_id": id, "sequence": uint64(1)})
	if _, err := manager.Execute(f.ctx, req, "fleet_attach", args); err == nil {
		t.Fatal("implicit writer claim accepted")
	}
	args, _ = json.Marshal(map[string]any{"attachment_id": "attachment-00000001", "origin_wing_id": "fixture-wing", "session_id": id, "takeover": true, "sequence": uint64(1)})
	if _, err := manager.Execute(f.ctx, req, "fleet_attach", args); err == nil {
		t.Fatal("old egg acknowledged exclusive takeover without a lease")
	}
	if len(manager.snapshot()) != 0 {
		t.Fatal("failed lease acknowledgement retained an attachment")
	}
	select {
	case input := <-f.egg(id).input:
		t.Fatalf("unacknowledged writer received %q", input)
	default:
	}
}
