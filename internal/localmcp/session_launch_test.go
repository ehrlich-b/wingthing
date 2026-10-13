package localmcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/fleet"
)

func TestQualifiedSessionLaunchExactOriginPolicyAndNoReplay(t *testing.T) {
	s := inventoryFixture(t, "gateway", "gateway")
	if err := config.SaveRemotes(s.Cfg.Dir, map[string]config.Remote{
		"a": {SSHTarget: "fixture-a", WingID: "execution", WingthingDir: "/fixture/state", ControlSocket: "/fixture/control.sock", ControlVersion: control.ContractVersion},
		"b": {SSHTarget: "fixture-b", WingID: "execution", WingthingDir: "/fixture/state", ControlSocket: "/fixture/control.sock", ControlVersion: control.ContractVersion},
	}); err != nil {
		t.Fatal(err)
	}
	tool, ok := control.Lookup("session_launch")
	if !ok || tool.InputSchema["additionalProperties"] != false || tool.Grant != "terminal.start" {
		t.Fatal("launch schema/grant")
	}
	raw := json.RawMessage(`{"origin_wing_id":"execution","operation":"terminal_start","arguments":{"command":["fixture"],"cwd":"/personal/repo"}}`)
	calls := 0
	s.remoteToolCall = func(_ context.Context, name, operation string, input []byte) (map[string]any, error) {
		calls++
		var args launchArgs
		if err := json.Unmarshal(input, &args); err != nil {
			t.Fatal(err)
		}
		if name != "a" || operation != "session_launch" || !args.LocalOnly || args.OriginWingID != "execution" {
			t.Fatal("wrong/recursive launch route")
		}
		return map[string]any{"origin_wing_id": "execution", "session_id": "exact-new-id"}, nil
	}
	result, err := s.ToolSessionLaunch(t.Context(), raw)
	if err != nil || calls != 1 || result["route"].(fleet.Route).Kind != "via" {
		t.Fatalf("route: %v %v", result, err)
	}
	for _, input := range []string{
		`{"operation":"terminal_start"}`, `{"origin_wing_id":"unknown","operation":"terminal_start"}`,
		`{"origin_wing_id":"execution","operation":"terminal_stop"}`, `{"origin_wing_id":"execution","operation":"terminal_start","arguments":null}`,
		`{"origin_wing_id":"execution","operation":"agent_start","arguments":{"resume_session":"short"}}`,
		`{"origin_wing_id":"execution","operation":"terminal_start","extra":true}`,
		`{"origin_wing_id":"execution","operation":"terminal_start","local_only":true}`,
	} {
		before := calls
		if _, err := s.ToolSessionLaunch(t.Context(), json.RawMessage(input)); err == nil || calls != before {
			t.Fatalf("unsafe launch dispatched: %s", input)
		}
	}
	s.remoteToolCall = func(context.Context, string, string, []byte) (map[string]any, error) {
		calls++
		return nil, errors.New("lost reply")
	}
	calls = 0
	if _, err := s.ToolSessionLaunch(t.Context(), raw); err == nil || calls != 1 {
		t.Fatal("uncertain launch replayed")
	}
	s.Grants = map[string]bool{"terminal.read": true}
	calls = 0
	if _, err := s.ToolSessionLaunch(t.Context(), raw); err == nil || calls != 0 {
		t.Fatal("read grant launched")
	}
	s.Grants = nil
	for _, guard := range []string{"path", "conversation", "shared", "org"} {
		s.enforcePathBounds = guard == "path"
		s.BoundConversation = ""
		if guard == "conversation" {
			s.BoundConversation = "bound"
		}
		s.Sessions.SharedHost = guard == "shared"
		s.Sessions.Policy().Wing.Org = ""
		if guard == "org" {
			s.Sessions.Policy().Wing.Org = "org"
		}
		if _, err := s.ToolSessionLaunch(t.Context(), raw); err == nil || calls != 0 {
			t.Fatalf("restricted %s dispatched", guard)
		}
	}
}
