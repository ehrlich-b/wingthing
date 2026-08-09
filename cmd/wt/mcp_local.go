package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	agentpkg "github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/promptmgr"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

const localMCPProtocolVersion = "2025-11-25"

func mcpCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Expose Wingthing to LLM clients",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "stdio",
		Short: "Run the local Wingthing MCP server over stdin/stdout",
		Long: "Run a newline-delimited MCP server that lets a local LLM client discover agents, " +
			"control persistent terminals, run prompts, and coordinate bounded loops and swarms.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			server := &localMCPServer{cfg: cfg, in: os.Stdin, out: os.Stdout, logs: os.Stderr}
			return server.serve(cmd.Context())
		},
	})
	return cmd
}

type localMCPServer struct {
	cfg  *config.Config
	in   io.Reader
	out  io.Writer
	logs io.Writer
}

type localMCPRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type localMCPResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *localMCPError  `json:"error,omitempty"`
}

type localMCPError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type localMCPTool struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`
}

func (s *localMCPServer) serve(ctx context.Context) error {
	scanner := bufio.NewScanner(s.in)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	encoder := json.NewEncoder(s.out)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		var request localMCPRequest
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			if encodeErr := encoder.Encode(localMCPResponse{
				JSONRPC: "2.0",
				Error:   &localMCPError{Code: -32700, Message: "parse error"},
			}); encodeErr != nil {
				return encodeErr
			}
			continue
		}
		response, respond := s.handle(ctx, request)
		if !respond {
			continue
		}
		if err := encoder.Encode(response); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read MCP request: %w", err)
	}
	return nil
}

func (s *localMCPServer) handle(ctx context.Context, request localMCPRequest) (localMCPResponse, bool) {
	response := localMCPResponse{JSONRPC: "2.0", ID: request.ID}
	if request.JSONRPC != "2.0" || request.Method == "" {
		response.Error = &localMCPError{Code: -32600, Message: "invalid request"}
		return response, len(request.ID) > 0
	}

	switch request.Method {
	case "initialize":
		response.Result = map[string]any{
			"protocolVersion": localMCPProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo": map[string]any{
				"name":    "wingthing-local",
				"version": version,
			},
			"instructions": "Wingthing is a local-first runtime. Use terminal tools for persistent PTYs, prompt_run for one headless model call, prompt_loop for bounded iteration, and swarm_run for a dependency DAG.",
		}
	case "notifications/initialized", "notifications/cancelled":
		return localMCPResponse{}, false
	case "ping":
		response.Result = map[string]any{}
	case "tools/list":
		response.Result = map[string]any{"tools": localMCPTools()}
	case "tools/call":
		var call struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := decodeStrict(request.Params, &call); err != nil || call.Name == "" {
			response.Error = &localMCPError{Code: -32602, Message: "invalid tools/call params"}
			break
		}
		if len(call.Arguments) == 0 {
			call.Arguments = json.RawMessage(`{}`)
		}
		data, isError, protocolErr := s.callTool(ctx, call.Name, call.Arguments)
		if protocolErr != nil {
			response.Error = protocolErr
			break
		}
		response.Result = localMCPToolResult(data, isError)
	default:
		if len(request.ID) == 0 {
			return localMCPResponse{}, false
		}
		response.Error = &localMCPError{Code: -32601, Message: "method not found: " + request.Method}
	}
	return response, len(request.ID) > 0
}

func localMCPTools() []localMCPTool {
	stringProperty := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	objectSchema := func(properties map[string]any, required ...string) map[string]any {
		schema := map[string]any{
			"type":                 "object",
			"properties":           properties,
			"additionalProperties": false,
		}
		if len(required) > 0 {
			schema["required"] = required
		}
		return schema
	}
	readOnly := map[string]any{"readOnlyHint": true, "destructiveHint": false, "openWorldHint": false}
	mutating := map[string]any{"readOnlyHint": false, "destructiveHint": false, "openWorldHint": false}
	modelCall := map[string]any{"readOnlyHint": false, "destructiveHint": false, "openWorldHint": true}

	tools := []localMCPTool{
		{
			Name: "wingthing_capabilities", Title: "Wingthing capabilities",
			Description: "Discover supported and installed agent CLIs plus the local runtime primitives available on this machine.",
			InputSchema: objectSchema(map[string]any{}), Annotations: readOnly,
		},
		{
			Name: "terminal_list", Title: "List persistent terminals",
			Description: "List live local Wingthing sessions with stable IDs, labels, process kind, agent, activity, and working directory.",
			InputSchema: objectSchema(map[string]any{}), Annotations: readOnly,
		},
		{
			Name: "terminal_read", Title: "Read terminal snapshot",
			Description: "Read the current ANSI snapshot of one persistent terminal. This is raw terminal state, not semantic agent state.",
			InputSchema: objectSchema(map[string]any{
				"session": stringProperty("Session ID, unique ID prefix, or label"),
			}, "session"), Annotations: readOnly,
		},
		{
			Name: "terminal_send", Title: "Send terminal input",
			Description: "Send text to a persistent PTY, optionally followed by Enter.",
			InputSchema: objectSchema(map[string]any{
				"session": stringProperty("Session ID, unique ID prefix, or label"),
				"input":   stringProperty("Text to send"),
				"enter":   map[string]any{"type": "boolean", "description": "Append Enter after the text", "default": false},
			}, "session", "input"), Annotations: mutating,
		},
		{
			Name: "terminal_wait", Title: "Wait for terminal output",
			Description: "Wait without polling until a terminal produces text or becomes idle.",
			InputSchema: objectSchema(map[string]any{
				"session":         stringProperty("Session ID, unique ID prefix, or label"),
				"contains":        stringProperty("Text to wait for; omit to wait for idle"),
				"idle_seconds":    map[string]any{"type": "number", "minimum": 0.2, "description": "Idle duration when contains is omitted", "default": 2},
				"timeout_seconds": map[string]any{"type": "number", "minimum": 0.1, "maximum": 3600, "description": "Maximum wait", "default": 30},
			}, "session"), Annotations: readOnly,
		},
		{
			Name: "agent_start", Title: "Start persistent agent terminal",
			Description: "Start a supported agent in a durable sandboxed PTY and return immediately with its session ID.",
			InputSchema: objectSchema(map[string]any{
				"agent":      stringProperty("Supported agent name"),
				"cwd":        stringProperty("Working directory; defaults to the MCP server's current directory"),
				"label":      stringProperty("Optional stable human-readable session label"),
				"unattended": map[string]any{"type": "boolean", "description": "Enable the agent's unattended permission mode", "default": false},
			}, "agent"), Annotations: modelCall,
		},
		{
			Name: "terminal_stop", Title: "Stop persistent terminal",
			Description: "Stop one Wingthing session and its process tree.",
			InputSchema: objectSchema(map[string]any{
				"session": stringProperty("Session ID, unique ID prefix, or label"),
			}, "session"),
			Annotations: map[string]any{"readOnlyHint": false, "destructiveHint": true, "openWorldHint": false},
		},
		{
			Name: "prompt_list", Title: "List saved prompts",
			Description: "List current named prompt assets with immutable revisions, variables, default agents, and working directories.",
			InputSchema: objectSchema(map[string]any{}), Annotations: readOnly,
		},
		{
			Name: "prompt_get", Title: "Get saved prompt",
			Description: "Read the current or an immutable historical revision of a named prompt asset.",
			InputSchema: objectSchema(map[string]any{
				"name":     stringProperty("Prompt asset name"),
				"revision": stringProperty("Optional immutable revision; current revision when omitted"),
			}, "name"), Annotations: readOnly,
		},
		{
			Name: "prompt_save", Title: "Save prompt",
			Description: "Create or atomically update a named prompt template while preserving a content-addressed historical revision.",
			InputSchema: objectSchema(map[string]any{
				"name":              stringProperty("Prompt asset name"),
				"description":       stringProperty("Human-readable purpose"),
				"template":          stringProperty("Go text/template prompt body; variables use {{.name}}"),
				"variables":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "default": []string{}},
				"agent":             stringProperty("Optional default supported agent"),
				"cwd":               stringProperty("Optional absolute default working directory"),
				"expected_revision": stringProperty("Reject the update unless this is still the current revision"),
			}, "name", "template"), Annotations: mutating,
		},
		{
			Name: "prompt_run", Title: "Run one prompt",
			Description: "Run either a raw prompt or a named immutable prompt revision through a supported agent, with sandboxing and durable task provenance.",
			InputSchema: objectSchema(map[string]any{
				"prompt":      stringProperty("Raw prompt to execute; mutually exclusive with prompt_name"),
				"prompt_name": stringProperty("Saved prompt asset; mutually exclusive with prompt"),
				"revision":    stringProperty("Optional immutable saved-prompt revision"),
				"variables":   map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}, "default": map[string]string{}},
				"agent":       stringProperty("Agent override; then prompt default; then Wingthing default"),
				"cwd":         stringProperty("Working-directory override; then prompt default; then MCP server cwd"),
			}), Annotations: modelCall,
		},
		{
			Name: "task_get", Title: "Get prompt task",
			Description: "Get structured status, output, error, timing, agent, and dependency data for a Wingthing task.",
			InputSchema: objectSchema(map[string]any{
				"task_id": stringProperty("Wingthing task ID"),
			}, "task_id"), Annotations: readOnly,
		},
		{
			Name: "prompt_loop", Title: "Run bounded prompt loop",
			Description: "Run a prompt sequentially for a bounded number of iterations. Each iteration receives the prior result and stops early when until_contains matches.",
			InputSchema: objectSchema(map[string]any{
				"prompt":         stringProperty("Base prompt for every iteration"),
				"agent":          stringProperty("Supported agent name; defaults to Wingthing configuration"),
				"cwd":            stringProperty("Working directory shared by every iteration"),
				"max_iterations": map[string]any{"type": "integer", "minimum": 1, "maximum": 12, "default": 3},
				"until_contains": stringProperty("Stop when an iteration's output contains this text"),
			}, "prompt"), Annotations: modelCall,
		},
		{
			Name: "swarm_run", Title: "Run agent swarm DAG",
			Description: "Run a bounded dependency graph of prompts. Independent nodes execute in parallel; completed dependency outputs are injected into downstream prompts.",
			InputSchema: objectSchema(map[string]any{
				"name":         stringProperty("Human-readable swarm purpose"),
				"cwd":          stringProperty("Working directory shared by every node"),
				"max_parallel": map[string]any{"type": "integer", "minimum": 1, "maximum": 4, "default": 2},
				"nodes": map[string]any{
					"type": "array", "minItems": 1, "maxItems": 16,
					"items": objectSchema(map[string]any{
						"id":         stringProperty("Unique logical node ID"),
						"prompt":     stringProperty("Prompt executed by this node"),
						"agent":      stringProperty("Supported agent name; defaults to Wingthing configuration"),
						"depends_on": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "default": []string{}},
					}, "id", "prompt"),
				},
			}, "nodes"), Annotations: modelCall,
		},
	}
	return tools
}

func localMCPToolResult(data map[string]any, isError bool) map[string]any {
	encoded, err := json.Marshal(data)
	if err != nil {
		encoded = []byte(`{"error":"could not encode tool result"}`)
		isError = true
	}
	return map[string]any{
		"content":           []map[string]any{{"type": "text", "text": string(encoded)}},
		"structuredContent": data,
		"isError":           isError,
	}
}

func (s *localMCPServer) callTool(ctx context.Context, name string, arguments json.RawMessage) (map[string]any, bool, *localMCPError) {
	var data map[string]any
	var err error
	isError := false
	switch name {
	case "wingthing_capabilities":
		data, err = s.toolCapabilities(arguments)
	case "terminal_list":
		data, err = s.toolTerminalList(ctx, arguments)
	case "terminal_read":
		data, err = s.toolTerminalRead(ctx, arguments)
	case "terminal_send":
		data, err = s.toolTerminalSend(ctx, arguments)
	case "terminal_wait":
		data, err = s.toolTerminalWait(ctx, arguments)
	case "agent_start":
		data, err = s.toolAgentStart(arguments)
	case "terminal_stop":
		data, err = s.toolTerminalStop(ctx, arguments)
	case "prompt_list":
		data, err = s.toolPromptList(arguments)
	case "prompt_get":
		data, err = s.toolPromptGet(arguments)
	case "prompt_save":
		data, err = s.toolPromptSave(arguments)
	case "prompt_run":
		data, isError, err = s.toolPromptRun(ctx, arguments)
	case "task_get":
		data, err = s.toolTaskGet(arguments)
	case "prompt_loop":
		data, isError, err = s.toolPromptLoop(ctx, arguments)
	case "swarm_run":
		data, isError, err = s.toolSwarmRun(ctx, arguments)
	default:
		return nil, false, &localMCPError{Code: -32602, Message: "unknown tool: " + name}
	}
	if err != nil {
		fmt.Fprintf(s.logs, "wingthing MCP %s: %v\n", name, err)
		return map[string]any{"error": err.Error()}, true, nil
	}
	return data, isError, nil
}

func (s *localMCPServer) toolCapabilities(arguments json.RawMessage) (map[string]any, error) {
	if err := requireEmptyObject(arguments); err != nil {
		return nil, err
	}
	agents := make([]map[string]any, 0)
	for _, definition := range agentpkg.Definitions() {
		profile := egg.Profile(definition.Name)
		path, lookErr := exec.LookPath(definition.Command)
		agents = append(agents, map[string]any{
			"name":                  definition.Name,
			"command":               definition.Command,
			"installed":             lookErr == nil,
			"path":                  path,
			"interactive":           true,
			"headless":              true,
			"resume":                definition.ResumeFlag != "",
			"resume_flag":           definition.ResumeFlag,
			"provider_substitution": definition.ProviderSubstitution,
			"release_canary":        definition.ReleaseCanary,
			"max_parallel":          definition.MaxParallel,
			"network_domains":       profile.Domains,
			"persistent_storage":    append(append([]string(nil), profile.WriteRegex...), profile.WriteDirs...),
		})
	}
	return map[string]any{
		"version": version,
		"agents":  agents,
		"objects": []string{"terminal", "prompt_asset", "task", "loop", "swarm"},
		"transports": map[string]any{
			"local": true,
			"ssh":   true,
			"web":   true,
		},
	}, nil
}

func (s *localMCPServer) toolTerminalList(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
	if err := requireEmptyObject(arguments); err != nil {
		return nil, err
	}
	sessions, err := discoverActiveSessions(ctx, s.cfg)
	if err != nil {
		return nil, err
	}
	return map[string]any{"sessions": sessions}, nil
}

func (s *localMCPServer) toolTerminalRead(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		Session string `json:"session"`
	}
	if err := decodeStrict(arguments, &args); err != nil || args.Session == "" {
		return nil, errors.New("session is required")
	}
	session, snapshot, err := readSessionSnapshot(ctx, s.cfg, args.Session)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"session":     session.ID,
		"label":       session.Name,
		"ansi":        string(snapshot),
		"base64":      base64.StdEncoding.EncodeToString(snapshot),
		"byte_length": len(snapshot),
	}, nil
}

func (s *localMCPServer) toolTerminalSend(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		Session string `json:"session"`
		Input   string `json:"input"`
		Enter   bool   `json:"enter"`
	}
	if err := decodeStrict(arguments, &args); err != nil || args.Session == "" {
		return nil, errors.New("session and input are required")
	}
	input := []byte(args.Input)
	if args.Enter {
		input = append(input, '\r')
	}
	session, err := sendSessionBytes(ctx, s.cfg, args.Session, input)
	if err != nil {
		return nil, err
	}
	return map[string]any{"session": session.ID, "bytes_sent": len(input)}, nil
}

func (s *localMCPServer) toolTerminalWait(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		Session        string  `json:"session"`
		Contains       string  `json:"contains"`
		IdleSeconds    float64 `json:"idle_seconds"`
		TimeoutSeconds float64 `json:"timeout_seconds"`
	}
	if err := decodeStrict(arguments, &args); err != nil || args.Session == "" {
		return nil, errors.New("session is required")
	}
	if args.TimeoutSeconds == 0 {
		args.TimeoutSeconds = 30
	}
	if args.TimeoutSeconds < 0.1 || args.TimeoutSeconds > 3600 {
		return nil, errors.New("timeout_seconds must be between 0.1 and 3600")
	}
	waitCtx, cancel := context.WithTimeout(ctx, durationSeconds(args.TimeoutSeconds))
	defer cancel()
	if args.Contains != "" {
		session, err := waitForSessionText(waitCtx, s.cfg, args.Session, args.Contains)
		if err != nil {
			return nil, err
		}
		return map[string]any{"session": session.ID, "condition": "contains", "value": args.Contains}, nil
	}
	if args.IdleSeconds == 0 {
		args.IdleSeconds = 2
	}
	if args.IdleSeconds < 0.2 {
		return nil, errors.New("idle_seconds must be at least 0.2")
	}
	session, ec, err := openLocalEgg(waitCtx, s.cfg, args.Session)
	if err != nil {
		return nil, err
	}
	defer ec.Close()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		status, statusErr := ec.Status(waitCtx)
		if statusErr != nil {
			return nil, statusErr
		}
		if float64(status.IdleSeconds) >= args.IdleSeconds {
			return map[string]any{"session": session.ID, "condition": "idle", "idle_seconds": status.IdleSeconds}, nil
		}
		select {
		case <-waitCtx.Done():
			return nil, waitCtx.Err()
		case <-ticker.C:
		}
	}
}

func (s *localMCPServer) toolAgentStart(arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		Agent      string `json:"agent"`
		CWD        string `json:"cwd"`
		Label      string `json:"label"`
		Unattended bool   `json:"unattended"`
	}
	if err := decodeStrict(arguments, &args); err != nil || args.Agent == "" {
		return nil, errors.New("agent is required")
	}
	if _, ok := agentpkg.LookupDefinition(args.Agent); !ok {
		return nil, fmt.Errorf("unsupported agent %q", args.Agent)
	}
	resolvedCWD, err := resolveWorkingDirectory(args.CWD)
	if err != nil {
		return nil, err
	}
	args.CWD = resolvedCWD
	eggCfg := egg.DiscoverEggConfig(args.CWD, nil)
	if args.Unattended {
		copyCfg := *eggCfg
		eggCfg = &copyCfg
		eggCfg.DangerouslySkipPermissions = true
	}
	sessionID := uuid.NewString()[:8]
	ec, err := spawnEgg(s.cfg, sessionID, args.Agent, eggCfg, 24, 80, args.CWD, false, false, false, EggIdentity{}, 0,
		spawnEggOpts{Label: args.Label, Kind: "agent"})
	if err != nil {
		return nil, err
	}
	_ = ec.Close()
	return map[string]any{"session": sessionID, "label": args.Label, "agent": args.Agent, "cwd": args.CWD}, nil
}

func (s *localMCPServer) toolTerminalStop(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		Session string `json:"session"`
	}
	if err := decodeStrict(arguments, &args); err != nil || args.Session == "" {
		return nil, errors.New("session is required")
	}
	session, ec, err := openLocalEgg(ctx, s.cfg, args.Session)
	if err != nil {
		return nil, err
	}
	defer ec.Close()
	if err := ec.Kill(ctx, session.ID); err != nil {
		return nil, err
	}
	return map[string]any{"session": session.ID, "status": "stopped"}, nil
}

func (s *localMCPServer) toolPromptList(arguments json.RawMessage) (map[string]any, error) {
	if err := requireEmptyObject(arguments); err != nil {
		return nil, err
	}
	assets, err := promptmgr.New(s.cfg.PromptsDir()).List()
	if err != nil {
		return nil, err
	}
	return map[string]any{"prompts": assets}, nil
}

func (s *localMCPServer) toolPromptGet(arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		Name     string `json:"name"`
		Revision string `json:"revision"`
	}
	if err := decodeStrict(arguments, &args); err != nil || args.Name == "" {
		return nil, errors.New("name is required")
	}
	asset, err := promptmgr.New(s.cfg.PromptsDir()).Get(args.Name, args.Revision)
	if err != nil {
		return nil, err
	}
	return map[string]any{"prompt": asset}, nil
}

func (s *localMCPServer) toolPromptSave(arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		Name             string   `json:"name"`
		Description      string   `json:"description"`
		Template         string   `json:"template"`
		Variables        []string `json:"variables"`
		Agent            string   `json:"agent"`
		CWD              string   `json:"cwd"`
		ExpectedRevision string   `json:"expected_revision"`
	}
	if err := decodeStrict(arguments, &args); err != nil || args.Name == "" || strings.TrimSpace(args.Template) == "" {
		return nil, errors.New("name and template are required")
	}
	if args.Agent != "" {
		if _, ok := agentpkg.LookupDefinition(args.Agent); !ok {
			return nil, fmt.Errorf("unsupported agent %q", args.Agent)
		}
	}
	asset, err := promptmgr.New(s.cfg.PromptsDir()).Save(promptmgr.Asset{
		Name: args.Name, Description: args.Description, Template: args.Template,
		Variables: args.Variables, Agent: args.Agent, CWD: args.CWD,
	}, args.ExpectedRevision)
	if err != nil {
		return nil, err
	}
	return map[string]any{"prompt": asset}, nil
}

func (s *localMCPServer) toolPromptRun(ctx context.Context, arguments json.RawMessage) (map[string]any, bool, error) {
	var args struct {
		Prompt     string            `json:"prompt"`
		PromptName string            `json:"prompt_name"`
		Revision   string            `json:"revision"`
		Variables  map[string]string `json:"variables"`
		Agent      string            `json:"agent"`
		CWD        string            `json:"cwd"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, false, err
	}
	hasRaw := strings.TrimSpace(args.Prompt) != ""
	hasSaved := args.PromptName != ""
	if hasRaw == hasSaved {
		return nil, false, errors.New("provide exactly one of prompt or prompt_name")
	}
	promptName := ""
	promptRevision := ""
	if hasSaved {
		asset, err := promptmgr.New(s.cfg.PromptsDir()).Get(args.PromptName, args.Revision)
		if err != nil {
			return nil, false, err
		}
		args.Prompt, err = promptmgr.Render(asset, args.Variables)
		if err != nil {
			return nil, false, err
		}
		promptName = asset.Name
		promptRevision = asset.Revision
		if args.Agent == "" {
			args.Agent = asset.Agent
		}
		if args.CWD == "" {
			args.CWD = asset.CWD
		}
	} else if args.Revision != "" || len(args.Variables) > 0 {
		return nil, false, errors.New("revision and variables require prompt_name")
	}
	resolvedCWD, err := resolveWorkingDirectory(args.CWD)
	if err != nil {
		return nil, false, err
	}
	args.CWD = resolvedCWD
	task, runErr := s.executePrompt(ctx, args.Prompt, args.Agent, args.CWD, promptName, promptRevision, nil, nil)
	if task == nil {
		return nil, true, runErr
	}
	return taskData(task), runErr != nil, nil
}

