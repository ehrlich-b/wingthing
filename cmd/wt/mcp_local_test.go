package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/promptmgr"
)

func TestLocalMCPStdioProtocolAndToolDiscovery(t *testing.T) {
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"wingthing_capabilities","arguments":{}}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	server := &localMCPServer{
		cfg:  &config.Config{Dir: t.TempDir(), DefaultAgent: "claude"},
		in:   strings.NewReader(input),
		out:  &output,
		logs: &bytes.Buffer{},
	}
	if err := server.serve(context.Background()); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("responses = %d, want 3 (notifications have no response):\n%s", len(lines), output.String())
	}
	var initialize map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &initialize); err != nil {
		t.Fatal(err)
	}
	result := initialize["result"].(map[string]any)
	if result["protocolVersion"] != localMCPProtocolVersion {
		t.Fatalf("protocolVersion = %v", result["protocolVersion"])
	}

	var listed struct {
		Result struct {
			Tools []localMCPTool `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Result.Tools) != 15 {
		t.Fatalf("tools = %d, want 15", len(listed.Result.Tools))
	}
	names := make(map[string]bool)
	for _, tool := range listed.Result.Tools {
		names[tool.Name] = true
		if tool.InputSchema["type"] != "object" {
			t.Errorf("tool %s has invalid input schema: %#v", tool.Name, tool.InputSchema)
		}
		if tool.Name == "prompt_run" {
			properties := tool.InputSchema["properties"].(map[string]any)
			if _, ok := properties["cwd"]; !ok {
				t.Error("prompt_run schema does not expose working directory")
			}
		}
	}
	for _, want := range []string{
		"terminal_list", "agent_start", "prompt_list", "prompt_get", "prompt_save",
		"prompt_run", "prompt_loop", "swarm_run", "sandbox_explain",
	} {
		if !names[want] {
			t.Errorf("missing tool %q", want)
		}
	}

	var capabilities struct {
		Result struct {
			IsError           bool           `json:"isError"`
			StructuredContent map[string]any `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[2]), &capabilities); err != nil {
		t.Fatal(err)
	}
	if capabilities.Result.IsError {
		t.Fatal("wingthing_capabilities returned an error")
	}
	if len(capabilities.Result.StructuredContent["agents"].([]any)) != 7 {
		t.Fatalf("agents = %#v", capabilities.Result.StructuredContent["agents"])
	}
}

func TestLocalMCPRejectsUnknownToolAndArguments(t *testing.T) {
	server := &localMCPServer{cfg: &config.Config{Dir: t.TempDir(), DefaultAgent: "claude"}, logs: &bytes.Buffer{}}

	unknown := localMCPRequest{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/call",
		Params: json.RawMessage(`{"name":"not_a_tool","arguments":{}}`),
	}
	response, respond := server.handle(context.Background(), unknown)
	if !respond || response.Error == nil || response.Error.Code != -32602 {
		t.Fatalf("unknown tool response = %#v", response)
	}

	badArgs := localMCPRequest{
		JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: "tools/call",
		Params: json.RawMessage(`{"name":"wingthing_capabilities","arguments":{"surprise":true}}`),
	}
	response, _ = server.handle(context.Background(), badArgs)
	result := response.Result.(map[string]any)
	if result["isError"] != true {
		t.Fatalf("unexpected arguments should be a tool error: %#v", result)
	}
}

func TestValidateSwarmRejectsInvalidGraphs(t *testing.T) {
	valid := []swarmNodeSpec{
		{ID: "research-a", Prompt: "research A", Agent: "claude"},
		{ID: "research-b", Prompt: "research B", Agent: "gemini"},
		{ID: "synthesize", Prompt: "synthesize", Agent: "codex", DependsOn: []string{"research-a", "research-b"}},
	}
	if err := validateSwarm(valid, "claude"); err != nil {
		t.Fatalf("valid swarm: %v", err)
	}

	tests := map[string][]swarmNodeSpec{
		"cycle": {
			{ID: "a", Prompt: "a", DependsOn: []string{"b"}},
			{ID: "b", Prompt: "b", DependsOn: []string{"a"}},
		},
		"unknown dependency": {
			{ID: "a", Prompt: "a", DependsOn: []string{"missing"}},
		},
		"unknown agent": {
			{ID: "a", Prompt: "a", Agent: "made-up-agent"},
		},
		"duplicate": {
			{ID: "a", Prompt: "a"},
			{ID: "a", Prompt: "again"},
		},
	}
	for name, nodes := range tests {
		t.Run(name, func(t *testing.T) {
			if err := validateSwarm(nodes, "claude"); err == nil {
				t.Fatal("invalid swarm was accepted")
			}
		})
	}
}

