package wing

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/egg"
)

func TestPhoneSessionInventoryAttachmentInputAndStop(t *testing.T) {
	f := newSessionTransportFixture(t)
	created := callSessionMCP(t, f.mcp("http", "alice"), "terminal_start", map[string]any{"command": []string{"fixture-command"}, "cwd": f.work})
	id := created["session"].(string)
	h := &phoneSessions{service: f.sessions, owner: "alice", attachments: map[string]*phoneAttachment{}, seen: map[string]map[string]int64{}}
	t.Cleanup(func() { h.Close("phone-connection") })
	result, e := h.Execute(f.ctx, "phone-connection", "sessions", "", "")
	if e != nil {
		t.Fatal(e)
	}
	rows := result.([]map[string]any)
	if len(rows) != 1 || rows[0]["id"] != id {
		t.Fatal("phone didn't list main wing session")
	}
	result, e = h.Execute(f.ctx, "phone-connection", "thread", id, `{"after":0}`)
	if e != nil || result.(egg.SessionView).SessionID != id {
		t.Fatal("phone couldn't read exact owned native lifecycle", e)
	}
	for _, input := range []string{`{"after":-1}`, `not-json`} {
		if _, e = h.Execute(f.ctx, "phone-connection", "thread", id, input); e == nil {
			t.Fatal("invalid thread cursor accepted")
		}
	}
	if _, e = h.Execute(f.ctx, "phone-connection", "thread", id[:4], ""); e == nil {
		t.Fatal("thread accepted a display-only prefix")
	}
	foreign := &phoneSessions{service: f.sessions, owner: ""}
	if _, e = foreign.Execute(f.ctx, "foreign-phone", "thread", id, ""); e == nil {
		t.Fatal("thread accepted an unbound owner identity")
	}
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	if _, e = h.Execute(ctx, "phone-connection", "attach", id, ""); e != nil {
		t.Fatal(e)
	}
	result, e = h.Execute(ctx, "phone-connection", "read", id, "")
	if e != nil || !strings.Contains(result.(map[string]any)["screen"].(string), "fixture ready") {
		t.Fatal("native attachment didn't render existing terminal snapshot", e)
	}
	if _, e = h.Execute(ctx, "phone-connection", "input", "other-session", "never"); e == nil {
		t.Fatal("input crossed attachment")
	}
	if _, e = h.Execute(ctx, "phone-connection", "input", id, "phone input\r"); e != nil {
		t.Fatal(e)
	}
	select {
	case input := <-f.egg(id).input:
		if string(input) != "phone input\r" {
			t.Fatal("wrong phone input")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("phone input not delivered")
	}
	if _, e = h.Execute(ctx, "phone-connection", "config", id, ""); e == nil {
		t.Fatal("phone mutated config")
	}
	if _, e = h.Execute(ctx, "phone-connection", "stop", id, ""); e != nil {
		t.Fatal(e)
	}
	select {
	case <-f.egg(id).killed:
	case <-time.After(2 * time.Second):
		t.Fatal("phone stop not delivered")
	}
	h.Close("phone-connection")
	if _, e = h.Execute(ctx, "phone-connection", "input", id, "never"); e == nil {
		t.Fatal("closed phone retained stream")
	}
}