func (s *localMCPServer) toolTaskGet(arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		TaskID string `json:"task_id"`
	}
	if err := decodeStrict(arguments, &args); err != nil || args.TaskID == "" {
		return nil, errors.New("task_id is required")
	}
	taskStore, err := store.Open(s.cfg.DBPath())
	if err != nil {
		return nil, err
	}
	defer taskStore.Close()
	task, err := taskStore.GetTask(args.TaskID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, fmt.Errorf("task %q not found", args.TaskID)
	}
	return taskData(task), nil
}

func (s *localMCPServer) executePrompt(ctx context.Context, prompt, agentName, cwd, promptName, promptRevision string, parentID, dependsOn *string) (*store.Task, error) {
	if agentName == "" {
		agentName = s.cfg.DefaultAgent
	}
	if _, ok := agentpkg.LookupDefinition(agentName); !ok {
		return nil, fmt.Errorf("unsupported agent %q", agentName)
	}
	taskStore, err := store.Open(s.cfg.DBPath())
	if err != nil {
		return nil, err
	}
	defer taskStore.Close()
	task := &store.Task{
		ID: genTaskID(), Type: "prompt", What: prompt, Agent: agentName,
		RunAt: time.Now().UTC(), ParentID: parentID, DependsOn: dependsOn, CWD: cwd,
		PromptName: promptName, PromptRevision: promptRevision,
	}
	if err := taskStore.CreateTask(task); err != nil {
		return nil, err
	}
	runErr := runTaskTo(ctx, s.cfg, taskStore, task, io.Discard)
	stored, getErr := taskStore.GetTask(task.ID)
	if getErr != nil {
		return task, errors.Join(runErr, getErr)
	}
	return stored, runErr
}

