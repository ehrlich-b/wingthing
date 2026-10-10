package localmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	agentpkg "github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	mcppkg "github.com/ehrlich-b/wingthing/internal/mcp"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
)

func TestLocalMCPStdioProtocolAndToolDiscovery(t *testing.T) {
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"wingthing_capabilities","arguments":{},"_meta":{"progressToken":"claude-code"}}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	server := testWingServer(t, &Server{Version: "dev",
		Cfg:  &config.Config{Dir: t.TempDir(), DefaultAgent: "claude"},
		In:   strings.NewReader(input),
		Out:  &output,
		Logs: &bytes.Buffer{},
	})
	if err := server.Serve(context.Background()); err != nil {
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
			Tools []LocalMCPTool `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &listed); err != nil {
		t.Fatal(err)
	}
	if want := len(control.Tools(control.SurfaceLocalMCP)); len(listed.Result.Tools) != want {
		t.Fatalf("tools = %d, want %d from the control registry", len(listed.Result.Tools), want)
	}
	names := make(map[string]bool)
	for _, tool := range listed.Result.Tools {
		names[tool.Name] = true
		if tool.InputSchema["type"] != "object" {
			t.Errorf("tool %s has invalid input schema: %#v", tool.Name, tool.InputSchema)
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
		"agent_wait", "agent_wait_any", "agent_result", "agent_events", "agent_steer", "agent_stop",
		"sandbox_explain",
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
	contract := capabilities.Result.StructuredContent["control_contract"].(map[string]any)
	if contract["surface"] != string(control.SurfaceLocalMCP) || contract["version"] != control.ContractVersion {
		t.Fatalf("local control contract = %#v", contract)
	}
	if got := len(contract["operations"].([]any)); got != len(listed.Result.Tools) {
		t.Fatalf("capability operations = %d, listed tools = %d", got, len(listed.Result.Tools))
	}
}

func TestLocalMCPStdioEOFCancelsOutstandingWait(t *testing.T) {
	inputReader, inputWriter := io.Pipe()
	var output bytes.Buffer
	server := testWingServer(t, &Server{Version: "dev",
		Cfg: &config.Config{Dir: t.TempDir(), DefaultAgent: "claude"},
		In:  inputReader, Out: &output, Logs: &bytes.Buffer{}, Principal: "owner", Actor: "test",
	})
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background()) }()
	if _, err := io.WriteString(inputWriter, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"agent_wait_any","arguments":{"run_ids":["missing"],"timeout_seconds":3600}}}`+"\n"); err != nil {
		t.Fatal(err)
	}
	// The request is dispatched asynchronously. EOF must cancel it whether it
	// has entered its wait loop or is just about to do so.
	if err := inputWriter.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("local MCP server remained stuck in a one-hour wait after stdin EOF")
	}
}

func TestLocalMCPCallAdmissionIsBounded(t *testing.T) {
	slots := make(chan struct{}, maxConcurrentLocalMCPCalls)
	for range maxConcurrentLocalMCPCalls {
		if !acquireLocalMCPCallSlot(slots) {
			t.Fatal("call slot rejected before reaching the limit")
		}
	}
	if acquireLocalMCPCallSlot(slots) {
		t.Fatal("call slot accepted beyond the concurrency limit")
	}
	<-slots
	if !acquireLocalMCPCallSlot(slots) {
		t.Fatal("released call slot was not reusable")
	}
}

func TestMCPToolCallParamsStillRejectUnknownEnvelopeFields(t *testing.T) {
	server := testWingServer(t, &Server{Version: "dev"})
	response, _ := server.handle(context.Background(), localMCPRequest{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/call",
		Params: json.RawMessage(`{"name":"wingthing_capabilities","arguments":{},"_meta":{"progressToken":"ok"},"surprise":true}`),
	})
	if response.Error == nil || response.Error.Code != -32602 || !strings.Contains(response.Error.Message, `unknown field "surprise"`) {
		t.Fatalf("unknown tool-call envelope field response = %#v", response)
	}
}

