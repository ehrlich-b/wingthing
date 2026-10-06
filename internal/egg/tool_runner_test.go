package egg

import (
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
)

func TestToolRunnerCallWithEnvOverridesStaticIdentity(t *testing.T) {
	runner := NewToolRunner([]*config.ToolConfig{{
		Name: "whoami", Run: `printf '%s' "$WT_MCP_USER"`,
		Env: map[string]string{"WT_MCP_USER": "spoofed"},
	}})
	resp := runner.CallWithEnv("whoami", nil, map[string]string{"WT_MCP_USER": "alice"})
	if resp.Error != "" || resp.ExitCode != 0 {
		t.Fatalf("call failed: %+v", resp)
	}
	if resp.Stdout != "alice" {
		t.Fatalf("identity = %q, want alice", resp.Stdout)
	}
}

func TestToolRunnerDoesNotInheritUnrelatedHostSecrets(t *testing.T) {
	t.Setenv("WT_TEST_SECRET", "must-not-leak")
	runner := NewToolRunner([]*config.ToolConfig{{
		Name: "env-check", Run: `printf '%s|%s' "${WT_TEST_SECRET-unset}" "$PATH"`,
	}})
	resp := runner.Call("env-check", nil)
	if resp.ExitCode != 0 || resp.Error != "" {
		t.Fatalf("call failed: %+v", resp)
	}
	parts := strings.SplitN(resp.Stdout, "|", 2)
	if len(parts) != 2 || parts[0] != "unset" || parts[1] == "" {
		t.Fatalf("unexpected filtered environment: %q", resp.Stdout)
	}
}

func TestToolRunnerCapsOutput(t *testing.T) {
	runner := NewToolRunner([]*config.ToolConfig{{
		Name: "large-output", Run: `yes x | head -c 1048580`,
	}})
	resp := runner.Call("large-output", nil)
	if resp.ExitCode != 0 || resp.Error != "" {
		t.Fatalf("call failed: %+v", resp)
	}
	if !strings.Contains(resp.Stdout, "tool output truncated") {
		t.Fatalf("missing truncation marker; output length = %d", len(resp.Stdout))
	}
	if len(resp.Stdout) > maxToolOutputBytes+100 {
		t.Fatalf("output was not capped: %d bytes", len(resp.Stdout))
	}
}

func TestToolRunnerListIsStable(t *testing.T) {
	runner := NewToolRunner([]*config.ToolConfig{{Name: "zulu"}, {Name: "alpha"}})
	tools := runner.List()
	if len(tools) != 2 || tools[0].Name != "alpha" || tools[1].Name != "zulu" {
		t.Fatalf("tools are not sorted: %v", tools)
	}
}

func TestContextArgumentsPreserveTypesAndRejectInvalidValues(t *testing.T) {
	params := []config.ToolParam{
		{Name: "query", Type: "string", Required: true}, {Name: "page", Type: "integer"},
		{Name: "filter", Type: "object"}, {Name: "items", Type: "array"},
		{Name: "enabled", Type: "boolean"}, {Name: "ratio", Type: "number"},
	}
	values, err := contextArguments(params, []string{"q", "2", `{"key":"value"}`, `[1,2]`, "false", "1.5"})
	if err != nil || len(values) != 6 || values["query"] != "q" || values["enabled"] != false {
		t.Fatalf("values=%v err=%v", values, err)
	}
	values, err = contextArguments(params, []string{"q", "", ""})
	if err != nil || len(values) != 1 {
		t.Fatalf("optional values=%v err=%v", values, err)
	}
	for _, args := range [][]string{nil, {"q", "2.5"}, {"q", "true"}, {"q", "2", "[]"}, {"q", "2", "null"}, {"q", "2", "{}", "{}"}, {"q", "2", "{}", "[]", "1"}, {"q", "2", "{}", "[]", "true", "{}"}, {"q", "2 3"}, {"q", "2", "{}", "[]", "true", "1", "extra"}} {
		if _, err := contextArguments(params, args); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
}

func TestContextRunnerRefusesEnvIdentity(t *testing.T) {
	runner := NewToolRunner([]*config.ToolConfig{{Name: "context", Context: "jira-search", Env: map[string]string{"WT_USER_EMAIL": "spoof@slide.tech"}}})
	response := runner.CallWithEnv("context", nil, map[string]string{"WT_MCP_EMAIL": "spoof@slide.tech"})
	if response.Error != "context: wing context block is required" {
		t.Fatalf("response: %+v", response)
	}
}
