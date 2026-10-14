package localmcp

import (
	"context"
	"encoding/json"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPermissionMissingExplicitGrantAndMCPExclusion(t *testing.T) {
	sessions, _ := localSocketPolicyFixture(t)
	s, err := resolveLocalWingClient("test", sessions, "owner", NewMCPAdmissionState(), controlsocket.Hello{Human: "tui"})
	if err != nil {
		t.Fatal(err)
	}
	args := json.RawMessage(`{"wing_id":"fixture-wing","session_id":"exact","request_id":"request","digest":"digest","attempt_id":"attempt","decision":"allow"}`)
	for _, grants := range []map[string]bool{nil, {"terminal.read": true, "terminal.send": true}, {"permission.read": true}} {
		s.Grants = grants
		_, err = permissionControl(context.Background(), s, "tui", "permission_respond", args, func() (*Server, error) { return s, nil })
		if err == nil || !strings.Contains(err.Error(), "permission.respond") {
			t.Fatalf("missing grant: %v", err)
		}
	}
	audit, err := os.ReadFile(filepath.Join(s.Cfg.Dir, "permissions", "audit.jsonl"))
	if err != nil || strings.Count(string(audit), "denied") != 3 {
		t.Fatalf("missing denials: %s %v", audit, err)
	}
	for _, surface := range []control.Surface{control.SurfaceLocalMCP, control.SurfaceHTTPMCP, control.SurfaceDirectMCP} {
		for _, tool := range control.Tools(surface) {
			if strings.HasPrefix(tool.Name, "permission_") {
				t.Fatalf("MCP exposed %s", tool.Name)
			}
		}
	}
	// MCP cannot call these operations even when its caller supplies all grants.
	s.Grants = map[string]bool{"permission.read": true, "permission.respond": true}
	_, _, protocol := s.callTool(t.Context(), "permission_respond", args)
	if protocol == nil {
		t.Fatal("MCP answered permission")
	}
	listener, err := ListenLocalWingControl(t.Context(), "test", sessions, "owner", NewMCPAdmissionState())
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	c, err := controlsocket.Dial(t.Context(), "state", controlsocket.Hello{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, _, err = c.Call(t.Context(), "permission_respond", args); err == nil || !strings.Contains(err.Error(), "unavailable to MCP") {
		t.Fatalf("MCP socket bypass: %v", err)
	}
}
func TestPermissionHumanBindingRefusesScopedAndBoundConnections(t *testing.T) {
	sessions, _ := localSocketPolicyFixture(t)
	for _, hello := range []controlsocket.Hello{{Human: "tui", Conversation: "bound"}, {Human: "tui", Execution: "bound"}, {Human: "tui", Scope: "{}"}, {Human: "tui", Aggregate: true}, {Human: "other"}} {
		if _, err := resolveLocalWingClient("test", sessions, "owner", NewMCPAdmissionState(), hello); err == nil {
			t.Fatalf("invalid binding accepted: %+v", hello)
		}
	}
}