func TestGeneratedTaskIDsDoNotCollideWithinSecond(t *testing.T) {
	seen := make(map[string]bool)
	for range 1000 {
		id := genTaskID()
		if seen[id] {
			t.Fatalf("duplicate task ID %q", id)
		}
		seen[id] = true
	}
}

func TestResolveWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	got, err := resolveWorkingDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Fatalf("resolved directory = %q, want %q", got, dir)
	}
	if _, err := resolveWorkingDirectory(dir + "/missing"); err == nil {
		t.Fatal("missing working directory was accepted")
	}
}

// TestLocalMCPSandboxExplain proves the sandbox policy is reachable by a model,
// not only by a human reading `wt egg explain`. A capability only a human can
// drive is unfinished (CLAUDE.md), and "is this sandbox safe?" is exactly the
// question an orchestrating model has to be able to ask.
func TestLocalMCPSandboxExplain(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "egg.yaml")
	if err := os.WriteFile(configPath, []byte("base: none\nfs: [\"rw:./\"]\nnetwork: [corp.example]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	server := &localMCPServer{cfg: &config.Config{Dir: t.TempDir(), DefaultAgent: "claude"}, logs: &bytes.Buffer{}}

	args := json.RawMessage(`{"agent":"claude","config":` + strconv.Quote(configPath) + `}`)
	got, isError, protocolErr := server.callTool(context.Background(), "sandbox_explain", args)
	if protocolErr != nil || isError {
		t.Fatalf("sandbox_explain = %#v isError=%v protocol=%v", got, isError, protocolErr)
	}

	policy, ok := got["policy"].(explainedPolicy)
	if !ok {
		t.Fatalf("policy = %#v, want explainedPolicy", got["policy"])
	}
	if policy.Agent != "claude" {
		t.Errorf("agent = %q, want claude", policy.Agent)
	}
	if !containsString(policy.Domains, "corp.example") {
		t.Errorf("domains %v missing the declared domain", policy.Domains)
	}
	if len(policy.Drilled) == 0 {
		t.Error("no auto-drilled holes reported to the model")
	}
	if policy.Enforcement == "" {
		t.Error("policy does not tell the model whether the boundary is enforced")
	}

	// The whole policy must survive the JSON round trip a real client performs.
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded struct {
		Policy explainedPolicy `json:"policy"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(decoded.Policy.Drilled) != len(policy.Drilled) {
		t.Errorf("round trip lost holes: %d != %d", len(decoded.Policy.Drilled), len(policy.Drilled))
	}

	// Unknown arguments are rejected, like every other tool here.
	if _, _, err := server.callTool(context.Background(), "sandbox_explain", json.RawMessage(`{"surprise":true}`)); err != nil {
		t.Fatalf("protocol error: %v", err)
	}
	bad, isError, _ := server.callTool(context.Background(), "sandbox_explain", json.RawMessage(`{"surprise":true}`))
	if !isError {
		t.Fatalf("unknown argument accepted: %#v", bad)
	}

	// A missing config is an error, not a silent fallback to built-in defaults —
	// answering with the wrong policy is worse than refusing.
	missing, isError, _ := server.callTool(context.Background(),
		"sandbox_explain", json.RawMessage(`{"config":`+strconv.Quote(filepath.Join(dir, "nope.yaml"))+`}`))
	if !isError {
		t.Fatalf("missing config silently accepted: %#v", missing)
	}
}

func TestLocalMCPPromptManager(t *testing.T) {
	server := &localMCPServer{cfg: &config.Config{Dir: t.TempDir(), DefaultAgent: "claude"}, logs: &bytes.Buffer{}}
	saved, isError, protocolErr := server.callTool(context.Background(), "prompt_save", json.RawMessage(`{
		"name":"review","description":"review code","template":"Review {{.target}}",
		"variables":["target"],"agent":"opencode","cwd":"/work/repo"
	}`))
	if protocolErr != nil || isError {
		t.Fatalf("save = %#v isError=%v protocol=%v", saved, isError, protocolErr)
	}
	asset := saved["prompt"].(*promptmgr.Asset)
	if asset.Revision == "" {
		t.Fatal("saved prompt has no revision")
	}

	got, isError, _ := server.callTool(context.Background(), "prompt_get", json.RawMessage(`{"name":"review"}`))
	if isError || got["prompt"].(*promptmgr.Asset).Revision != asset.Revision {
		t.Fatalf("get = %#v isError=%v", got, isError)
	}
	listed, isError, _ := server.callTool(context.Background(), "prompt_list", json.RawMessage(`{}`))
	if isError || len(listed["prompts"].([]promptmgr.Asset)) != 1 {
		t.Fatalf("list = %#v isError=%v", listed, isError)
	}
}