type promptLoopArgs struct {
	Prompt        string `json:"prompt"`
	Agent         string `json:"agent"`
	CWD           string `json:"cwd"`
	MaxIterations int    `json:"max_iterations"`
	UntilContains string `json:"until_contains"`
}

func (s *localMCPServer) toolPromptLoop(ctx context.Context, arguments json.RawMessage) (map[string]any, bool, error) {
	var args promptLoopArgs
	if err := decodeStrict(arguments, &args); err != nil || strings.TrimSpace(args.Prompt) == "" {
		return nil, false, errors.New("prompt is required")
	}
	if args.MaxIterations == 0 {
		args.MaxIterations = 3
	}
	if args.MaxIterations < 1 || args.MaxIterations > 12 {
		return nil, false, errors.New("max_iterations must be between 1 and 12")
	}
	if args.Agent == "" {
		args.Agent = s.cfg.DefaultAgent
	}
	if _, ok := agentpkg.LookupDefinition(args.Agent); !ok {
		return nil, false, fmt.Errorf("unsupported agent %q", args.Agent)
	}
	resolvedCWD, err := resolveWorkingDirectory(args.CWD)
	if err != nil {
		return nil, false, err
	}
	args.CWD = resolvedCWD

	root, rootStore, err := s.createMetaTask("loop", args.Prompt, args.Agent, args.CWD)
	if err != nil {
		return nil, false, err
	}
	rootStore.Close()
	results := make([]map[string]any, 0, args.MaxIterations)
	var previousTaskID string
	failed := false
	stopReason := "max_iterations"
	for iteration := 1; iteration <= args.MaxIterations; iteration++ {
		select {
		case <-ctx.Done():
			failed = true
			stopReason = "cancelled"
			iteration = args.MaxIterations
			continue
		default:
		}
		var dependsJSON *string
		if previousTaskID != "" {
			encoded, _ := json.Marshal([]string{previousTaskID})
			value := string(encoded)
			dependsJSON = &value
		}
		task, runErr := s.executePrompt(ctx, args.Prompt, args.Agent, args.CWD, "", "", &root.ID, dependsJSON)
		if task != nil {
			entry := taskData(task)
			entry["iteration"] = iteration
			results = append(results, entry)
			previousTaskID = task.ID
		}
		if runErr != nil || task == nil || task.Status != "done" {
			failed = true
			stopReason = "failed"
			break
		}
		if args.UntilContains != "" && task.Output != nil && strings.Contains(*task.Output, args.UntilContains) {
			stopReason = "condition_met"
			break
		}
	}
	status := "done"
	if failed {
		status = "failed"
	}
	data := map[string]any{
		"loop_id": root.ID, "status": status, "stop_reason": stopReason,
		"iterations": results, "until_contains": args.UntilContains,
	}
	if err := s.finishMetaTask(root.ID, status, data); err != nil {
		return nil, true, err
	}
	return data, failed, nil
}