func TestUnknownMCPToolIsNotMisreportedAsMissingGrant(t *testing.T) {
	server := testWingServer(t, &Server{Version: "dev", Grants: map[string]bool{}, Logs: io.Discard})
	_, _, protocolErr := server.callTool(context.Background(), "not_a_tool", json.RawMessage(`{}`))
	if protocolErr == nil || protocolErr.Code != -32602 || protocolErr.Message != "unknown tool: not_a_tool" {
		t.Fatalf("unknown tool error = %#v", protocolErr)
	}
}

func TestRoostNativeMCPAuthorityIsExplicitBoundedAndShared(t *testing.T) {
	admission := NewMCPAdmissionState()
	paths := []string{"/srv/alice"}
	server := testNativeServer(t, "dev", &config.Config{Dir: t.TempDir()}, true, admission, mcppkg.Principal{
		UserID: "alice", Email: "alice@example.com", ClientID: "codex",
	}, paths)
	paths[0] = "/srv/mutated"
	if server.admission != admission || server.MaxSessions != defaultDirectMCPMaxSessions || server.MaxSpawnsPerHour != defaultDirectMCPMaxSpawnsPerHour {
		t.Fatalf("HTTP MCP bounds/admission = sessions %d spawns %d admission %p", server.MaxSessions, server.MaxSpawnsPerHour, server.admission)
	}
	if !server.enforcePathBounds || len(server.allowedPaths) != 1 || server.allowedPaths[0] != "/srv/alice" || !server.identity.SealedFS || !server.identity.SharedHost {
		t.Fatalf("HTTP MCP identity boundary = paths %#v identity %#v", server.allowedPaths, server.identity)
	}
	for _, tool := range control.ToolsForAuthority(control.SurfaceHTTPMCP, control.AuthorityWing) {
		if !server.Grants[tool.Grant] {
			t.Errorf("HTTP MCP default authority omitted explicit grant %q for %s", tool.Grant, tool.Name)
		}
	}
	if portal, ok := control.Lookup("wing_list"); !ok || !server.Grants[portal.Grant] {
		t.Fatal("HTTP MCP capabilities omitted the composite portal wing_list grant")
	}
}

func TestRoostCapabilitiesReportHTTPContract(t *testing.T) {
	dir := t.TempDir()
	workspace := t.TempDir()
	cfg := &config.Config{Dir: dir, DefaultAgent: "claude"}
	if err := config.SaveWingConfig(dir, &config.WingConfig{
		Paths: config.PathList{{Path: workspace}},
	}); err != nil {
		t.Fatal(err)
	}
	var capabilities mcppkg.NativeTool
	for _, tool := range testNativeTools(t, "dev", cfg, true) {
		if tool.Name == "wingthing_capabilities" {
			capabilities = tool
			break
		}
	}
	if capabilities.Call == nil {
		t.Fatal("roost capabilities tool is missing")
	}
	result, isError, err := capabilities.Call(context.Background(), mcppkg.Principal{
		UserID: "alice", Email: "alice@example.com", ClientID: "codex",
	}, json.RawMessage(`{}`))
	if err != nil || isError {
		t.Fatalf("capabilities = %#v isError=%v err=%v", result, isError, err)
	}
	contract := result["control_contract"].(map[string]any)
	if contract["surface"] != string(control.SurfaceHTTPMCP) || contract["version"] != control.ContractVersion {
		t.Fatalf("HTTP control contract = %#v", contract)
	}
	if got, want := contract["operations"], control.OperationNames(control.SurfaceHTTPMCP); !reflect.DeepEqual(got, want) {
		t.Fatalf("HTTP operations = %#v, want %#v", got, want)
	}
	if got, want := result["objects"], control.ObjectKinds(control.SurfaceHTTPMCP); !reflect.DeepEqual(got, want) {
		t.Fatalf("HTTP objects = %#v, want %#v", got, want)
	}
}

