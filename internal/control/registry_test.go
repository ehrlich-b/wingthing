package control

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestRegistryDefinesExpectedSurfaceOperations(t *testing.T) {
	local := []string{
		"wingthing_capabilities",
		"sandbox_explain",
		"terminal_list", "terminal_read", "session_status", "session_read", "session_wait", "session_prompt", "terminal_send", "terminal_wait",
		"session_fork", "terminal_start", "agent_start",
		"agent_run", "agent_status", "agent_wait", "agent_wait_any", "agent_result",
		"agent_events", "agent_steer", "agent_stop",
		"terminal_rename", "terminal_stop",
		"conversation_bootstrap", "conversation_list", "conversation_read", "conversation_checkpoint", "conversation_wake",
		"wing_list",
	}
	http := []string{
		"wingthing_capabilities",
		"sandbox_explain",
		"terminal_list", "terminal_read", "session_status", "session_read", "session_wait", "session_prompt", "terminal_send", "terminal_wait",
		"session_fork", "terminal_start", "agent_start",
		"agent_run", "agent_status", "agent_wait", "agent_wait_any", "agent_result",
		"agent_events", "agent_steer", "agent_stop",
		"terminal_rename", "terminal_stop",
		"conversation_bootstrap", "conversation_list", "conversation_read", "conversation_checkpoint", "conversation_wake",
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

func TestAgentStartSchemaAllowsExactContinuationWithoutAgent(t *testing.T) {
	tool, ok := Lookup("agent_start")
	if !ok {
		t.Fatal("missing agent_start")
	}
	properties := tool.InputSchema["properties"].(map[string]any)
	for _, field := range []string{"resume_session", "conversation_role", "input", "request_id"} {
		if properties[field] == nil {
			t.Fatalf("missing continuation field %s", field)
		}
	}
	if _, globallyRequired := tool.InputSchema["required"]; globallyRequired {
		t.Fatal("agent must not be required for a continuation")
	}
	if tool.InputSchema["additionalProperties"] != false {
		t.Fatalf("open continuation schema %v", tool.InputSchema)
	}
}

func TestRegistryInputSchemasHaveNoTopLevelCombinators(t *testing.T) {
	for _, surface := range []Surface{SurfaceLocalMCP, SurfaceHTTPMCP, SurfaceDirectMCP} {
		t.Run(string(surface), func(t *testing.T) {
			for _, tool := range Tools(surface) {
				for _, keyword := range []string{"anyOf", "oneOf", "allOf"} {
					if _, ok := tool.InputSchema[keyword]; ok {
						t.Errorf("%s input schema has top-level %s", tool.Name, keyword)
					}
				}
			}
		})
	}
}

func TestAgentWaitAnySchemaAndPolicy(t *testing.T) {
	tool, ok := Lookup("agent_wait_any")
	if !ok {
		t.Fatal("missing agent_wait_any")
	}
	wait, _ := Lookup("agent_wait")
	if tool.Grant != wait.Grant || tool.AuditArguments != wait.AuditArguments || !reflect.DeepEqual(tool.AuditTargetKeys, []string{"run_ids"}) || !reflect.DeepEqual(tool.Annotations, wait.Annotations) {
		t.Fatalf("agent_wait_any policy differs from agent_wait: %#v", tool)
	}
	if !strings.Contains(tool.Description, "max_wait_hint_seconds: 110") || !strings.Contains(tool.Description, "under 110 seconds") {
		t.Fatalf("missing client timeout hint: %s", tool.Description)
	}
	properties := tool.InputSchema["properties"].(map[string]any)
	ids := properties["run_ids"].(map[string]any)
	if ids["type"] != "array" || ids["minItems"] != 1 || ids["maxItems"] != 64 || ids["items"].(map[string]any)["type"] != "string" {
		t.Fatalf("run_ids schema = %#v", ids)
	}
	if !reflect.DeepEqual(tool.InputSchema["required"], []string{"run_ids"}) {
		t.Fatalf("required = %v", tool.InputSchema["required"])
	}
	if timeout := properties["timeout_seconds"].(map[string]any); timeout["default"] != 30 || timeout["maximum"] != 600 {
		t.Fatalf("timeout schema = %#v", timeout)
	}
}

func TestObjectKindsFollowSurfaceAvailability(t *testing.T) {
	if got, want := ObjectKinds(SurfaceLocalMCP), []string{
		"terminal", "conversation", "agent_run", "sandbox_policy",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("local objects = %v, want %v", got, want)
	}
	if got, want := ObjectKinds(SurfaceHTTPMCP), []string{
		"wing", "terminal", "conversation", "agent_run", "sandbox_policy",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("HTTP objects = %v, want %v", got, want)
	}
	if got, want := ObjectKinds(SurfaceDirectMCP), []string{
		"wing", "terminal", "conversation", "agent_run", "sandbox_policy",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("direct objects = %v, want %v", got, want)
	}
}

func TestAuditTargetUsesOnlyDeclaredResourceFields(t *testing.T) {
	secret := json.RawMessage(`{"session":"s1","input":"do not log me"}`)
	if got := AuditTarget("terminal_send", secret, nil); got != "s1" {
		t.Fatalf("session target = %q", got)
	}
	if got := AuditTarget("wingthing_capabilities", json.RawMessage(`{"name":"not-approved"}`), nil); got != "" {
		t.Fatalf("capabilities leaked undeclared target %q", got)
	}
}

func TestSessionForkFlatSchemaGrantAndSourceAudit(t *testing.T) {
	tool, ok := Lookup("session_fork")
	if !ok || tool.Grant != "terminal.start" || tool.Annotations["readOnlyHint"] != false || tool.Annotations["destructiveHint"] != false {
		t.Fatalf("fork authority: %#v", tool)
	}
	properties := tool.InputSchema["properties"].(map[string]any)
	if len(properties) != 2 || properties["session"] == nil || properties["name"] == nil || tool.InputSchema["additionalProperties"] != false {
		t.Fatalf("fork schema: %#v", tool.InputSchema)
	}
	if target := AuditTarget("session_fork", json.RawMessage(`{"session":"source","name":"new-name"}`), map[string]any{"session": "new-session"}); target != "source" {
		t.Fatalf("fork audit target: %q", target)
	}
}

func TestAgentWaitAnyAuditTargetIsBounded(t *testing.T) {
	ids := []string{"first", "second", strings.Repeat("\n", 1000), strings.Repeat("界", 1000), "omitted"}
	arguments, err := json.Marshal(map[string]any{"run_ids": ids, "prompt": "secret"})
	if err != nil {
		t.Fatal(err)
	}
	target := AuditTarget("agent_wait_any", arguments, nil)
	var data struct {
		RunIDs []string `json:"run_ids"`
		Count  int      `json:"count"`
	}
	if err := json.Unmarshal([]byte(target), &data); err != nil {
		t.Fatal(err)
	}
	want := []string{"first", "second", strings.Repeat("\n", 61) + "...", strings.Repeat("界", 61) + "..."}
	if data.Count != len(ids) || !reflect.DeepEqual(data.RunIDs, want) || len(target) > 1600 {
		t.Fatalf("audit target = %q", target)
	}
	if strings.Contains(target, "omitted") || strings.Contains(target, "secret") {
		t.Fatalf("audit target leaked omitted arguments: %q", target)
	}
	for _, input := range []string{`{}`, `{"run_ids":[]}`, `{"run_ids":"invalid"}`, `{"run_ids":["first",1]}`} {
		if got := AuditTarget("agent_wait_any", json.RawMessage(input), nil); got != "" {
			t.Fatalf("invalid target for %s = %q", input, got)
		}
	}
	if got := AuditTarget("agent_wait", arguments, nil); got != "" {
		t.Fatalf("single-run audit used undeclared IDs: %q", got)
	}
}

func TestRegistryReturnsDeeplyIndependentDefinitions(t *testing.T) {
	first := Tools(SurfaceLocalMCP)
	first[0].Annotations["readOnlyHint"] = false
	first[0].InputSchema["type"] = "mutated"
	first[0].Surfaces[0] = Surface("mutated")
	var sendIndex int
	for index, tool := range first {
		if tool.Name == "terminal_send" {
			sendIndex = index
			break
		}
	}
	messageProperties := first[sendIndex].InputSchema["properties"].(map[string]any)
	messageProperties["input"].(map[string]any)["description"] = "mutated"

	second := Tools(SurfaceLocalMCP)
	if second[0].Annotations["readOnlyHint"] != true || second[0].InputSchema["type"] != "object" || second[0].Surfaces[0] != SurfaceLocalMCP {
		t.Fatalf("tool registry shared top-level storage: %#v", second[0])
	}
	secondProperties := second[sendIndex].InputSchema["properties"].(map[string]any)
	if secondProperties["input"].(map[string]any)["description"] == "mutated" {
		t.Fatal("tool registry shared nested schema storage")
	}
	lookedUp, ok := Lookup("terminal_send")
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
