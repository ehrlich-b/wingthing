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

func TestRegistryReturnsDeeplyIndependentDefinitions(t *testing.T) {
	first := Tools(SurfaceLocalMCP)
	first[0].Annotations["readOnlyHint"] = false
	first[0].InputSchema["type"] = "mutated"
	first[0].Surfaces[0] = Surface("mutated")
	messageProperties := first[1].InputSchema["properties"].(map[string]any)
	messageProperties["content"].(map[string]any)["description"] = "mutated"

	second := Tools(SurfaceLocalMCP)
	if second[0].Annotations["readOnlyHint"] != true || second[0].InputSchema["type"] != "object" || second[0].Surfaces[0] != SurfaceLocalMCP {
		t.Fatalf("tool registry shared top-level storage: %#v", second[0])
	}
	secondProperties := second[1].InputSchema["properties"].(map[string]any)
	if secondProperties["content"].(map[string]any)["description"] == "mutated" {
		t.Fatal("tool registry shared nested schema storage")
	}
	lookedUp, ok := Lookup("message_send")
	if !ok || lookedUp.InputSchema["type"] != "object" {
		t.Fatalf("Lookup observed a prior mutation: %#v", lookedUp)
	}
}

func TestRegistryPreservesDeployedEmptyArrayDefaults(t *testing.T) {
	checks := []struct {
		tool string
		path []string
	}{
		{tool: "terminal_start", path: []string{"command"}},
		{tool: "agent_start", path: []string{"args"}},
		{tool: "prompt_save", path: []string{"variables"}},
		{tool: "swarm_run", path: []string{"nodes", "items", "properties", "depends_on"}},
	}
	for _, check := range checks {
		t.Run(check.tool, func(t *testing.T) {
			tool, ok := Lookup(check.tool)
			if !ok {
				t.Fatalf("Lookup(%q) failed", check.tool)
			}
			value := any(tool.InputSchema["properties"])
			for _, component := range check.path {
				mapping, ok := value.(map[string]any)
				if !ok {
					t.Fatalf("schema path %v reached %T, want object", check.path, value)
				}
				value = mapping[component]
			}
			property, ok := value.(map[string]any)
			if !ok {
				t.Fatalf("schema path %v reached %T, want property", check.path, value)
			}
			defaultValue, ok := property["default"].([]string)
			if !ok || defaultValue == nil || len(defaultValue) != 0 {
				t.Fatalf("default at %v = %#v (%T), want non-nil empty []string", check.path, property["default"], property["default"])
			}
			encoded, err := json.Marshal(property)
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &wire); err != nil {
				t.Fatal(err)
			}
			if string(wire["default"]) != "[]" {
				t.Fatalf("wire default at %v = %s, want []", check.path, wire["default"])
			}
		})
	}
}

func toolNames(tools []Tool) []string {
	names := make([]string, len(tools))
	for index, tool := range tools {
		names[index] = tool.Name
	}
	return names
}