func TestLocalMCPUnsandboxedModeIsExplicitAndAudited(t *testing.T) {
	dir := t.TempDir()
	server := testWingServer(t, &Server{Version: "dev",
		Cfg: &config.Config{Dir: dir, DefaultAgent: "claude"}, Logs: &bytes.Buffer{},
		Principal: "claude-code", Unsandboxed: true,
	})
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
	if !strings.Contains(strings.ToLower(server.mcpInstructions()), "agent manager") {
		t.Fatal("initialize instructions do not explain Wingthing's agent-manager role")
	}
	explained, err := server.toolSandboxExplain(json.RawMessage(`{"agent":"claude"}`))
	if err != nil {
		t.Fatal(err)
	}
	policy := explained["policy"].(eggclient.ExplainedPolicy)
	if policy.ConfigSource != "MCP server --unsandboxed" || policy.Enforcement != "unrestricted" || policy.Isolation != "outer-boundary" {
		t.Fatalf("policy = %#v", policy)
	}
	if _, err := server.toolSandboxExplain(json.RawMessage(`{"config":"egg.yaml"}`)); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("unsandboxed server accepted a sandbox config it would not enforce: %v", err)
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
	server := testWingServer(t, &Server{Version: "dev", Cfg: &config.Config{Dir: t.TempDir(), DefaultAgent: "claude"}, Logs: &bytes.Buffer{}})

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

func TestResolveWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	got, err := ResolveWorkingDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Fatalf("resolved directory = %q, want %q", got, dir)
	}
	if _, err := ResolveWorkingDirectory(dir + "/missing"); err == nil {
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
	server := testWingServer(t, &Server{Version: "dev", Cfg: &config.Config{Dir: t.TempDir(), DefaultAgent: "claude"}, Logs: &bytes.Buffer{}})

	args := json.RawMessage(`{"agent":"claude","config":` + strconv.Quote(configPath) + `}`)
	got, isError, protocolErr := server.callTool(context.Background(), "sandbox_explain", args)
	if protocolErr != nil || isError {
		t.Fatalf("sandbox_explain = %#v isError=%v protocol=%v", got, isError, protocolErr)
	}

	policy, ok := got["policy"].(eggclient.ExplainedPolicy)
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
		Policy eggclient.ExplainedPolicy `json:"policy"`
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

	server := testWingServer(t, &Server{Version: "dev",
		Cfg: &config.Config{Dir: t.TempDir(), DefaultAgent: "claude"}, Logs: &bytes.Buffer{},
		allowedPaths: []string{wingpolicy.CanonicalSessionPath(workspace)}, enforcePathBounds: true,
	})
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
			if err := eggclient.WriteSessionPrincipal(sessionDir, principal); err != nil {
				t.Fatal(err)
			}
		}
	}
	createSession("alpha001", "alpha")
	createSession("beta001", "beta")
	createSession("human01", "")

	server := testWingServer(t, &Server{Version: "dev",
		Cfg:       &config.Config{Dir: dir, DefaultAgent: "claude"},
		Logs:      &bytes.Buffer{},
		Principal: "alpha",
	})
	listed, isError, protocolErr := server.callTool(context.Background(), "terminal_list", json.RawMessage(`{}`))
	if protocolErr != nil || isError {
		t.Fatalf("terminal_list failed: %#v %v", listed, protocolErr)
	}
	sessions := listed["sessions"].([]eggclient.LocalSession)
	if len(sessions) != 1 || sessions[0].ID != "alpha001" {
		t.Fatalf("alpha saw sessions %#v", sessions)
	}
	if _, err := server.resolveOwnedSession(context.Background(), "beta001"); err == nil || err.Error() != "session not found or not owned by caller" {
		t.Fatalf("cross-principal lookup error = %v", err)
	}

	defaultServer := testWingServer(t, &Server{Version: "dev", Cfg: server.Cfg, Logs: &bytes.Buffer{}})
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
	loaded, err := LoadLocalMCPClientsConfig(&config.Config{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	client := loaded.Clients["observer"]
	server := testWingServer(t, &Server{Version: "dev",
		Cfg:              &config.Config{Dir: dir},
		Logs:             &bytes.Buffer{},
		Principal:        "observer",
		Grants:           GrantSet(client.Grants),
		MaxSessions:      client.Bounds.MaxSessions,
		MaxSpawnsPerHour: client.Bounds.MaxSpawnsPerHour,
	})
	if !loaded.RequireClient || client.Owner != "ehrlich" || !server.toolAllowed("terminal_list") || server.toolAllowed("terminal_send") || server.toolAllowed("agent_start") {
		t.Fatalf("grant evaluation is wrong: %#v", loaded)
	}
	result, isError, protocolErr := server.callTool(context.Background(), "terminal_send", json.RawMessage(`{"session":"x","input":"oops"}`))
	if protocolErr != nil || !isError || !strings.Contains(result["error"].(string), "lacks grant") {
		t.Fatalf("denied tool result = %#v isError=%v protocol=%v", result, isError, protocolErr)
	}
}

func TestLocalMCPAgentRunLifecycleIsSemanticAndOwnerScoped(t *testing.T) {

	f := newRunWingFixture(t, nil)
	id := f.admit(t, "review this branch")
	<-f.submitted
	before, err := f.server.toolAgentResult(runArgs(id))
	if err != nil || before["ready"] != false {
		t.Fatalf("early result: %v %v", before, err)
	}
	foreign := &Server{Version: f.server.Version, Cfg: f.server.Cfg, Sessions: f.service, Principal: "foreign", identity: f.server.identity, sessionRole: f.server.sessionRole}
	for _, target := range []string{id, "missing-run"} {
		if _, err := foreign.toolAgentStatus(runArgs(target)); err == nil || err.Error() != fmt.Sprintf("agent run %q not found or not owned by caller", target) {
			t.Fatalf("ownership error: %v", err)
		}
	}
	f.finish(t, id, "semantic ✓ result", "")
	f.wait(t, id)
	result, err := f.server.toolAgentResult(json.RawMessage(`{"run_id":"` + id + `","max_chars":10}`))
	if err != nil || result["output"] != "semantic ✓" || result["truncated"] != true {
		t.Fatalf("semantic result: %v %v", result, err)
	}
	events, err := f.server.toolAgentEvents(runArgs(id))
	if err != nil || len(events["events"].([]map[string]any)) == 0 {
		t.Fatalf("events: %v %v", events, err)
	}

}

func TestAgentStopWinsCompletionRace(t *testing.T) {

	f := newRunWingFixture(t, nil)
	id := f.admit(t, "keep working")
	<-f.submitted
	stopped, err := f.server.toolAgentStop(runArgs(id))
	if err != nil || stopped["status"] != "stopped" || stopped["stopped"] != true {
		t.Fatalf("stop: %v %v", stopped, err)
	}
	f.finish(t, id, "late output", "")
	result, err := f.server.toolAgentResult(runArgs(id))
	if err != nil || result["status"] != "stopped" || result["output"] != "" {
		t.Fatalf("stale completion won: %v %v", result, err)
	}

}

func TestUnsandboxedAgentRunPersistsPrivilegedIsolation(t *testing.T) {

	f := newRunWingFixture(t, &Server{Version: "test", Cfg: &config.Config{Dir: t.TempDir(), DefaultAgent: "claude"}, Principal: "alpha", Unsandboxed: true, Logs: io.Discard})
	id := f.admit(t, "trusted task")
	<-f.submitted
	status, err := f.server.toolAgentStatus(runArgs(id))
	if err != nil || status["isolation"] != "privileged" {
		t.Fatalf("isolation: %v %v", status, err)
	}
	launch := <-f.launches
	if egg.RequiresSandbox(launch.Config, "claude") {
		t.Fatal("outer profile was replaced at egg launch")
	}
	f.finish(t, id, "done", "")
	f.wait(t, id)

}

func TestAgentStatusMarksOrphanedRunnerFailed(t *testing.T) {

	cfg := &config.Config{Dir: t.TempDir(), DefaultAgent: "claude"}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	task := &store.Task{ID: "legacy", Type: "agent_run", What: "orphan", Agent: "claude", Principal: "alpha", Status: "running", RunnerPID: 1 << 30, RunAt: time.Now(), CWD: cfg.Dir}
	if err := db.CreateTask(task); err != nil {
		t.Fatal(err)
	}
	f := newRunWingFixture(t, &Server{Version: "test", Cfg: cfg, Principal: "alpha", Logs: io.Discard})
	status, err := f.server.toolAgentStatus(runArgs(task.ID))
	if err != nil || status["status"] != "failed" {
		t.Fatalf("legacy state: %v %v", status, err)
	}
	result, err := f.server.toolAgentResult(runArgs(task.ID))
	if err != nil || fmt.Sprint(result["failure_kind"]) != "unknown_outcome" || result["error"] != "Wingthing legacy run ended: unknown_outcome." {
		t.Fatalf("legacy result: %v %v", result, err)
	}

}

func TestAgentSteerContinuesTerminalRunsWithPartialResultAndError(t *testing.T) {

	for _, kind := range []agentpkg.ErrorKind{"", agentpkg.ProviderError, agentpkg.Timeout, agentpkg.Stopped} {
		t.Run(string(kind), func(t *testing.T) {
			f := newRunWingFixture(t, nil)
			parent := f.admit(t, "original review")
			<-f.submitted
			f.finish(t, parent, "partial review ✓", kind)
			f.wait(t, parent)
			created, err := f.server.toolAgentSteer(json.RawMessage(`{"run_id":"` + parent + `","prompt":"focus on auth"}`))
			if err != nil {
				t.Fatal(err)
			}
			child := created["run_id"].(string)
			if <-f.submitted != child {
				t.Fatal("wrong child")
			}
			r, err := f.service.RunManager.Get(f.server.sessionAuthority(), child)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(r.Prompt, "Prior result:\npartial review ✓") || !strings.HasSuffix(r.Prompt, "New direction:\nfocus on auth") || r.ParentID != parent || r.Agent != "claude" || r.Model != "opus" || r.TimeoutSeconds != 0 || !r.Result.Deadline.IsZero() {
				t.Fatalf("followup inheritance: %+v", r)
			}
			if kind != "" && !strings.Contains(r.Prompt, "Prior error:\nEgg run turn ended: "+string(kind)+".") {
				t.Fatal("missing authored prior error")
			}
			f.finish(t, child, "done", "")
			f.wait(t, child)
		})
	}

}

func TestAgentSteerQueuesActiveAndRejectsUnownedRuns(t *testing.T) {

	f := newRunWingFixture(t, nil)
	parent := f.admit(t, "review")
	<-f.submitted
	created, err := f.server.toolAgentSteer(json.RawMessage(`{"run_id":"` + parent + `","prompt":"continue"}`))
	if err != nil || created["status"] != "pending" {
		t.Fatalf("active parent queue: %v %v", created, err)
	}
	foreign := &Server{Version: f.server.Version, Cfg: f.server.Cfg, Sessions: f.service, Principal: "other", identity: f.server.identity, sessionRole: f.server.sessionRole}
	for _, id := range []string{parent, "missing"} {
		if _, err := foreign.toolAgentSteer(json.RawMessage(`{"run_id":"` + id + `","prompt":"continue"}`)); err == nil {
			t.Fatal("unowned steer admitted")
		}
	}
	if _, err := f.server.toolAgentStop(runArgs(parent)); err != nil {
		t.Fatal(err)
	}

}

func TestAgentSteerPassesAndPersistsPriorResult(t *testing.T) {

	f := newRunWingFixture(t, nil)
	parent := f.admit(t, "review this branch")
	<-f.submitted
	f.finish(t, parent, "the auth boundary is sound", "")
	f.wait(t, parent)
	created, err := f.server.toolAgentSteer(json.RawMessage(`{"run_id":"` + parent + `","prompt":"now review the UI"}`))
	if err != nil {
		t.Fatal(err)
	}
	child := created["run_id"].(string)
	<-f.submitted
	db, err := store.Open(f.server.Cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stored, err := db.GetTask(child)
	want := wingsession.SteerPrompt("review this branch", "the auth boundary is sound", "", "now review the UI")
	if err != nil || stored.What != want || stored.ParentID == nil || *stored.ParentID != parent {
		t.Fatalf("persisted followup: %+v %v", stored, err)
	}
	f.finish(t, child, "done", "")
	f.wait(t, child)

}

func TestAgentSteerBoundsPriorResultWithoutSplittingUnicode(t *testing.T) {
	prior := strings.Repeat("✓", 200000+1)
	prompt := wingsession.SteerPrompt("review", prior, "", "continue")
	if strings.Contains(prompt, strings.Repeat("✓", 200000+1)) {
		t.Fatal("follow-up retained the unbounded prior result")
	}
	if !strings.Contains(prompt, strings.Repeat("✓", 200000)) ||
		!strings.Contains(prompt, "[Wingthing truncated the prior result for this follow-up.]") ||
		!strings.HasSuffix(prompt, "New direction:\ncontinue") {
		t.Fatalf("bounded follow-up prompt has the wrong shape: prefix=%q suffix=%q", prompt[:64], prompt[len(prompt)-96:])
	}
}

func TestStdioWaitDoesNotBlockStop(t *testing.T) {

	f := newRunWingFixture(t, nil)
	id := f.admit(t, "long task")
	<-f.submitted
	input, send := io.Pipe()
	receive, output := io.Pipe()
	f.server.In = input
	f.server.Out = output
	done := make(chan error, 1)
	go func() { done <- f.server.Serve(t.Context()) }()
	replies := make(chan []localMCPResponse, 1)
	go func() {
		var got []localMCPResponse
		decoder := json.NewDecoder(receive)
		for len(got) < 2 {
			var r localMCPResponse
			if decoder.Decode(&r) != nil {
				break
			}
			got = append(got, r)
		}
		replies <- got
	}()
	for i, name := range []string{"agent_wait", "agent_stop"} {
		wire, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": i + 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": map[string]any{"run_id": id}}})
		if _, err := send.Write(append(wire, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	got := <-replies
	seen := map[string]bool{}
	for _, r := range got {
		seen[string(r.ID)] = true
	}
	if !seen["1"] || !seen["2"] {
		t.Fatalf("concurrent stdio responses: %+v", got)
	}
	send.Close()
	receive.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

}

func TestSharedRoostPathBoundsFailClosed(t *testing.T) {
	dir := t.TempDir()
	workspace := t.TempDir()
	server := testWingServer(t, &Server{Version: "dev",
		Cfg: &config.Config{Dir: dir, DefaultAgent: "claude"}, Logs: &bytes.Buffer{},
		Principal: "member", enforcePathBounds: true,
	})
	if _, err := server.resolveWorkingDirectory(workspace); err == nil || !strings.Contains(err.Error(), "no configured workspace paths") {
		t.Fatalf("empty path policy error = %v", err)
	}
	listed, err := server.ToolTerminalList(context.Background(), json.RawMessage(`{}`))
	if err != nil || len(listed["sessions"].([]eggclient.LocalSession)) != 0 {
		t.Fatalf("empty path policy list = %#v err=%v", listed, err)
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
		if err := eggclient.WriteSessionPrincipal(sessionDir, roostSessionPrincipal(userID)); err != nil {
			t.Fatal(err)
		}
		if err := eggclient.WriteEggOwner(sessionDir, userID, userID+"@example.com"); err != nil {
			t.Fatal(err)
		}
	}
	var listTool mcppkg.NativeTool
	for _, tool := range testNativeTools(t, "dev", cfg, true) {
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
		sessions := result["sessions"].([]eggclient.LocalSession)
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
