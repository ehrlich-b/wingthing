package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	mcppkg "github.com/ehrlich-b/wingthing/internal/mcp"
	"github.com/ehrlich-b/wingthing/internal/promptmgr"
	"github.com/ehrlich-b/wingthing/internal/store"
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
	if len(listed.Result.Tools) != 27 {
		t.Fatalf("tools = %d, want 27", len(listed.Result.Tools))
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
		// Choosing a model is the most ordinary thing a human does at an agent
		// prompt. A model that cannot express it is not at parity, so the
		// passthrough has to be discoverable in the schema, not just accepted.
		if tool.Name == "agent_start" {
			properties := tool.InputSchema["properties"].(map[string]any)
			if _, ok := properties["model"]; !ok {
				t.Fatal("agent_start schema does not expose model selection")
			}
			args, ok := properties["args"].(map[string]any)
			if !ok {
				t.Fatal("agent_start schema does not expose agent arguments")
			}
			if args["type"] != "array" {
				t.Errorf("agent_start args type = %v, want array", args["type"])
			}
			items, ok := args["items"].(map[string]any)
			if !ok || items["type"] != "string" {
				t.Errorf("agent_start args items = %v, want string items", args["items"])
			}
		}
	}
	for _, want := range []string{
		"terminal_list", "terminal_start", "terminal_rename", "agent_start", "agent_run", "agent_status",
		"agent_wait", "agent_result", "agent_events", "agent_steer", "agent_stop", "prompt_list", "prompt_get", "prompt_save",
		"prompt_run", "prompt_loop", "swarm_run", "sandbox_explain", "message_send", "message_list", "message_wait",
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

func TestLocalMCPUnsandboxedModeIsExplicitAndAudited(t *testing.T) {
	dir := t.TempDir()
	server := &localMCPServer{
		cfg: &config.Config{Dir: dir, DefaultAgent: "claude"}, logs: &bytes.Buffer{},
		principal: "claude-code", unsandboxed: true,
	}
	capabilities, err := server.toolCapabilities(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if capabilities["session_isolation"] != "outer-boundary" {
		t.Fatalf("session isolation = %v", capabilities["session_isolation"])
	}
	if !strings.Contains(server.mcpInstructions(), "full authority") {
		t.Fatal("initialize instructions hide unsandboxed authority")
	}
	explained, err := server.toolSandboxExplain(json.RawMessage(`{"agent":"claude"}`))
	if err != nil {
		t.Fatal(err)
	}
	policy := explained["policy"].(explainedPolicy)
	if policy.ConfigSource != "MCP server --unsandboxed" || policy.Enforcement != "unrestricted" || policy.Isolation != "outer-boundary" {
		t.Fatalf("policy = %#v", policy)
	}
	if err := server.auditToolCall("wingthing_capabilities", json.RawMessage(`{}`), capabilities, "allowed"); err != nil {
		t.Fatal(err)
	}
	audit, err := os.ReadFile(filepath.Join(dir, "mcp-audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(audit, []byte(`"isolation":"outer-boundary"`)) {
		t.Fatalf("audit does not record trusted-host mode: %s", audit)
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

	// Strict decoding errors must survive validation. Reporting a missing
	// required field when the caller merely misspelled an optional one makes a
	// model retry the wrong fix and was found by dogfooding terminal_wait.
	badWaitArgs := localMCPRequest{
		JSONRPC: "2.0", ID: json.RawMessage(`3`), Method: "tools/call",
		Params: json.RawMessage(`{"name":"terminal_wait","arguments":{"session":"example","timeout":30}}`),
	}
	response, _ = server.handle(context.Background(), badWaitArgs)
	result = response.Result.(map[string]any)
	structured := result["structuredContent"].(map[string]any)
	if got := structured["error"]; got != `json: unknown field "timeout"` {
		t.Fatalf("terminal_wait strict error = %q", got)
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

func TestRoostMCPSandboxExplainBoundsExplicitConfig(t *testing.T) {
	workspace := t.TempDir()
	inside := filepath.Join(workspace, "egg.yaml")
	if err := os.WriteFile(inside, []byte("base: none\nfs: [\"rw:./\"]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "egg.yaml")
	if err := os.WriteFile(outside, []byte("base: none\nfs: [\"rw:/\"]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(workspace, "linked.yaml")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	server := &localMCPServer{
		cfg: &config.Config{Dir: t.TempDir(), DefaultAgent: "claude"}, logs: &bytes.Buffer{},
		allowedPaths: []string{canonicalSessionPath(workspace)}, enforcePathBounds: true,
	}
	for name, configPath := range map[string]string{
		"outside":         outside,
		"symlink outside": link,
	} {
		t.Run(name, func(t *testing.T) {
			arguments := json.RawMessage(`{"cwd":` + strconv.Quote(workspace) + `,"config":` + strconv.Quote(configPath) + `}`)
			if _, err := server.toolSandboxExplain(arguments); err == nil || !strings.Contains(err.Error(), "outside this user's roost paths") {
				t.Fatalf("explicit config %q error = %v", configPath, err)
			}
		})
	}

	arguments := json.RawMessage(`{"cwd":` + strconv.Quote(workspace) + `,"config":` + strconv.Quote(inside) + `}`)
	if _, err := server.toolSandboxExplain(arguments); err != nil {
		t.Fatalf("in-workspace config rejected: %v", err)
	}
}

// TestAgentStartArgsAreValidated keeps the passthrough from becoming a hole.
// These arguments become argv for a real process, so they are checked before a
// session is ever spawned rather than failing somewhere inside the egg.
func TestAgentStartArgsAreValidated(t *testing.T) {
	tests := map[string][]string{
		"empty argument":   {""},
		"NUL byte":         {"--model\x00sonnet"},
		"blank after trim": {"   "},
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			if err := validateAgentArgs(args); err == nil {
				t.Fatalf("accepted %q", args)
			}
		})
	}
	if err := validateAgentArgs([]string{"--model", "sonnet"}); err != nil {
		t.Fatalf("rejected ordinary args: %v", err)
	}
	if err := validateAgentArgs(nil); err != nil {
		t.Fatalf("rejected empty args: %v", err)
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

func TestLocalMCPPrincipalOwnershipAndAudit(t *testing.T) {
	dir := t.TempDir()
	createSession := func(id, principal string) {
		sessionDir := filepath.Join(dir, "eggs", id)
		if err := os.MkdirAll(sessionDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sessionDir, "egg.pid"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sessionDir, "egg.meta"), []byte("kind=agent\nagent=claude\ncwd=/tmp\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if principal != "" {
			if err := writeSessionPrincipal(sessionDir, principal); err != nil {
				t.Fatal(err)
			}
		}
	}
	createSession("alpha001", "alpha")
	createSession("beta001", "beta")
	createSession("human01", "")

	server := &localMCPServer{
		cfg:       &config.Config{Dir: dir, DefaultAgent: "claude"},
		logs:      &bytes.Buffer{},
		principal: "alpha",
	}
	listed, isError, protocolErr := server.callTool(context.Background(), "terminal_list", json.RawMessage(`{}`))
	if protocolErr != nil || isError {
		t.Fatalf("terminal_list failed: %#v %v", listed, protocolErr)
	}
	sessions := listed["sessions"].([]localSession)
	if len(sessions) != 1 || sessions[0].ID != "alpha001" {
		t.Fatalf("alpha saw sessions %#v", sessions)
	}
	if _, err := server.resolveOwnedSession(context.Background(), "beta001"); err == nil || err.Error() != "session not found or not owned by caller" {
		t.Fatalf("cross-principal lookup error = %v", err)
	}

	defaultServer := &localMCPServer{cfg: server.cfg, logs: &bytes.Buffer{}}
	if _, err := defaultServer.resolveOwnedSession(context.Background(), "human01"); err != nil {
		t.Fatalf("default principal should retain access to legacy sessions: %v", err)
	}
	if _, err := defaultServer.resolveOwnedSession(context.Background(), "alpha001"); err == nil {
		t.Fatal("default principal reached a named client's session")
	}

	auditData, err := os.ReadFile(filepath.Join(dir, "mcp-audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(auditData, []byte(`"principal":"alpha"`)) || !bytes.Contains(auditData, []byte(`"tool":"terminal_list"`)) {
		t.Fatalf("audit log missing attribution: %s", auditData)
	}
}

func TestLocalMCPClientConfigGrantsAndBounds(t *testing.T) {
	dir := t.TempDir()
	contents := []byte(`require_client: true
clients:
  observer:
    owner: ehrlich
    grants: [terminal.read]
    bounds:
      max_sessions: 2
      max_spawns_per_hour: 3
`)
	if err := os.WriteFile(filepath.Join(dir, "clients.yaml"), contents, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadLocalMCPClientsConfig(&config.Config{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	client := loaded.Clients["observer"]
	server := &localMCPServer{
		cfg:              &config.Config{Dir: dir},
		logs:             &bytes.Buffer{},
		principal:        "observer",
		grants:           grantSet(client.Grants),
		maxSessions:      client.Bounds.MaxSessions,
		maxSpawnsPerHour: client.Bounds.MaxSpawnsPerHour,
	}
	if !loaded.RequireClient || client.Owner != "ehrlich" || !server.toolAllowed("terminal_list") || server.toolAllowed("terminal_send") || server.toolAllowed("agent_start") {
		t.Fatalf("grant evaluation is wrong: %#v", loaded)
	}
	result, isError, protocolErr := server.callTool(context.Background(), "terminal_send", json.RawMessage(`{"session":"x","input":"oops"}`))
	if protocolErr != nil || !isError || !strings.Contains(result["error"].(string), "lacks grant") {
		t.Fatalf("denied tool result = %#v isError=%v protocol=%v", result, isError, protocolErr)
	}
}

func TestLocalMCPRejectsUnknownConfiguredClient(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WINGTHING_DIR", dir)
	configData := []byte("require_client: true\nclients:\n  observer:\n    grants: [terminal.read]\n")
	if err := os.WriteFile(filepath.Join(dir, "clients.yaml"), configData, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := mcpCmd()
	cmd.SetArgs([]string{"stdio", "--client", "typo"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), `MCP client "typo" is not configured`) {
		t.Fatalf("unknown client error = %v", err)
	}
}

func TestLocalMCPRejectsImplicitDefaultWhenAnyClientsAreConfigured(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WINGTHING_DIR", dir)
	configData := []byte("clients:\n  observer:\n    grants: [terminal.read]\n")
	if err := os.WriteFile(filepath.Join(dir, "clients.yaml"), configData, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := mcpCmd()
	cmd.SetArgs([]string{"stdio"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), `MCP client "default" is not configured`) {
		t.Fatalf("implicit default error = %v", err)
	}
}

func TestLocalMCPMessagesShareOwnerAndPreserveActorIsolation(t *testing.T) {
	dir := t.TempDir()
	codex := &localMCPServer{
		cfg: &config.Config{Dir: dir}, logs: &bytes.Buffer{},
		principal: "ehrlich", actor: "codex",
	}
	claude := &localMCPServer{
		cfg: &config.Config{Dir: dir}, logs: &bytes.Buffer{},
		principal: "ehrlich", actor: "claude",
	}
	otherOwner := &localMCPServer{
		cfg: &config.Config{Dir: dir}, logs: &bytes.Buffer{},
		principal: "someone-else", actor: "claude",
	}

	secretBody := "filesystem gate passed; canary content stays redacted"
	sent, isError, protocolErr := codex.callTool(context.Background(), "message_send", json.RawMessage(`{
		"channel":"factory-security","kind":"evidence","content":"filesystem gate passed; canary content stays redacted"
	}`))
	if protocolErr != nil || isError {
		t.Fatalf("send = %#v isError=%v protocol=%v", sent, isError, protocolErr)
	}
	messageID := sent["message_id"].(string)

	listed, isError, protocolErr := claude.callTool(context.Background(), "message_list", json.RawMessage(`{"channel":"factory-security"}`))
	if protocolErr != nil || isError {
		t.Fatalf("list = %#v isError=%v protocol=%v", listed, isError, protocolErr)
	}
	messages := listed["messages"].([]map[string]any)
	if len(messages) != 1 || messages[0]["message_id"] != messageID || messages[0]["sender_actor"] != "codex" {
		t.Fatalf("claude messages = %#v", messages)
	}

	self, _, _ := codex.callTool(context.Background(), "message_list", json.RawMessage(`{"channel":"factory-security"}`))
	if got := self["messages"].([]map[string]any); len(got) != 0 {
		t.Fatalf("sender received its own broadcast: %#v", got)
	}
	foreign, _, _ := otherOwner.callTool(context.Background(), "message_list", json.RawMessage(`{"channel":"factory-security"}`))
	if got := foreign["messages"].([]map[string]any); len(got) != 0 {
		t.Fatalf("other owner saw messages: %#v", got)
	}

	audit, err := os.ReadFile(filepath.Join(dir, "mcp-audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(audit, []byte(secretBody)) {
		t.Fatalf("message content leaked into audit log: %s", audit)
	}
	if !bytes.Contains(audit, []byte(`"actor":"codex"`)) || !bytes.Contains(audit, []byte(`"target":"`+messageID+`"`)) {
		t.Fatalf("message audit missing actor/target: %s", audit)
	}
}

func TestLocalMCPMessageWaitUnblocksOnSameOwnerSend(t *testing.T) {
	dir := t.TempDir()
	codex := &localMCPServer{cfg: &config.Config{Dir: dir}, logs: &bytes.Buffer{}, principal: "ehrlich", actor: "codex"}
	claude := &localMCPServer{cfg: &config.Config{Dir: dir}, logs: &bytes.Buffer{}, principal: "ehrlich", actor: "claude"}

	type waitResult struct {
		data map[string]any
		err  error
	}
	waited := make(chan waitResult, 1)
	go func() {
		data, err := claude.toolMessageWait(context.Background(), json.RawMessage(`{
			"channel":"factory-live","timeout_seconds":2
		}`))
		waited <- waitResult{data: data, err: err}
	}()
	time.Sleep(250 * time.Millisecond)
	if _, err := codex.toolMessageSend(json.RawMessage(`{
		"channel":"factory-live","kind":"question","content":"rerun the Arli canary"
	}`)); err != nil {
		t.Fatal(err)
	}

	select {
	case result := <-waited:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.data["timed_out"] != false || len(result.data["messages"].([]map[string]any)) != 1 {
			t.Fatalf("wait result = %#v", result.data)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("message_wait stayed blocked after message_send")
	}
}

func TestRoostMessageToolsShareOneOwnerAcrossOAuthClients(t *testing.T) {
	dir := t.TempDir()
	workspace := t.TempDir()
	cfg := &config.Config{Dir: dir}
	if err := config.SaveWingConfig(dir, &config.WingConfig{Paths: config.PathList{{Path: workspace}}}); err != nil {
		t.Fatal(err)
	}
	var sendTool, listTool mcppkg.NativeTool
	for _, tool := range roostNativeMCPTools(cfg, true) {
		switch tool.Name {
		case "message_send":
			sendTool = tool
		case "message_list":
			listTool = tool
		}
	}
	if sendTool.Call == nil || listTool.Call == nil {
		t.Fatal("roost message tools are missing")
	}

	aliceCodex := mcppkg.Principal{UserID: "alice", Email: "alice@example.com", ClientID: "codex-client"}
	aliceClaude := mcppkg.Principal{UserID: "alice", Email: "alice@example.com", ClientID: "claude-client"}
	bobClaude := mcppkg.Principal{UserID: "bob", Email: "bob@example.com", ClientID: "claude-client"}
	sent, isError, err := sendTool.Call(context.Background(), aliceCodex, json.RawMessage(`{"content":"factory evidence","kind":"evidence"}`))
	if err != nil || isError {
		t.Fatalf("roost send = %#v isError=%v err=%v", sent, isError, err)
	}
	for _, test := range []struct {
		name      string
		principal mcppkg.Principal
		want      int
	}{{"same owner", aliceClaude, 1}, {"other owner", bobClaude, 0}, {"sender", aliceCodex, 0}} {
		t.Run(test.name, func(t *testing.T) {
			listed, isError, err := listTool.Call(context.Background(), test.principal, json.RawMessage(`{}`))
			if err != nil || isError {
				t.Fatalf("list = %#v isError=%v err=%v", listed, isError, err)
			}
			if got := len(listed["messages"].([]map[string]any)); got != test.want {
				t.Fatalf("messages = %d, want %d (%#v)", got, test.want, listed)
			}
		})
	}
}

func TestLocalMCPTaskOwnership(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{Dir: dir}
	taskStore, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := taskStore.CreateTask(&store.Task{ID: "task-beta", What: "secret", RunAt: time.Now(), Principal: "beta"}); err != nil {
		t.Fatal(err)
	}
	taskStore.Close()

	server := &localMCPServer{cfg: cfg, logs: &bytes.Buffer{}, principal: "alpha"}
	result, isError, protocolErr := server.callTool(context.Background(), "task_get", json.RawMessage(`{"task_id":"task-beta"}`))
	if protocolErr != nil || !isError || !strings.Contains(result["error"].(string), `owned by principal "beta"`) {
		t.Fatalf("cross-principal task result = %#v isError=%v protocol=%v", result, isError, protocolErr)
	}
}

func TestLocalMCPAgentRunLifecycleIsSemanticAndOwnerScoped(t *testing.T) {
	dir := t.TempDir()
	cwd := t.TempDir()
	cfg := &config.Config{Dir: dir, DefaultAgent: "claude"}
	started := make(chan struct{})
	release := make(chan struct{})
	server := &localMCPServer{
		cfg: cfg, logs: &bytes.Buffer{}, principal: "alpha",
		runAgentTask: func(ctx context.Context, _ *config.Config, taskStore *store.Store, task *store.Task, _ taskRunOptions) error {
			if err := taskStore.UpdateTaskStatus(task.ID, "running"); err != nil {
				return err
			}
			_ = taskStore.AppendLog(task.ID, "started", nil)
			close(started)
			select {
			case <-ctx.Done():
				return taskStore.SetTaskError(task.ID, ctx.Err().Error())
			case <-release:
			}
			if err := taskStore.SetTaskOutput(task.ID, "semantic ✓ result"); err != nil {
				return err
			}
			return taskStore.UpdateTaskStatus(task.ID, "done")
		},
	}
	startedData, isError, protocolErr := server.callTool(context.Background(), "agent_run", json.RawMessage(`{
		"prompt":"review this branch","agent":"claude","model":"opus","cwd":"`+cwd+`","label":"review"
	}`))
	if protocolErr != nil || isError {
		t.Fatalf("agent_run = %#v isError=%v protocol=%v", startedData, isError, protocolErr)
	}
	runID := startedData["run_id"].(string)
	<-started

	status, err := server.toolAgentStatus(json.RawMessage(`{"run_id":"` + runID + `"}`))
	if err != nil || status["status"] != "running" || status["model"] != "opus" {
		t.Fatalf("agent_status = %#v err=%v", status, err)
	}
	other := &localMCPServer{cfg: cfg, logs: &bytes.Buffer{}, principal: "beta"}
	wantHidden := fmt.Sprintf("agent run %q not found or not owned by caller", runID)
	if _, err := other.toolAgentStatus(json.RawMessage(`{"run_id":"` + runID + `"}`)); err == nil || err.Error() != wantHidden {
		t.Fatalf("cross-principal lookup error = %v, want %q", err, wantHidden)
	}
	wantMissing := `agent run "missing-run" not found or not owned by caller`
	if _, err := other.toolAgentStatus(json.RawMessage(`{"run_id":"missing-run"}`)); err == nil || err.Error() != wantMissing {
		t.Fatalf("missing lookup error = %v, want %q", err, wantMissing)
	}
	before, err := server.toolAgentResult(json.RawMessage(`{"run_id":"` + runID + `"}`))
	if err != nil || before["ready"] != false {
		t.Fatalf("early result = %#v err=%v", before, err)
	}

	close(release)
	waited, err := server.toolAgentWait(context.Background(), json.RawMessage(`{"run_id":"`+runID+`","timeout_seconds":2}`))
	if err != nil || waited["status"] != "done" {
		t.Fatalf("agent_wait = %#v err=%v", waited, err)
	}
	result, err := server.toolAgentResult(json.RawMessage(`{"run_id":"` + runID + `","max_chars":10}`))
	if err != nil || result["output"] != "semantic ✓" || result["truncated"] != true {
		t.Fatalf("agent_result = %#v err=%v", result, err)
	}
	events, err := server.toolAgentEvents(json.RawMessage(`{"run_id":"` + runID + `"}`))
	if err != nil || len(events["events"].([]map[string]any)) == 0 {
		t.Fatalf("agent_events = %#v err=%v", events, err)
	}
}

func TestAgentStopWinsCompletionRace(t *testing.T) {
	dir := t.TempDir()
	cwd := t.TempDir()
	started := make(chan struct{})
	server := &localMCPServer{
		cfg: &config.Config{Dir: dir, DefaultAgent: "claude"}, logs: &bytes.Buffer{}, principal: "alpha",
		runAgentTask: func(ctx context.Context, _ *config.Config, taskStore *store.Store, task *store.Task, _ taskRunOptions) error {
			if err := taskStore.UpdateTaskStatus(task.ID, "running"); err != nil {
				return err
			}
			close(started)
			<-ctx.Done()
			// Deliberately attempt the stale completion write that used to win
			// the cancellation race. toolAgentStop waits for this runner and
			// writes the final stopped state afterward.
			_ = taskStore.SetTaskOutput(task.ID, "late output")
			return taskStore.UpdateTaskStatus(task.ID, "done")
		},
	}
	created, err := server.toolAgentRun(json.RawMessage(`{"prompt":"keep working","agent":"claude","cwd":` + strconv.Quote(cwd) + `}`))
	if err != nil {
		t.Fatal(err)
	}
	runID := created["run_id"].(string)
	<-started
	stopped, err := server.toolAgentStop(json.RawMessage(`{"run_id":` + strconv.Quote(runID) + `}`))
	if err != nil {
		t.Fatal(err)
	}
	if stopped["status"] != "failed" || stopped["stopped"] != true {
		t.Fatalf("stopped = %#v", stopped)
	}
	result, err := server.toolAgentResult(json.RawMessage(`{"run_id":` + strconv.Quote(runID) + `}`))
	if err != nil || !strings.Contains(result["error"].(string), "stopped by MCP principal alpha") {
		t.Fatalf("final result = %#v err=%v", result, err)
	}
}

func TestUnsandboxedAgentRunPersistsPrivilegedIsolation(t *testing.T) {
	dir := t.TempDir()
	cwd := t.TempDir()
	seen := make(chan string, 1)
	server := &localMCPServer{
		cfg: &config.Config{Dir: dir, DefaultAgent: "claude"}, logs: &bytes.Buffer{},
		principal: "alpha", unsandboxed: true,
		runAgentTask: func(_ context.Context, _ *config.Config, taskStore *store.Store, task *store.Task, _ taskRunOptions) error {
			seen <- task.Isolation
			return taskStore.UpdateTaskStatus(task.ID, "done")
		},
	}
	created, err := server.toolAgentRun(json.RawMessage(`{"prompt":"trusted task","agent":"claude","cwd":` + strconv.Quote(cwd) + `}`))
	if err != nil {
		t.Fatal(err)
	}
	if created["isolation"] != "privileged" {
		t.Fatalf("submitted isolation = %#v", created)
	}
	select {
	case isolation := <-seen:
		if isolation != "privileged" {
			t.Fatalf("runner isolation = %q", isolation)
		}
	case <-time.After(time.Second):
		t.Fatal("agent runner did not start")
	}
}

func TestAgentStatusMarksOrphanedRunnerFailed(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{Dir: dir, DefaultAgent: "claude"}
	taskStore, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	task := &store.Task{
		ID: "orphaned-run", Type: "agent_run", What: "orphan", Agent: "claude",
		RunAt: time.Now(), CWD: t.TempDir(), Principal: "alpha", RunnerPID: 1 << 30,
	}
	if err := taskStore.CreateTask(task); err != nil {
		t.Fatal(err)
	}
	if err := taskStore.UpdateTaskStatus(task.ID, "running"); err != nil {
		t.Fatal(err)
	}
	taskStore.Close()
	server := &localMCPServer{cfg: cfg, logs: &bytes.Buffer{}, principal: "alpha"}
	status, err := server.toolAgentStatus(json.RawMessage(`{"run_id":"orphaned-run"}`))
	if err != nil {
		t.Fatal(err)
	}
	if status["status"] != "failed" {
		t.Fatalf("orphan status = %#v", status)
	}
	result, err := server.toolAgentResult(json.RawMessage(`{"run_id":"orphaned-run"}`))
	if err != nil || !strings.Contains(result["error"].(string), "supervising Wingthing process") {
		t.Fatalf("orphan result = %#v err=%v", result, err)
	}
}

func TestFailedParentDoesNotReleaseSteeredRun(t *testing.T) {
	dir := t.TempDir()
	cwd := t.TempDir()
	cfg := &config.Config{Dir: dir, DefaultAgent: "claude"}
	taskStore, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	parent := &store.Task{
		ID: "failed-parent", Type: "agent_run", What: "original review", Agent: "claude", Model: "opus",
		RunAt: time.Now(), CWD: cwd, Principal: "alpha", RunnerPID: os.Getpid(),
	}
	if err := taskStore.CreateTask(parent); err != nil {
		t.Fatal(err)
	}
	if err := taskStore.SetTaskError(parent.ID, "review failed"); err != nil {
		t.Fatal(err)
	}
	taskStore.Close()
	runnerCalled := make(chan struct{}, 1)
	server := &localMCPServer{
		cfg: cfg, logs: &bytes.Buffer{}, principal: "alpha",
		runAgentTask: func(context.Context, *config.Config, *store.Store, *store.Task, taskRunOptions) error {
			runnerCalled <- struct{}{}
			return nil
		},
	}
	created, err := server.toolAgentSteer(json.RawMessage(`{"run_id":"failed-parent","prompt":"focus on auth"}`))
	if err != nil {
		t.Fatal(err)
	}
	childID := created["run_id"].(string)
	waited, err := server.toolAgentWait(context.Background(), json.RawMessage(`{"run_id":`+strconv.Quote(childID)+`,"timeout_seconds":2}`))
	if err != nil || waited["status"] != "failed" {
		t.Fatalf("child wait = %#v err=%v", waited, err)
	}
	select {
	case <-runnerCalled:
		t.Fatal("failed parent released the steered child")
	default:
	}
	child, childStore, err := server.ownedAgentRun(childID)
	if err != nil {
		t.Fatal(err)
	}
	defer childStore.Close()
	if !strings.Contains(child.What, "Prior request:\noriginal review") || !strings.Contains(child.What, "New direction:\nfocus on auth") {
		t.Fatalf("steered prompt = %q", child.What)
	}
}

func TestStdioWaitDoesNotBlockStop(t *testing.T) {
	dir := t.TempDir()
	cwd := t.TempDir()
	started := make(chan struct{})
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	server := &localMCPServer{
		cfg: &config.Config{Dir: dir, DefaultAgent: "claude"}, in: inputReader, out: outputWriter,
		logs: &bytes.Buffer{}, principal: "alpha",
		runAgentTask: func(ctx context.Context, _ *config.Config, taskStore *store.Store, task *store.Task, _ taskRunOptions) error {
			if err := taskStore.UpdateTaskStatus(task.ID, "running"); err != nil {
				return err
			}
			close(started)
			<-ctx.Done()
			return taskStore.SetTaskError(task.ID, ctx.Err().Error())
		},
	}
	created, err := server.toolAgentRun(json.RawMessage(`{"prompt":"long task","agent":"claude","cwd":` + strconv.Quote(cwd) + `}`))
	if err != nil {
		t.Fatal(err)
	}
	runID := created["run_id"].(string)
	<-started
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.serve(context.Background()) }()
	if _, err := fmt.Fprintf(inputWriter, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"agent_wait","arguments":{"run_id":%s,"timeout_seconds":10}}}`+"\n", strconv.Quote(runID)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := fmt.Fprintf(inputWriter, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"agent_stop","arguments":{"run_id":%s}}}`+"\n", strconv.Quote(runID)); err != nil {
		t.Fatal(err)
	}

	responses := make(chan []localMCPResponse, 1)
	go func() {
		decoder := json.NewDecoder(outputReader)
		var got []localMCPResponse
		for len(got) < 2 {
			var response localMCPResponse
			if err := decoder.Decode(&response); err != nil {
				break
			}
			got = append(got, response)
		}
		responses <- got
	}()
	select {
	case got := <-responses:
		if len(got) != 2 {
			t.Fatalf("responses = %#v", got)
		}
		seen := map[string]bool{}
		for _, response := range got {
			seen[string(response.ID)] = true
		}
		if !seen["1"] || !seen["2"] {
			t.Fatalf("response IDs = %#v", seen)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("agent_stop was blocked behind agent_wait")
	}
	_ = inputWriter.Close()
	_ = outputReader.Close()
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("stdio server did not finish")
	}
}

func TestSharedRoostPathBoundsFailClosed(t *testing.T) {
	dir := t.TempDir()
	workspace := t.TempDir()
	server := &localMCPServer{
		cfg: &config.Config{Dir: dir, DefaultAgent: "claude"}, logs: &bytes.Buffer{},
		principal: "member", enforcePathBounds: true,
	}
	if _, err := server.resolveWorkingDirectory(workspace); err == nil || !strings.Contains(err.Error(), "no configured workspace paths") {
		t.Fatalf("empty path policy error = %v", err)
	}
	listed, err := server.toolTerminalList(context.Background(), json.RawMessage(`{}`))
	if err != nil || len(listed["sessions"].([]localSession)) != 0 {
		t.Fatalf("empty path policy list = %#v err=%v", listed, err)
	}
}

func TestSharedHostFilesystemPolicyIgnoresCallerWidening(t *testing.T) {
	stateDir := t.TempDir()
	workspace := t.TempDir()
	cfg := &config.Config{Dir: stateDir}
	source := &egg.EggConfig{
		FS:            []string{"rw:/", "rw:/Users/someone-else"},
		AgentSettings: map[string]string{"claude": "/host/secret/settings.json"},
	}
	sealed, err := sealedSharedHostEggConfig(cfg, source, workspace, []string{workspace})
	if err != nil {
		t.Fatal(err)
	}
	if len(sealed.FS) == 0 || sealed.FS[0] != "deny:/" {
		t.Fatalf("sealed fs = %#v", sealed.FS)
	}
	for _, rule := range sealed.FS {
		if rule == "rw:/" || strings.Contains(rule, "someone-else") {
			t.Fatalf("caller widened sealed policy with %q", rule)
		}
	}
	if !containsString(sealed.FS, "rw:"+canonicalSessionPath(workspace)) {
		t.Fatalf("workspace missing from sealed fs: %#v", sealed.FS)
	}
	if sealed.AgentSettings != nil {
		t.Fatalf("host agent settings survived sealing: %#v", sealed.AgentSettings)
	}
	if _, _, err := sharedHostFilesystemRules(cfg, []string{stateDir}); err == nil {
		t.Fatal("Wingthing state was accepted as a shared workspace")
	}
}

func TestRoostControlToolsKeepTwoUsersSessionsSeparate(t *testing.T) {
	dir := t.TempDir()
	workspace := t.TempDir()
	cfg := &config.Config{Dir: dir, DefaultAgent: "claude"}
	if err := config.SaveWingConfig(dir, &config.WingConfig{
		Paths: config.PathList{{Path: workspace}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, userID := range []string{"alice", "bob"} {
		sessionDir := filepath.Join(dir, "eggs", "session-"+userID)
		if err := os.MkdirAll(sessionDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sessionDir, "egg.pid"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sessionDir, "egg.meta"), []byte("cwd="+workspace+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := writeSessionPrincipal(sessionDir, roostSessionPrincipal(userID)); err != nil {
			t.Fatal(err)
		}
		if err := writeEggOwner(sessionDir, userID, userID+"@example.com"); err != nil {
			t.Fatal(err)
		}
	}
	var listTool mcppkg.NativeTool
	for _, tool := range roostNativeMCPTools(cfg, true) {
		if tool.Name == "terminal_list" {
			listTool = tool
			break
		}
	}
	if listTool.Call == nil {
		t.Fatal("roost terminal_list tool is missing")
	}
	for _, userID := range []string{"alice", "bob"} {
		result, isError, err := listTool.Call(context.Background(), mcppkg.Principal{
			UserID: userID, Email: userID + "@example.com", ClientID: "client-" + userID,
		}, json.RawMessage(`{}`))
		if err != nil || isError {
			t.Fatalf("%s terminal_list: result=%#v isError=%v err=%v", userID, result, isError, err)
		}
		sessions := result["sessions"].([]localSession)
		if len(sessions) != 1 || sessions[0].ID != "session-"+userID {
			t.Fatalf("%s saw sessions %#v", userID, sessions)
		}
		ownerPath := filepath.Join(dir, "eggs", sessions[0].ID, "egg.owner")
		info, err := os.Stat(ownerPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("owner metadata mode = %v", info.Mode().Perm())
		}
	}
}