type swarmNodeSpec struct {
	ID        string   `json:"id"`
	Prompt    string   `json:"prompt"`
	Agent     string   `json:"agent"`
	DependsOn []string `json:"depends_on"`
}

type swarmRunArgs struct {
	Name        string          `json:"name"`
	CWD         string          `json:"cwd"`
	MaxParallel int             `json:"max_parallel"`
	Nodes       []swarmNodeSpec `json:"nodes"`
}

type swarmNodeResult struct {
	logicalID string
	task      *store.Task
	err       error
}

func (s *localMCPServer) toolSwarmRun(ctx context.Context, arguments json.RawMessage) (map[string]any, bool, error) {
	var args swarmRunArgs
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, false, err
	}
	if args.MaxParallel == 0 {
		args.MaxParallel = 2
	}
	if args.MaxParallel < 1 || args.MaxParallel > 4 {
		return nil, false, errors.New("max_parallel must be between 1 and 4")
	}
	if len(args.Nodes) < 1 || len(args.Nodes) > 16 {
		return nil, false, errors.New("nodes must contain between 1 and 16 entries")
	}
	if err := validateSwarm(args.Nodes, s.cfg.DefaultAgent); err != nil {
		return nil, false, err
	}
	if args.Name == "" {
		args.Name = "agent swarm"
	}
	resolvedCWD, err := resolveWorkingDirectory(args.CWD)
	if err != nil {
		return nil, false, err
	}
	args.CWD = resolvedCWD

	root, rootStore, err := s.createMetaTask("swarm", args.Name, s.cfg.DefaultAgent, args.CWD)
	if err != nil {
		return nil, false, err
	}
	defer rootStore.Close()

	taskIDs := make(map[string]string, len(args.Nodes))
	byID := make(map[string]swarmNodeSpec, len(args.Nodes))
	for _, node := range args.Nodes {
		taskIDs[node.ID] = genTaskID()
		byID[node.ID] = node
	}
	for _, node := range args.Nodes {
		agentName := node.Agent
		if agentName == "" {
			agentName = s.cfg.DefaultAgent
		}
		depends := make([]string, 0, len(node.DependsOn))
		for _, dep := range node.DependsOn {
			depends = append(depends, taskIDs[dep])
		}
		var dependsJSON *string
		if len(depends) > 0 {
			encoded, _ := json.Marshal(depends)
			value := string(encoded)
			dependsJSON = &value
		}
		parentID := root.ID
		task := &store.Task{
			ID: taskIDs[node.ID], Type: "prompt", What: node.Prompt, Agent: agentName,
			RunAt: time.Now().UTC(), ParentID: &parentID, DependsOn: dependsJSON, CWD: args.CWD,
		}
		if err := rootStore.CreateTask(task); err != nil {
			return nil, true, err
		}
	}

	state := make(map[string]string, len(args.Nodes))
	results := make(map[string]*store.Task, len(args.Nodes))
	agentSemaphores := make(map[string]chan struct{})
	for _, definition := range agentpkg.Definitions() {
		if definition.MaxParallel > 0 {
			agentSemaphores[definition.Name] = make(chan struct{}, definition.MaxParallel)
		}
	}
	for len(state) < len(args.Nodes) {
		var ready []swarmNodeSpec
		for _, node := range args.Nodes {
			if state[node.ID] != "" {
				continue
			}
			allTerminal := true
			dependencyFailed := false
			for _, dep := range node.DependsOn {
				if state[dep] == "" {
					allTerminal = false
					break
				}
				if state[dep] != "done" {
					dependencyFailed = true
				}
			}
			if !allTerminal {
				continue
			}
			if dependencyFailed {
				message := "one or more dependencies failed"
				_ = rootStore.SetTaskError(taskIDs[node.ID], message)
				skipped, _ := rootStore.GetTask(taskIDs[node.ID])
				results[node.ID] = skipped
				state[node.ID] = "blocked"
				continue
			}
			ready = append(ready, node)
		}
		if len(ready) == 0 {
			break
		}

		resultCh := make(chan swarmNodeResult, len(ready))
		semaphore := make(chan struct{}, args.MaxParallel)
		var wg sync.WaitGroup
		for _, node := range ready {
			node := node
			state[node.ID] = "running"
			wg.Add(1)
			go func() {
				defer wg.Done()
				semaphore <- struct{}{}
				defer func() { <-semaphore }()
				agentName := node.Agent
				if agentName == "" {
					agentName = s.cfg.DefaultAgent
				}
				if agentSemaphore := agentSemaphores[agentName]; agentSemaphore != nil {
					agentSemaphore <- struct{}{}
					defer func() { <-agentSemaphore }()
				}
				taskStore, openErr := store.Open(s.cfg.DBPath())
				if openErr != nil {
					resultCh <- swarmNodeResult{logicalID: node.ID, err: openErr}
					return
				}
				defer taskStore.Close()
				task, getErr := taskStore.GetTask(taskIDs[node.ID])
				if getErr == nil && task != nil {
					getErr = runTaskTo(ctx, s.cfg, taskStore, task, io.Discard)
					task, _ = taskStore.GetTask(task.ID)
				}
				resultCh <- swarmNodeResult{logicalID: node.ID, task: task, err: getErr}
			}()
		}
		wg.Wait()
		close(resultCh)
		for result := range resultCh {
			results[result.logicalID] = result.task
			if result.err != nil || result.task == nil || result.task.Status != "done" {
				state[result.logicalID] = "failed"
			} else {
				state[result.logicalID] = "done"
			}
		}
	}

	failed := false
	nodeData := make([]map[string]any, 0, len(args.Nodes))
	for _, node := range args.Nodes {
		entry := map[string]any{
			"id": node.ID, "task_id": taskIDs[node.ID], "status": state[node.ID],
			"depends_on": node.DependsOn,
		}
		if task := results[node.ID]; task != nil {
			entry["task"] = taskData(task)
		}
		if state[node.ID] != "done" {
			failed = true
		}
		nodeData = append(nodeData, entry)
	}
	status := "done"
	if failed {
		status = "failed"
	}
	data := map[string]any{"swarm_id": root.ID, "name": args.Name, "status": status, "nodes": nodeData}
	if err := s.finishMetaTask(root.ID, status, data); err != nil {
		return nil, true, err
	}
	return data, failed, nil
}

