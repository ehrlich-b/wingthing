package control

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestRegistryDefinesExpectedSurfaceOperations(t *testing.T) {
	local := []string{
		"wingthing_capabilities",
		"message_send", "message_list", "message_wait",
		"sandbox_explain",
		"terminal_list", "terminal_read", "terminal_send", "terminal_wait",
		"terminal_start", "agent_start",
		"agent_run", "agent_status", "agent_wait", "agent_result",
		"agent_events", "agent_steer", "agent_stop",
		"terminal_rename", "terminal_stop",
		"prompt_list", "prompt_get", "prompt_save", "prompt_run", "task_get",
		"prompt_loop", "swarm_run",
	}
	http := []string{
		"wingthing_capabilities",
		"message_send", "message_list", "message_wait",
		"sandbox_explain",
		"terminal_list", "terminal_read", "terminal_send", "terminal_wait",
		"terminal_start", "agent_start",
		"agent_run", "agent_status", "agent_wait", "agent_result",
		"agent_events", "agent_steer", "agent_stop",
		"terminal_rename", "terminal_stop",
		"wing_list",
	}

	if got := toolNames(Tools(SurfaceLocalMCP)); !reflect.DeepEqual(got, local) {
		t.Fatalf("local MCP operations changed:\n got: %v\nwant: %v", got, local)
	}
	if got := toolNames(Tools(SurfaceHTTPMCP)); !reflect.DeepEqual(got, http) {
		t.Fatalf("HTTP MCP operations changed:\n got: %v\nwant: %v", got, http)
	}
	if got := toolNames(Tools(SurfaceDirectMCP)); !reflect.DeepEqual(got, http) {
		t.Fatalf("direct MCP operations changed:\n got: %v\nwant: %v", got, http)
	}
	for _, tool := range ToolsForAuthority(SurfaceDirectMCP, AuthorityWing) {
		properties := tool.InputSchema["properties"].(map[string]any)
		if _, ok := properties["wing_id"]; !ok {
			t.Errorf("direct operation %s has no wing_id", tool.Name)
		}
		required := tool.InputSchema["required"].([]string)
		if len(required) == 0 || required[0] != "wing_id" {
			t.Errorf("direct operation %s required = %v", tool.Name, required)
		}
	}
}

func TestRegistryDefinitionsAreComplete(t *testing.T) {
	seen := map[string]bool{}
	for _, tool := range Tools(SurfaceLocalMCP) {
		if seen[tool.Name] {
			t.Errorf("duplicate operation %q", tool.Name)
		}
		seen[tool.Name] = true
		if tool.Version == "" {
			t.Errorf("%s has no version", tool.Name)
		}
		if tool.Grant == "" {
			t.Errorf("%s has no grant", tool.Name)
		}
		if tool.Authority == "" {
			t.Errorf("%s has no authority", tool.Name)
		}
		if tool.AuditArguments != AuditArgumentsDigest {
			t.Errorf("%s audit arguments = %q, want digest", tool.Name, tool.AuditArguments)
		}
		if tool.InputSchema["type"] != "object" {
			t.Errorf("%s schema type = %v, want object", tool.Name, tool.InputSchema["type"])
		}
		if tool.InputSchema["additionalProperties"] != false {
			t.Errorf("%s schema is not closed: %#v", tool.Name, tool.InputSchema)
		}
		for _, hint := range []string{"readOnlyHint", "destructiveHint", "openWorldHint"} {
			if _, ok := tool.Annotations[hint].(bool); !ok {
				t.Errorf("%s annotation %s is missing or not boolean", tool.Name, hint)
			}
		}
		if _, ok := Lookup(tool.Name); !ok {
			t.Errorf("Lookup(%q) failed", tool.Name)
		}
	}
	for _, tool := range Tools(SurfaceHTTPMCP) {
		if tool.Authority == AuthorityWing && !seen[tool.Name] {
			t.Errorf("HTTP operation %q is absent from the local contract", tool.Name)
		}
	}
	if got := toolNames(ToolsForAuthority(SurfaceHTTPMCP, AuthorityPortal)); !reflect.DeepEqual(got, []string{"wing_list"}) {
		t.Fatalf("portal operations = %v, want [wing_list]", got)
	}
}

func TestObjectKindsFollowSurfaceAvailability(t *testing.T) {
	if got, want := ObjectKinds(SurfaceLocalMCP), []string{
		"terminal", "agent_run", "message", "prompt_asset", "task", "loop", "swarm", "sandbox_policy",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("local objects = %v, want %v", got, want)
	}
	if got, want := ObjectKinds(SurfaceHTTPMCP), []string{
		"wing", "terminal", "agent_run", "message", "sandbox_policy",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("HTTP objects = %v, want %v", got, want)
	}
	if got, want := ObjectKinds(SurfaceDirectMCP), []string{
		"wing", "terminal", "agent_run", "message", "sandbox_policy",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("direct objects = %v, want %v", got, want)
	}
}

func TestAuditTargetUsesOnlyDeclaredResourceFields(t *testing.T) {
	secret := json.RawMessage(`{"content":"do not log me","reply_to":"msg-1"}`)
	if got := AuditTarget("message_send", secret, map[string]any{"message_id": "msg-2"}); got != "msg-1" {
		t.Fatalf("message target = %q, want msg-1", got)
	}
	if got := AuditTarget("message_send", json.RawMessage(`{"content":"do not log me"}`), map[string]any{"message_id": "msg-2"}); got != "msg-2" {
		t.Fatalf("message result target = %q, want msg-2", got)
	}
	if got := AuditTarget("wingthing_capabilities", json.RawMessage(`{"name":"not-approved"}`), nil); got != "" {
		t.Fatalf("capabilities leaked undeclared target %q", got)
	}
}

func toolNames(tools []Tool) []string {
	names := make([]string, len(tools))
	for index, tool := range tools {
		names[index] = tool.Name
	}
	return names
}