func validateSwarm(nodes []swarmNodeSpec, defaultAgent string) error {
	byID := make(map[string]swarmNodeSpec, len(nodes))
	for _, node := range nodes {
		if err := validateSessionName(node.ID); err != nil || node.ID == "" {
			return fmt.Errorf("invalid swarm node ID %q", node.ID)
		}
		if _, exists := byID[node.ID]; exists {
			return fmt.Errorf("duplicate swarm node ID %q", node.ID)
		}
		if strings.TrimSpace(node.Prompt) == "" {
			return fmt.Errorf("swarm node %q has an empty prompt", node.ID)
		}
		agentName := node.Agent
		if agentName == "" {
			agentName = defaultAgent
		}
		if _, ok := agentpkg.LookupDefinition(agentName); !ok {
			return fmt.Errorf("swarm node %q uses unsupported agent %q", node.ID, agentName)
		}
		byID[node.ID] = node
	}
	for _, node := range nodes {
		for _, dependency := range node.DependsOn {
			if dependency == node.ID {
				return fmt.Errorf("swarm node %q depends on itself", node.ID)
			}
			if _, exists := byID[dependency]; !exists {
				return fmt.Errorf("swarm node %q depends on unknown node %q", node.ID, dependency)
			}
		}
	}
	visiting := make(map[string]bool)
	visited := make(map[string]bool)
	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("swarm dependency cycle includes %q", id)
		}
		if visited[id] {
			return nil
		}
		visiting[id] = true
		for _, dependency := range byID[id].DependsOn {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		visiting[id] = false
		visited[id] = true
		return nil
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

func (s *localMCPServer) createMetaTask(kind, what, agentName, cwd string) (*store.Task, *store.Store, error) {
	taskStore, err := store.Open(s.cfg.DBPath())
	if err != nil {
		return nil, nil, err
	}
	task := &store.Task{
		ID: genTaskID(), Type: kind, What: what, Agent: agentName,
		RunAt: time.Now().UTC(), Status: "pending", CWD: cwd,
	}
	if err := taskStore.CreateTask(task); err != nil {
		taskStore.Close()
		return nil, nil, err
	}
	if err := taskStore.UpdateTaskStatus(task.ID, "running"); err != nil {
		taskStore.Close()
		return nil, nil, err
	}
	return task, taskStore, nil
}

func (s *localMCPServer) finishMetaTask(taskID, status string, data map[string]any) error {
	taskStore, err := store.Open(s.cfg.DBPath())
	if err != nil {
		return err
	}
	defer taskStore.Close()
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if status == "failed" {
		if err := taskStore.SetTaskError(taskID, "one or more child tasks failed"); err != nil {
			return err
		}
		return taskStore.SetTaskOutput(taskID, string(encoded))
	}
	if err := taskStore.SetTaskOutput(taskID, string(encoded)); err != nil {
		return err
	}
	return taskStore.UpdateTaskStatus(taskID, "done")
}

func taskData(task *store.Task) map[string]any {
	data := map[string]any{
		"id": task.ID, "type": task.Type, "prompt": task.What,
		"agent": task.Agent, "isolation": task.Isolation, "status": task.Status,
		"run_at":      task.RunAt.UTC().Format(time.RFC3339),
		"created_at":  task.CreatedAt.UTC().Format(time.RFC3339),
		"retry_count": task.RetryCount, "max_retries": task.MaxRetries,
	}
	if task.CWD != "" {
		data["cwd"] = task.CWD
	}
	if task.PromptName != "" {
		data["prompt_name"] = task.PromptName
		data["prompt_revision"] = task.PromptRevision
	}
	if task.ParentID != nil {
		data["parent_id"] = *task.ParentID
	}
	if task.DependsOn != nil {
		var dependencies []string
		if json.Unmarshal([]byte(*task.DependsOn), &dependencies) == nil {
			data["depends_on"] = dependencies
		}
	}
	if task.StartedAt != nil {
		data["started_at"] = task.StartedAt.UTC().Format(time.RFC3339)
	}
	if task.FinishedAt != nil {
		data["finished_at"] = task.FinishedAt.UTC().Format(time.RFC3339)
	}
	if task.Output != nil {
		data["output"] = *task.Output
	}
	if task.Error != nil {
		data["error"] = *task.Error
	}
	return data
}

func resolveWorkingDirectory(cwd string) (string, error) {
	var err error
	if cwd == "" {
		cwd, err = os.Getwd()
	} else {
		cwd, err = filepath.Abs(cwd)
	}
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	info, statErr := os.Stat(cwd)
	if statErr != nil {
		return "", fmt.Errorf("working directory %q: %w", cwd, statErr)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("working directory %q is not a directory", cwd)
	}
	return cwd, nil
}

func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("expected one JSON object")
	}
	return nil
}

func requireEmptyObject(data []byte) error {
	var value map[string]any
	if err := decodeStrict(data, &value); err != nil {
		return err
	}
	if len(value) != 0 {
		return errors.New("tool accepts no arguments")
	}
	return nil
}

func durationSeconds(seconds float64) time.Duration {
	return time.Duration(seconds * float64(time.Second))
}
