package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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
	mcppkg "github.com/ehrlich-b/wingthing/internal/mcp"
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
	var clientName string
	var unsandboxed bool
	stdioCmd := &cobra.Command{
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
			principal := strings.TrimSpace(clientName)
			if principal == "" {
				principal = strings.TrimSpace(os.Getenv("WT_MCP_CLIENT"))
			}
			explicitClient := principal != ""
			if principal == "" {
				principal = "default"
			}
			if err := validateSessionName(principal); err != nil {
				return fmt.Errorf("invalid MCP client name: %w", err)
			}
			clientsConfig, err := loadLocalMCPClientsConfig(cfg)
			if err != nil {
				return err
			}
			if clientsConfig.RequireClient && !explicitClient {
				return errors.New("clients.yaml requires an explicit MCP client; pass --client or WT_MCP_CLIENT")
			}
			clientConfig, configured := clientsConfig.Clients[principal]
			if clientsConfig.RequireClient && !configured {
				return fmt.Errorf("MCP client %q is not configured in clients.yaml", principal)
			}
			// Once an operator defines any clients, every principal must have an
			// explicit entry. An omitted --client resolves to the literal
			// "default" entry rather than acquiring the nil-grants full-access
			// behavior intended only for installations without clients.yaml.
			if len(clientsConfig.Clients) > 0 && !configured {
				return fmt.Errorf("MCP client %q is not configured in clients.yaml", principal)
			}
			clientID := principal
			owner := clientID
			if configured && strings.TrimSpace(clientConfig.Owner) != "" {
				owner = strings.TrimSpace(clientConfig.Owner)
				if err := validateSessionName(owner); err != nil {
					return fmt.Errorf("invalid MCP owner name: %w", err)
				}
			}
			server := &localMCPServer{
				cfg: cfg, in: os.Stdin, out: os.Stdout, logs: os.Stderr,
				principal: owner, actor: clientID, unsandboxed: unsandboxed,
			}
			if configured {
				server.grants = grantSet(clientConfig.Grants)
				server.maxSessions = clientConfig.Bounds.MaxSessions
				server.maxSpawnsPerHour = clientConfig.Bounds.MaxSpawnsPerHour
			}
			return server.serve(cmd.Context())
		},
	}
	stdioCmd.Flags().StringVar(&clientName, "client", "", "local MCP principal name (or WT_MCP_CLIENT)")
	stdioCmd.Flags().BoolVar(&unsandboxed, "unsandboxed", false, "trust an outer VM/container boundary for all sessions and prompt runs")
	cmd.AddCommand(stdioCmd)
	return cmd
}

type localMCPServer struct {
	cfg               *config.Config
	in                io.Reader
	out               io.Writer
	logs              io.Writer
	principal         string
	unsandboxed       bool
	grants            map[string]bool
	maxSessions       int
	maxSpawnsPerHour  int
	spawnMu           sync.Mutex
	spawnTimes        []time.Time
	identity          EggIdentity
	actor             string
	allowedPaths      []string
	enforcePathBounds bool
	runAgentTask      func(context.Context, *config.Config, *store.Store, *store.Task, taskRunOptions) error
}

type activeMCPAgentRun struct {
	principal string
	cancel    context.CancelFunc
	done      <-chan struct{}
}

var activeMCPAgentRuns sync.Map

func (s *localMCPServer) clientPrincipal() string {
	if s.principal == "" {
		return "default"
	}
	return s.principal
}

func (s *localMCPServer) clientActor() string {
	if strings.TrimSpace(s.actor) != "" {
		return strings.TrimSpace(s.actor)
	}
	return s.clientPrincipal()
}

func (s *localMCPServer) toolAllowed(name string) bool {
	if s.grants == nil {
		return true
	}
	grant, known := localMCPToolGrants[name]
	return known && s.grants[grant]
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
	var calls sync.WaitGroup
	var encodeMu sync.Mutex
	var encodeErr error
	writeResponse := func(response localMCPResponse) {
		encodeMu.Lock()
		defer encodeMu.Unlock()
		if encodeErr == nil {
			encodeErr = encoder.Encode(response)
		}
	}
	dispatch := func(request localMCPRequest) {
		response, respond := s.handle(ctx, request)
		if respond {
			writeResponse(response)
		}
	}
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		var request localMCPRequest
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			writeResponse(localMCPResponse{
				JSONRPC: "2.0",
				Error:   &localMCPError{Code: -32700, Message: "parse error"},
			})
			continue
		}
		// Tool calls may wait for terminals or agent runs. Dispatching them
		// independently lets the same stdio client send agent_stop, steering,
		// and status calls while another request is waiting.
		if request.Method == "tools/call" {
			calls.Add(1)
			go func() {
				defer calls.Done()
				dispatch(request)
			}()
			continue
		}
		dispatch(request)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read MCP request: %w", err)
	}
	calls.Wait()
	return encodeErr
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
				"name":      "wingthing-local",
				"version":   version,
				"principal": s.clientPrincipal(),
				"actor":     s.clientActor(),
			},
			"instructions": s.mcpInstructions(),
		}
	case "notifications/initialized", "notifications/cancelled":
		return localMCPResponse{}, false
	case "ping":
		response.Result = map[string]any{}
	case "tools/list":
		tools := localMCPTools()
		if s.grants != nil {
			filtered := tools[:0]
			for _, tool := range tools {
				if s.toolAllowed(tool.Name) {
					filtered = append(filtered, tool)
				}
			}
			tools = filtered
		}
		response.Result = map[string]any{"tools": tools}
	case "tools/call":
		var call struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := decodeStrict(request.Params, &call); err != nil {
			response.Error = &localMCPError{Code: -32602, Message: "invalid tools/call params: " + err.Error()}
			break
		}
		if call.Name == "" {
			response.Error = &localMCPError{Code: -32602, Message: "invalid tools/call params: name is required"}
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

func (s *localMCPServer) mcpInstructions() string {
	base := "Wingthing is a local-first runtime. Use terminal tools for persistent PTYs, agent_run for supervised semantic work, prompt_loop for bounded iteration, and swarm_run for a dependency DAG."
	if s.unsandboxed {
		return base + " This server trusts an outer VM/container boundary: spawned processes have the full authority of the local OS user."
	}
	return base
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
			Name: "message_send", Title: "Send owner message",
			Description: "Send a durable message to another Codex, Claude, or other client authenticated as the same Wingthing owner.",
			InputSchema: objectSchema(map[string]any{
				"content":  stringProperty("Message body; stored owner-scoped and omitted from audit logs"),
				"channel":  stringProperty("Conversation channel; defaults to factory"),
				"to_actor": stringProperty("Optional recipient actor ID; empty broadcasts to the owner's other clients"),
				"kind": map[string]any{
					"type": "string", "enum": []string{"message", "status", "question", "answer", "evidence", "error"},
					"default": "message", "description": "Structured message kind",
				},
				"reply_to": stringProperty("Optional owner-scoped message ID being answered"),
				"ttl_seconds": map[string]any{
					"type": "integer", "minimum": 60, "maximum": 604800, "default": 86400,
					"description": "Retention time from one minute through seven days",
				},
			}, "content"), Annotations: mutating,
		},
		{
			Name: "message_list", Title: "List owner messages",
			Description: "List durable messages visible to this authenticated actor in ascending order, with a cursor for the next call.",
			InputSchema: objectSchema(map[string]any{
				"channel":      stringProperty("Conversation channel; defaults to factory"),
				"after_id":     stringProperty("Return messages after this owner-scoped message ID"),
				"limit":        map[string]any{"type": "integer", "minimum": 1, "maximum": 20, "default": 20},
				"include_sent": map[string]any{"type": "boolean", "default": false, "description": "Include messages sent by this actor"},
			}), Annotations: readOnly,
		},
		{
			Name: "message_wait", Title: "Wait for owner message",
			Description: "Wait until another same-owner client sends a visible message after the supplied cursor, or until the bounded timeout expires.",
			InputSchema: objectSchema(map[string]any{
				"channel":         stringProperty("Conversation channel; defaults to factory"),
				"after_id":        stringProperty("Wait for messages after this owner-scoped message ID"),
				"limit":           map[string]any{"type": "integer", "minimum": 1, "maximum": 20, "default": 20},
				"timeout_seconds": map[string]any{"type": "number", "minimum": 0.1, "maximum": 3600, "default": 30},
			}), Annotations: readOnly,
		},
		{
			Name: "sandbox_explain", Title: "Explain sandbox policy",
			Description: "Resolve the effective sandbox policy for an agent: mounts, denied paths, network domains, whether the network boundary is actually enforced on this platform, and every hole drilled automatically for the agent with the reason for it.",
			InputSchema: objectSchema(map[string]any{
				"agent":             stringProperty("Agent name; omit for a plain shell session"),
				"config":            stringProperty("Path to an egg.yaml; discovered from the working directory when omitted"),
				"cwd":               stringProperty("Directory to discover egg.yaml in; defaults to the MCP server's current directory"),
				"provider_base_url": stringProperty("Optional provider URL whose exact host becomes the derived egress domain"),
			}), Annotations: readOnly,
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
			Name: "terminal_start", Title: "Start persistent terminal",
			Description: "Start a durable shell or command terminal under the MCP server's declared isolation mode and return immediately with its session ID.",
			InputSchema: objectSchema(map[string]any{
				"command": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "default": []string{}, "description": "Executable and arguments; omit to start $SHELL"},
				"cwd":     stringProperty("Working directory; defaults to the MCP server's current directory"),
				"label":   stringProperty("Optional stable human-readable session label"),
			}), Annotations: mutating,
		},
		{
			Name: "agent_start", Title: "Start persistent agent terminal",
			Description: "Start a supported agent in a durable PTY under the MCP server's declared isolation mode and return immediately with its session ID.",
			InputSchema: objectSchema(map[string]any{
				"agent":      stringProperty("Supported agent name"),
				"model":      stringProperty("Provider model name, such as opus or gpt-5.6-terra"),
				"cwd":        stringProperty("Working directory; defaults to the MCP server's current directory"),
				"label":      stringProperty("Optional stable human-readable session label"),
				"unattended": map[string]any{"type": "boolean", "description": "Enable the agent's unattended permission mode", "default": false},
				"args": map[string]any{
					"type": "array", "items": map[string]any{"type": "string"}, "default": []string{},
					"description": "Extra arguments passed to the agent CLI verbatim, after Wingthing's own flags. Use the agent's native syntax, for example [\"--model\",\"sonnet\"] for claude or [\"-m\",\"gpt-5.6-terra\"] for codex.",
				},
			}, "agent"), Annotations: modelCall,
		},
		{
			Name: "agent_run", Title: "Run agent task",
			Description: "Start a supervised headless agent run and return immediately with an owner-scoped run ID. Use agent_wait and agent_result instead of reading terminal ANSI.",
			InputSchema: objectSchema(map[string]any{
				"prompt":          stringProperty("Task for the agent"),
				"agent":           stringProperty("Supported agent name, such as codex or claude"),
				"model":           stringProperty("Provider model name, such as gpt-5.6-terra or opus"),
				"cwd":             stringProperty("Working directory; defaults to the MCP server's current directory"),
				"label":           stringProperty("Short human-readable purpose recorded with the run"),
				"timeout_seconds": map[string]any{"type": "integer", "minimum": 10, "maximum": 7200, "default": 900, "description": "Provider process deadline"},
			}, "prompt", "agent"), Annotations: modelCall,
		},
		{
			Name: "agent_status", Title: "Get agent run status",
			Description: "Read bounded lifecycle metadata for one run owned by this MCP principal.",
			InputSchema: objectSchema(map[string]any{
				"run_id": stringProperty("Wingthing agent run ID"),
			}, "run_id"), Annotations: readOnly,
		},
		{
			Name: "agent_wait", Title: "Wait for agent run",
			Description: "Wait without polling until an agent run reaches a terminal state or the requested timeout expires.",
			InputSchema: objectSchema(map[string]any{
				"run_id":          stringProperty("Wingthing agent run ID"),
				"timeout_seconds": map[string]any{"type": "number", "minimum": 0.1, "maximum": 3600, "default": 30},
			}, "run_id"), Annotations: readOnly,
		},
		{
			Name: "agent_result", Title: "Read agent result",
			Description: "Read the final semantic output or error for one completed run, with an explicit response bound.",
			InputSchema: objectSchema(map[string]any{
				"run_id":    stringProperty("Wingthing agent run ID"),
				"max_chars": map[string]any{"type": "integer", "minimum": 1, "maximum": 200000, "default": 50000},
			}, "run_id"), Annotations: readOnly,
		},
		{
			Name: "agent_events", Title: "Read agent run events",
			Description: "Read bounded lifecycle events for one owned run.",
			InputSchema: objectSchema(map[string]any{
				"run_id": stringProperty("Wingthing agent run ID"),
				"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 200, "default": 50},
			}, "run_id"), Annotations: readOnly,
		},
		{
			Name: "agent_steer", Title: "Steer agent run",
			Description: "Queue an owner-scoped follow-up run that receives the prior request and result plus new direction.",
			InputSchema: objectSchema(map[string]any{
				"run_id": stringProperty("Run to follow up"),
				"prompt": stringProperty("New direction for the agent"),
				"model":  stringProperty("Optional model override for the follow-up"),
			}, "run_id", "prompt"), Annotations: modelCall,
		},
		{
			Name: "agent_stop", Title: "Stop agent run",
			Description: "Cancel an active owner-scoped run and its provider process tree.",
			InputSchema: objectSchema(map[string]any{
				"run_id": stringProperty("Wingthing agent run ID"),
			}, "run_id"),
			Annotations: map[string]any{"readOnlyHint": false, "destructiveHint": true, "openWorldHint": false},
		},
		{
			Name: "terminal_rename", Title: "Rename persistent terminal",
			Description: "Assign a stable human-readable label to a terminal owned by this MCP principal.",
			InputSchema: objectSchema(map[string]any{
				"session": stringProperty("Session ID, unique ID prefix, or current label"),
				"name":    stringProperty("New session label"),
			}, "session", "name"), Annotations: mutating,
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
			Description: "Run either a raw prompt or a named immutable prompt revision through a supported agent, under the MCP server's declared isolation mode and with durable task provenance.",
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

var roostControlToolNames = map[string]bool{
	"wingthing_capabilities": true,
	"message_send":           true,
	"message_list":           true,
	"message_wait":           true,
	"sandbox_explain":        true,
	"terminal_list":          true,
	"terminal_read":          true,
	"terminal_send":          true,
	"terminal_wait":          true,
	"terminal_start":         true,
	"agent_start":            true,
	"agent_run":              true,
	"agent_status":           true,
	"agent_wait":             true,
	"agent_result":           true,
	"agent_events":           true,
	"agent_steer":            true,
	"agent_stop":             true,
	"terminal_rename":        true,
	"terminal_stop":          true,
}

// roostNativeMCPTools adapts the local typed control surface to authenticated
// Streamable HTTP MCP. The request principal is supplied by the roost after
// bearer-token verification and never accepted from tool arguments.
func roostNativeMCPTools(cfg *config.Config, sharedHost bool) []mcppkg.NativeTool {
	var tools []mcppkg.NativeTool
	for _, localTool := range localMCPTools() {
		if !roostControlToolNames[localTool.Name] {
			continue
		}
		tool := localTool
		tools = append(tools, mcppkg.NativeTool{
			Name: tool.Name, Title: tool.Title, Description: tool.Description,
			InputSchema: tool.InputSchema, Annotations: tool.Annotations,
			Call: func(ctx context.Context, principal mcppkg.Principal, arguments json.RawMessage) (map[string]any, bool, error) {
				if principal.UserID == "" {
					return nil, true, errors.New("authenticated user identity is required")
				}
				paths, err := roostMCPPaths(cfg, principal.Email)
				if err != nil {
					return nil, true, err
				}
				server := &localMCPServer{
					cfg: cfg, logs: os.Stderr,
					principal:         roostSessionPrincipal(principal.UserID),
					actor:             principal.ClientID,
					allowedPaths:      paths,
					enforcePathBounds: true,
					identity: EggIdentity{
						UserID: principal.UserID, Email: principal.Email, SharedHost: sharedHost,
						AllowedPaths: append([]string(nil), paths...), SealedFS: sharedHost,
					},
				}
				data, isError, protocolErr := server.callTool(ctx, tool.Name, arguments)
				if protocolErr != nil {
					return map[string]any{"error": protocolErr.Message}, true, nil
				}
				return data, isError, nil
			},
		})
	}
	return tools
}

func roostSessionPrincipal(userID string) string {
	digest := sha256.Sum256([]byte(userID))
	return "user-" + hex.EncodeToString(digest[:10])
}

func roostMCPPaths(cfg *config.Config, email string) ([]string, error) {
	wingCfg, err := config.LoadWingConfig(cfg.Dir)
	if err != nil {
		return nil, fmt.Errorf("load roost path policy: %w", err)
	}
	home, _ := os.UserHomeDir()
	return canonicalPaths(pathsForRequest(wingCfg.Paths, email, "member", home)), nil
}

func canonicalPaths(paths []string) []string {
	canonical := make([]string, 0, len(paths))
	for _, path := range paths {
		cleaned := filepath.Clean(path)
		if resolved, err := filepath.EvalSymlinks(cleaned); err == nil {
			cleaned = resolved
		}
		canonical = append(canonical, cleaned)
	}
	return canonical
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
	defer func() {
		decision := "allowed"
		if err != nil || isError {
			decision = "error"
		}
		if auditErr := s.auditToolCall(name, arguments, data, decision); auditErr != nil {
			fmt.Fprintf(s.logs, "wingthing MCP audit: %v\n", auditErr)
		}
	}()
	if !s.toolAllowed(name) {
		err = fmt.Errorf("principal %q lacks grant %q", s.clientPrincipal(), localMCPToolGrants[name])
		return map[string]any{"error": err.Error()}, true, nil
	}
	switch name {
	case "wingthing_capabilities":
		data, err = s.toolCapabilities(arguments)
	case "message_send":
		data, err = s.toolMessageSend(arguments)
	case "message_list":
		data, err = s.toolMessageList(arguments)
	case "message_wait":
		data, err = s.toolMessageWait(ctx, arguments)
	case "sandbox_explain":
		data, err = s.toolSandboxExplain(arguments)
	case "terminal_list":
		data, err = s.toolTerminalList(ctx, arguments)
	case "terminal_read":
		data, err = s.toolTerminalRead(ctx, arguments)
	case "terminal_send":
		data, err = s.toolTerminalSend(ctx, arguments)
	case "terminal_wait":
		data, err = s.toolTerminalWait(ctx, arguments)
	case "terminal_start":
		data, err = s.toolTerminalStart(arguments)
	case "agent_start":
		data, err = s.toolAgentStart(arguments)
	case "agent_run":
		data, err = s.toolAgentRun(arguments)
	case "agent_status":
		data, err = s.toolAgentStatus(arguments)
	case "agent_wait":
		data, err = s.toolAgentWait(ctx, arguments)
	case "agent_result":
		data, err = s.toolAgentResult(arguments)
	case "agent_events":
		data, err = s.toolAgentEvents(arguments)
	case "agent_steer":
		data, err = s.toolAgentSteer(arguments)
	case "agent_stop":
		data, err = s.toolAgentStop(arguments)
	case "terminal_rename":
		data, err = s.toolTerminalRename(ctx, arguments)
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
		err = fmt.Errorf("unknown tool: %s", name)
		return nil, false, &localMCPError{Code: -32602, Message: "unknown tool: " + name}
	}
	if err != nil {
		fmt.Fprintf(s.logs, "wingthing MCP %s: %v\n", name, err)
		return map[string]any{"error": err.Error()}, true, nil
	}
	return data, isError, nil
}

func (s *localMCPServer) auditToolCall(tool string, arguments json.RawMessage, result map[string]any, decision string) error {
	if s.cfg == nil || s.cfg.Dir == "" {
		return nil
	}
	target := ""
	var parsed map[string]any
	if json.Unmarshal(arguments, &parsed) == nil {
		for _, key := range []string{"session", "run_id", "task_id", "message_id", "after_id", "reply_to", "prompt_name", "name"} {
			if value, ok := parsed[key].(string); ok && value != "" {
				target = value
				break
			}
		}
	}
	if target == "" && result != nil {
		for _, key := range []string{"session", "run_id", "task_id", "message_id"} {
			if value, ok := result[key].(string); ok && value != "" {
				target = value
				break
			}
		}
	}
	digest := sha256.Sum256(arguments)
	record := map[string]any{
		"timestamp":       time.Now().UTC().Format(time.RFC3339Nano),
		"principal":       s.clientPrincipal(),
		"tool":            tool,
		"decision":        decision,
		"argument_sha256": fmt.Sprintf("%x", digest[:]),
		"isolation":       s.sessionIsolationMode(),
	}
	if s.actor != "" {
		record["actor"] = s.actor
	}
	if target != "" {
		record["target"] = target
	}
	if err := os.MkdirAll(s.cfg.Dir, 0700); err != nil {
		return err
	}
	path := filepath.Join(s.cfg.Dir, "mcp-audit.log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Chmod(0600); err != nil {
		return err
	}
	return json.NewEncoder(file).Encode(record)
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
		"version":           version,
		"principal":         s.clientPrincipal(),
		"agents":            agents,
		"actor":             s.clientActor(),
		"objects":           []string{"terminal", "agent_run", "message", "prompt_asset", "task", "loop", "swarm", "sandbox_policy"},
		"session_isolation": s.sessionIsolationMode(),
		"transports": map[string]any{
			"local": true,
			"ssh":   true,
			"web":   true,
		},
	}, nil
}

const maxMessageContentBytes = 32 << 10

var messageKinds = map[string]bool{
	"message":  true,
	"status":   true,
	"question": true,
	"answer":   true,
	"evidence": true,
	"error":    true,
}

func (s *localMCPServer) toolMessageSend(arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		Content    string `json:"content"`
		Channel    string `json:"channel"`
		ToActor    string `json:"to_actor"`
		Kind       string `json:"kind"`
		ReplyTo    string `json:"reply_to"`
		TTLSeconds int    `json:"ttl_seconds"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if strings.TrimSpace(args.Content) == "" {
		return nil, errors.New("content is required")
	}
	if len([]byte(args.Content)) > maxMessageContentBytes {
		return nil, fmt.Errorf("content exceeds %d bytes", maxMessageContentBytes)
	}
	channel, err := normalizeMessageChannel(args.Channel)
	if err != nil {
		return nil, err
	}
	toActor, err := normalizeMessageActorRef(args.ToActor, "to_actor")
	if err != nil {
		return nil, err
	}
	replyTo, err := normalizeMessageID(args.ReplyTo, "reply_to")
	if err != nil {
		return nil, err
	}
	kind := args.Kind
	if kind == "" {
		kind = "message"
	}
	if !messageKinds[kind] {
		return nil, fmt.Errorf("unsupported message kind %q", kind)
	}
	ttl := args.TTLSeconds
	if ttl == 0 {
		ttl = 86400
	}
	if ttl < 60 || ttl > 604800 {
		return nil, errors.New("ttl_seconds must be between 60 and 604800")
	}

	db, err := s.openMessageStore()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if err := db.PurgeExpiredMessages(); err != nil {
		return nil, err
	}
	message := &store.Message{
		MessageID:      "msg-" + uuid.NewString(),
		OwnerID:        s.clientPrincipal(),
		SenderActor:    s.clientActor(),
		RecipientActor: toActor,
		Channel:        channel,
		Kind:           kind,
		ReplyTo:        replyTo,
		Content:        args.Content,
		ExpiresAt:      time.Now().UTC().Add(time.Duration(ttl) * time.Second),
	}
	if err := db.CreateMessage(message); err != nil {
		return nil, err
	}
	return map[string]any{
		"message":    messageResult(message),
		"message_id": message.MessageID,
		"owner":      s.clientPrincipal(),
		"actor":      s.clientActor(),
	}, nil
}

func (s *localMCPServer) toolMessageList(arguments json.RawMessage) (map[string]any, error) {
	var args messageListArgs
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if err := args.normalize(); err != nil {
		return nil, err
	}
	db, err := s.openMessageStore()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if err := db.PurgeExpiredMessages(); err != nil {
		return nil, err
	}
	messages, err := db.ListMessages(s.clientPrincipal(), s.clientActor(), args.Channel, args.AfterID, args.Limit, args.IncludeSent)
	if err != nil {
		return nil, err
	}
	return messageListResult(s, args.Channel, args.AfterID, messages, false), nil
}

func (s *localMCPServer) toolMessageWait(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
	var args messageWaitArgs
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if err := args.normalize(); err != nil {
		return nil, err
	}
	db, err := s.openMessageStore()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if err := db.PurgeExpiredMessages(); err != nil {
		return nil, err
	}
	timeout := args.TimeoutSeconds
	if timeout == 0 {
		timeout = 30
	}
	if timeout < 0.1 || timeout > 3600 {
		return nil, errors.New("timeout_seconds must be between 0.1 and 3600")
	}
	timer := time.NewTimer(time.Duration(timeout * float64(time.Second)))
	defer timer.Stop()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		messages, err := db.ListMessages(s.clientPrincipal(), s.clientActor(), args.Channel, args.AfterID, args.Limit, false)
		if err != nil {
			return nil, err
		}
		if len(messages) > 0 {
			return messageListResult(s, args.Channel, args.AfterID, messages, false), nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return messageListResult(s, args.Channel, args.AfterID, nil, true), nil
		case <-ticker.C:
		}
	}
}

type messageListArgs struct {
	Channel     string `json:"channel"`
	AfterID     string `json:"after_id"`
	Limit       int    `json:"limit"`
	IncludeSent bool   `json:"include_sent"`
}

func (args *messageListArgs) normalize() error {
	channel, err := normalizeMessageChannel(args.Channel)
	if err != nil {
		return err
	}
	afterID, err := normalizeMessageID(args.AfterID, "after_id")
	if err != nil {
		return err
	}
	args.Channel = channel
	args.AfterID = afterID
	if args.Limit == 0 {
		args.Limit = 20
	}
	if args.Limit < 1 || args.Limit > 20 {
		return errors.New("limit must be between 1 and 20")
	}
	return nil
}

type messageWaitArgs struct {
	Channel        string  `json:"channel"`
	AfterID        string  `json:"after_id"`
	Limit          int     `json:"limit"`
	TimeoutSeconds float64 `json:"timeout_seconds"`
}

func (args *messageWaitArgs) normalize() error {
	list := messageListArgs{Channel: args.Channel, AfterID: args.AfterID, Limit: args.Limit}
	if err := list.normalize(); err != nil {
		return err
	}
	args.Channel, args.AfterID, args.Limit = list.Channel, list.AfterID, list.Limit
	return nil
}

func normalizeMessageChannel(channel string) (string, error) {
	channel = strings.TrimSpace(channel)
	if channel == "" {
		channel = "factory"
	}
	if err := validateSessionName(channel); err != nil {
		return "", fmt.Errorf("invalid message channel: %w", err)
	}
	return channel, nil
}

func normalizeMessageActorRef(actor, field string) (string, error) {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return "", nil
	}
	if len(actor) > 256 {
		return "", fmt.Errorf("%s must be at most 256 characters", field)
	}
	for _, r := range actor {
		if r < 0x21 || r > 0x7e {
			return "", fmt.Errorf("%s must contain printable non-space ASCII", field)
		}
	}
	return actor, nil
}

func normalizeMessageID(id, field string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", nil
	}
	if len(id) > 128 || !strings.HasPrefix(id, "msg-") {
		return "", fmt.Errorf("%s is not a Wingthing message ID", field)
	}
	for _, r := range id {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
			return "", fmt.Errorf("%s is not a Wingthing message ID", field)
		}
	}
	return id, nil
}

func (s *localMCPServer) openMessageStore() (*store.Store, error) {
	if s.cfg == nil || s.cfg.Dir == "" {
		return nil, errors.New("Wingthing state directory is required for messages")
	}
	if err := os.MkdirAll(s.cfg.Dir, 0700); err != nil {
		return nil, err
	}
	return store.Open(s.cfg.DBPath())
}

func messageResult(message *store.Message) map[string]any {
	return map[string]any{
		"message_id":   message.MessageID,
		"sender_actor": message.SenderActor,
		"to_actor":     message.RecipientActor,
		"channel":      message.Channel,
		"kind":         message.Kind,
		"reply_to":     message.ReplyTo,
		"content":      message.Content,
		"created_at":   message.CreatedAt.UTC().Format(time.RFC3339),
		"expires_at":   message.ExpiresAt.UTC().Format(time.RFC3339),
	}
}

func messageListResult(s *localMCPServer, channel, afterID string, messages []*store.Message, timedOut bool) map[string]any {
	items := make([]map[string]any, 0, len(messages))
	next := afterID
	for _, message := range messages {
		items = append(items, messageResult(message))
		next = message.MessageID
	}
	return map[string]any{
		"owner":         s.clientPrincipal(),
		"actor":         s.clientActor(),
		"channel":       channel,
		"messages":      items,
		"next_after_id": next,
		"timed_out":     timedOut,
	}
}

func (s *localMCPServer) sessionIsolationMode() string {
	if s.unsandboxed {
		return "outer-boundary"
	}
	return "wingthing-sandbox"
}

func (s *localMCPServer) toolSandboxExplain(arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		Agent           string `json:"agent"`
		Config          string `json:"config"`
		CWD             string `json:"cwd"`
		ProviderBaseURL string `json:"provider_base_url"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	cwd, err := s.resolveWorkingDirectory(args.CWD)
	if err != nil {
		return nil, err
	}
	if args.Config != "" {
		args.Config, err = s.resolveConfigPath(args.Config)
		if err != nil {
			return nil, err
		}
	}
	var eggCfg *egg.EggConfig
	var source string
	if s.unsandboxed && args.Config == "" {
		eggCfg = egg.UnsandboxedEggConfig()
		source = "MCP server --unsandboxed"
	} else {
		eggCfg, source, err = loadEggConfigForExplain(args.Config, cwd)
		if err != nil {
			return nil, err
		}
	}
	home, _ := os.UserHomeDir()
	policy, err := explainPolicyWithProvider(eggCfg, args.Agent, home, source, args.ProviderBaseURL)
	if err != nil {
		return nil, err
	}
	return map[string]any{"policy": policy}, nil
}

func (s *localMCPServer) resolveConfigPath(path string) (string, error) {
	resolved, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve sandbox config path: %w", err)
	}
	canonical := filepath.Clean(resolved)
	if evaluated, evalErr := filepath.EvalSymlinks(canonical); evalErr == nil {
		canonical = evaluated
	}
	if s.enforcePathBounds && (len(s.allowedPaths) == 0 || !isUnderPaths(canonical, s.allowedPaths)) {
		return "", fmt.Errorf("sandbox config %q is outside this user's roost paths", path)
	}
	return canonical, nil
}

func (s *localMCPServer) ownsSession(session localSession) bool {
	principal := s.clientPrincipal()
	if principal == "default" {
		return session.Principal == "" || session.Principal == principal
	}
	return session.Principal == principal
}

func (s *localMCPServer) resolveOwnedSession(ctx context.Context, ref string) (localSession, error) {
	session, err := resolveActiveSession(ctx, s.cfg, ref)
	if err != nil {
		return localSession{}, err
	}
	if !s.ownsSession(session) {
		return localSession{}, errors.New("session not found or not owned by caller")
	}
	if s.enforcePathBounds && (len(s.allowedPaths) == 0 || !isUnderPaths(canonicalSessionPath(session.CWD), s.allowedPaths)) {
		return localSession{}, errors.New("session not found or not owned by caller")
	}
	return session, nil
}

func canonicalSessionPath(path string) string {
	cleaned := filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil {
		return resolved
	}
	return cleaned
}

func (s *localMCPServer) checkSpawnBounds() error {
	if s.maxSessions > 0 {
		sessions, err := discoverSessionRefs(s.cfg)
		if err != nil {
			return err
		}
		owned := 0
		for _, session := range sessions {
			if s.ownsSession(session) {
				owned++
			}
		}
		if owned >= s.maxSessions {
			return fmt.Errorf("principal %q reached max_sessions=%d", s.clientPrincipal(), s.maxSessions)
		}
	}
	if s.maxSpawnsPerHour > 0 {
		s.spawnMu.Lock()
		defer s.spawnMu.Unlock()
		cutoff := time.Now().Add(-time.Hour)
		kept := s.spawnTimes[:0]
		for _, timestamp := range s.spawnTimes {
			if timestamp.After(cutoff) {
				kept = append(kept, timestamp)
			}
		}
		s.spawnTimes = kept
		if len(s.spawnTimes) >= s.maxSpawnsPerHour {
			return fmt.Errorf("principal %q reached max_spawns_per_hour=%d", s.clientPrincipal(), s.maxSpawnsPerHour)
		}
	}
	return nil
}

func (s *localMCPServer) recordSpawn() {
	if s.maxSpawnsPerHour <= 0 {
		return
	}
	s.spawnMu.Lock()
	s.spawnTimes = append(s.spawnTimes, time.Now())
	s.spawnMu.Unlock()
}

func (s *localMCPServer) toolTerminalList(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
	if err := requireEmptyObject(arguments); err != nil {
		return nil, err
	}
	sessions, err := discoverActiveSessions(ctx, s.cfg)
	if err != nil {
		return nil, err
	}
	owned := make([]localSession, 0, len(sessions))
	for _, session := range sessions {
		if s.ownsSession(session) && (!s.enforcePathBounds || (len(s.allowedPaths) > 0 && isUnderPaths(canonicalSessionPath(session.CWD), s.allowedPaths))) {
			owned = append(owned, session)
		}
	}
	return map[string]any{"sessions": owned}, nil
}

func (s *localMCPServer) toolTerminalRead(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		Session string `json:"session"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if args.Session == "" {
		return nil, errors.New("session is required")
	}
	owned, err := s.resolveOwnedSession(ctx, args.Session)
	if err != nil {
		return nil, err
	}
	session, snapshot, err := readSessionSnapshot(ctx, s.cfg, owned.ID)
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
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if args.Session == "" {
		return nil, errors.New("session is required")
	}
	input := []byte(args.Input)
	if args.Enter {
		input = append(input, '\r')
	}
	owned, err := s.resolveOwnedSession(ctx, args.Session)
	if err != nil {
		return nil, err
	}
	session, err := sendSessionBytes(ctx, s.cfg, owned.ID, input)
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
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if args.Session == "" {
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
	owned, err := s.resolveOwnedSession(waitCtx, args.Session)
	if err != nil {
		return nil, err
	}
	if args.Contains != "" {
		session, err := waitForSessionText(waitCtx, s.cfg, owned.ID, args.Contains)
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
	session, ec, err := openLocalEgg(waitCtx, s.cfg, owned.ID)
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

func (s *localMCPServer) toolTerminalStart(arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		Command []string `json:"command"`
		CWD     string   `json:"cwd"`
		Label   string   `json:"label"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if err := s.checkSpawnBounds(); err != nil {
		return nil, err
	}
	resolvedCWD, err := s.resolveWorkingDirectory(args.CWD)
	if err != nil {
		return nil, err
	}
	args.CWD = resolvedCWD
	kind := "command"
	if len(args.Command) == 0 {
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/sh"
		}
		args.Command = []string{shell}
		kind = "shell"
	}
	eggCfg, err := loadSpawnEggConfig("", args.CWD, s.unsandboxed)
	if err != nil {
		return nil, err
	}
	sessionID := uuid.NewString()[:8]
	ec, err := spawnEgg(s.cfg, sessionID, "", eggCfg, 24, 80, args.CWD, false, false, false, s.identity, 0,
		spawnEggOpts{Label: args.Label, Kind: kind, Command: args.Command, Principal: s.clientPrincipal()})
	if err != nil {
		return nil, err
	}
	_ = ec.Close()
	s.recordSpawn()
	return map[string]any{
		"session": sessionID, "label": args.Label, "kind": kind,
		"command": args.Command, "cwd": args.CWD, "isolation": s.sessionIsolationMode(),
	}, nil
}

func (s *localMCPServer) toolAgentStart(arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		Agent      string   `json:"agent"`
		Model      string   `json:"model"`
		CWD        string   `json:"cwd"`
		Label      string   `json:"label"`
		Unattended bool     `json:"unattended"`
		Args       []string `json:"args"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if args.Agent == "" {
		return nil, errors.New("agent is required")
	}
	if err := s.checkSpawnBounds(); err != nil {
		return nil, err
	}
	if _, ok := agentpkg.LookupDefinition(args.Agent); !ok {
		return nil, fmt.Errorf("unsupported agent %q", args.Agent)
	}
	if err := validateAgentArgs(args.Args); err != nil {
		return nil, err
	}
	modelArgs, err := agentModelArgs(args.Agent, args.Model)
	if err != nil {
		return nil, err
	}
	args.Args = append(modelArgs, args.Args...)
	resolvedCWD, err := s.resolveWorkingDirectory(args.CWD)
	if err != nil {
		return nil, err
	}
	args.CWD = resolvedCWD
	eggCfg, err := loadSpawnEggConfig("", args.CWD, s.unsandboxed)
	if err != nil {
		return nil, err
	}
	if args.Unattended {
		copyCfg := *eggCfg
		eggCfg = &copyCfg
		eggCfg.DangerouslySkipPermissions = true
	}
	sessionID := uuid.NewString()[:8]
	ec, err := spawnEgg(s.cfg, sessionID, args.Agent, eggCfg, 24, 80, args.CWD, false, false, false, s.identity, 0,
		spawnEggOpts{Label: args.Label, Kind: "agent", AgentArgs: args.Args, Principal: s.clientPrincipal()})
	if err != nil {
		return nil, err
	}
	_ = ec.Close()
	s.recordSpawn()
	return map[string]any{
		"session": sessionID, "label": args.Label, "agent": args.Agent,
		"cwd": args.CWD, "args": args.Args, "isolation": s.sessionIsolationMode(),
	}, nil
}

func agentModelArgs(agentName, model string) ([]string, error) {
	if model == "" {
		return nil, nil
	}
	if strings.TrimSpace(model) != model || strings.IndexByte(model, 0) >= 0 {
		return nil, errors.New("model must be a non-empty provider model name without surrounding whitespace")
	}
	switch agentName {
	case "claude":
		return []string{"--model", model}, nil
	case "codex":
		return []string{"-m", model}, nil
	default:
		return nil, fmt.Errorf("model selection is not defined for agent %q", agentName)
	}
}

type agentRunArgs struct {
	Prompt         string `json:"prompt"`
	Agent          string `json:"agent"`
	Model          string `json:"model"`
	CWD            string `json:"cwd"`
	Label          string `json:"label"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

func (s *localMCPServer) toolAgentRun(arguments json.RawMessage) (map[string]any, error) {
	var args agentRunArgs
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	return s.submitAgentRun(args, nil)
}

func (s *localMCPServer) submitAgentRun(args agentRunArgs, parentID *string) (map[string]any, error) {
	if strings.TrimSpace(args.Prompt) == "" {
		return nil, errors.New("prompt is required")
	}
	if args.Agent == "" {
		args.Agent = s.cfg.DefaultAgent
	}
	if _, ok := agentpkg.LookupDefinition(args.Agent); !ok {
		return nil, fmt.Errorf("unsupported agent %q", args.Agent)
	}
	if _, err := agentModelArgs(args.Agent, args.Model); err != nil {
		return nil, err
	}
	if err := validateSessionName(args.Label); err != nil {
		return nil, err
	}
	if args.TimeoutSeconds == 0 {
		args.TimeoutSeconds = 900
	}
	if args.TimeoutSeconds < 10 || args.TimeoutSeconds > 7200 {
		return nil, errors.New("timeout_seconds must be between 10 and 7200")
	}
	resolvedCWD, err := s.resolveWorkingDirectory(args.CWD)
	if err != nil {
		return nil, err
	}
	if err := s.checkSpawnBounds(); err != nil {
		return nil, err
	}

	var dependsOn *string
	if parentID != nil {
		encoded, _ := json.Marshal([]string{*parentID})
		value := string(encoded)
		dependsOn = &value
	}
	now := time.Now().UTC()
	task := &store.Task{
		ID: genTaskID(), Type: "agent_run", What: args.Prompt,
		Agent: args.Agent, Model: args.Model, TimeoutSeconds: args.TimeoutSeconds,
		RunAt: now, CreatedAt: now,
		ParentID: parentID, DependsOn: dependsOn, CWD: resolvedCWD,
		Principal: s.clientPrincipal(), RunnerPID: os.Getpid(),
	}
	if s.unsandboxed {
		task.Isolation = "privileged"
	}
	taskStore, err := store.Open(s.cfg.DBPath())
	if err != nil {
		return nil, err
	}
	if err := taskStore.CreateTask(task); err != nil {
		taskStore.Close()
		return nil, err
	}
	if args.Label != "" {
		label := args.Label
		_ = taskStore.AppendLog(task.ID, "label", &label)
	}
	taskStore.Close()
	s.recordSpawn()
	s.startAgentRun(task.ID, parentID)
	data := agentRunStatusData(task)
	data["run_id"] = task.ID
	if args.Label != "" {
		data["label"] = args.Label
	}
	return data, nil
}

func (s *localMCPServer) startAgentRun(runID string, parentID *string) {
	runCtx, cancel := context.WithCancel(context.Background())
	key := s.agentRunKey(runID)
	done := make(chan struct{})
	activeMCPAgentRuns.Store(key, activeMCPAgentRun{principal: s.clientPrincipal(), cancel: cancel, done: done})
	options := s.agentTaskRunOptions()
	go func() {
		defer close(done)
		defer cancel()
		defer activeMCPAgentRuns.Delete(key)
		if parentID != nil {
			if err := s.waitForAgentRunTerminal(runCtx, *parentID); err != nil {
				s.setAgentRunError(runID, err)
				return
			}
			parent, parentStore, err := s.ownedAgentRun(*parentID)
			if err != nil {
				s.setAgentRunError(runID, err)
				return
			}
			parentStore.Close()
			if parent.Status != "done" {
				s.setAgentRunError(runID, fmt.Errorf("parent agent run %s finished with status %s", parent.ID, parent.Status))
				return
			}
		}
		taskStore, err := store.Open(s.cfg.DBPath())
		if err != nil {
			s.setAgentRunError(runID, err)
			return
		}
		defer taskStore.Close()
		task, err := taskStore.GetTask(runID)
		if err != nil || task == nil {
			if err == nil {
				err = fmt.Errorf("run %q disappeared before execution", runID)
			}
			s.setAgentRunError(runID, err)
			return
		}
		if s.runAgentTask != nil {
			_ = s.runAgentTask(runCtx, s.cfg, taskStore, task, options)
		} else {
			_ = runTaskToWithOptions(runCtx, s.cfg, taskStore, task, io.Discard, options)
		}
	}()
}

func (s *localMCPServer) agentTaskRunOptions() taskRunOptions {
	options := taskRunOptions{
		SharedHost:   s.identity.SharedHost,
		AllowedPaths: append([]string(nil), s.identity.AllowedPaths...),
	}
	if s.identity.UserID != "" && (s.identity.SharedHost || s.identity.OrgWing) {
		options.UserHome = filepath.Join(s.cfg.Dir, "user-homes", userHash(s.identity.UserID))
		if !s.identity.SharedHost {
			_ = os.MkdirAll(options.UserHome, 0700)
		}
	}
	return options
}

func (s *localMCPServer) setAgentRunError(runID string, runErr error) {
	if runErr == nil {
		return
	}
	taskStore, err := store.Open(s.cfg.DBPath())
	if err == nil {
		defer taskStore.Close()
		_ = taskStore.SetTaskError(runID, runErr.Error())
	}
}

func (s *localMCPServer) agentRunKey(runID string) string {
	return s.cfg.DBPath() + "\x00" + runID
}

func (s *localMCPServer) ownedAgentRun(runID string) (*store.Task, *store.Store, error) {
	if runID == "" {
		return nil, nil, errors.New("run_id is required")
	}
	taskStore, err := store.Open(s.cfg.DBPath())
	if err != nil {
		return nil, nil, err
	}
	task, err := taskStore.GetTask(runID)
	if err != nil {
		taskStore.Close()
		return nil, nil, err
	}
	if task == nil || task.Type != "agent_run" || !s.ownsTask(task) {
		taskStore.Close()
		return nil, nil, fmt.Errorf("agent run %q not found or not owned by caller", runID)
	}
	if (task.Status == "pending" || task.Status == "running") && task.RunnerPID > 0 && !ownedProcessIsAlive(task.RunnerPID) {
		_ = taskStore.SetTaskError(task.ID, fmt.Sprintf("supervising Wingthing process %d exited", task.RunnerPID))
		task, err = taskStore.GetTask(runID)
		if err != nil {
			taskStore.Close()
			return nil, nil, err
		}
	}
	return task, taskStore, nil
}

func agentRunTerminal(status string) bool {
	return status == "done" || status == "failed"
}

func agentRunStatusData(task *store.Task) map[string]any {
	data := map[string]any{
		"run_id": task.ID, "status": task.Status, "agent": task.Agent,
		"model": task.Model, "cwd": task.CWD, "isolation": task.Isolation,
		"timeout_seconds": task.TimeoutSeconds,
		"created_at":      task.CreatedAt.UTC().Format(time.RFC3339),
	}
	if task.StartedAt != nil {
		data["started_at"] = task.StartedAt.UTC().Format(time.RFC3339)
	}
	if task.FinishedAt != nil {
		data["finished_at"] = task.FinishedAt.UTC().Format(time.RFC3339)
	}
	return data
}

func (s *localMCPServer) toolAgentStatus(arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		RunID string `json:"run_id"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	task, taskStore, err := s.ownedAgentRun(args.RunID)
	if err != nil {
		return nil, err
	}
	defer taskStore.Close()
	return agentRunStatusData(task), nil
}

func (s *localMCPServer) toolAgentWait(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		RunID          string  `json:"run_id"`
		TimeoutSeconds float64 `json:"timeout_seconds"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if args.TimeoutSeconds == 0 {
		args.TimeoutSeconds = 30
	}
	if args.TimeoutSeconds < 0.1 || args.TimeoutSeconds > 3600 {
		return nil, errors.New("timeout_seconds must be between 0.1 and 3600")
	}
	waitCtx, cancel := context.WithTimeout(ctx, durationSeconds(args.TimeoutSeconds))
	defer cancel()
	err := s.waitForAgentRunTerminal(waitCtx, args.RunID)
	task, taskStore, loadErr := s.ownedAgentRun(args.RunID)
	if loadErr != nil {
		return nil, loadErr
	}
	defer taskStore.Close()
	data := agentRunStatusData(task)
	if errors.Is(err, context.DeadlineExceeded) {
		data["timed_out"] = true
		return data, nil
	}
	return data, err
}

func (s *localMCPServer) waitForAgentRunTerminal(ctx context.Context, runID string) error {
	task, taskStore, err := s.ownedAgentRun(runID)
	if err != nil {
		return err
	}
	defer taskStore.Close()
	if agentRunTerminal(task.Status) {
		return nil
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		task, err = taskStore.GetTask(runID)
		if err != nil {
			return err
		}
		if task == nil || task.Type != "agent_run" || !s.ownsTask(task) {
			return fmt.Errorf("agent run %q not found", runID)
		}
		terminal := agentRunTerminal(task.Status)
		if terminal {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *localMCPServer) toolAgentResult(arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		RunID    string `json:"run_id"`
		MaxChars int    `json:"max_chars"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if args.MaxChars == 0 {
		args.MaxChars = 50000
	}
	if args.MaxChars < 1 || args.MaxChars > 200000 {
		return nil, errors.New("max_chars must be between 1 and 200000")
	}
	task, taskStore, err := s.ownedAgentRun(args.RunID)
	if err != nil {
		return nil, err
	}
	defer taskStore.Close()
	data := agentRunStatusData(task)
	data["ready"] = agentRunTerminal(task.Status)
	if task.Output != nil {
		outputRunes := []rune(*task.Output)
		output := string(outputRunes)
		if len(outputRunes) > args.MaxChars {
			output = string(outputRunes[:args.MaxChars])
			data["truncated"] = true
			data["total_chars"] = len(outputRunes)
		}
		data["output"] = output
	}
	if task.Error != nil {
		data["error"] = *task.Error
	}
	return data, nil
}

func (s *localMCPServer) toolAgentEvents(arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		RunID string `json:"run_id"`
		Limit int    `json:"limit"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if args.Limit == 0 {
		args.Limit = 50
	}
	if args.Limit < 1 || args.Limit > 200 {
		return nil, errors.New("limit must be between 1 and 200")
	}
	_, taskStore, err := s.ownedAgentRun(args.RunID)
	if err != nil {
		return nil, err
	}
	defer taskStore.Close()
	rows, err := taskStore.DB().Query(`SELECT timestamp, event, COALESCE(detail, '') FROM task_log WHERE task_id = ? ORDER BY id DESC LIMIT ?`, args.RunID, args.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []map[string]any
	for rows.Next() {
		var timestamp, event, detail string
		if err := rows.Scan(&timestamp, &event, &detail); err != nil {
			return nil, err
		}
		entry := map[string]any{"timestamp": timestamp, "event": event}
		if detail != "" {
			entry["detail"] = detail
		}
		events = append(events, entry)
	}
	return map[string]any{"run_id": args.RunID, "events": events}, rows.Err()
}

func (s *localMCPServer) toolAgentSteer(arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		RunID  string `json:"run_id"`
		Prompt string `json:"prompt"`
		Model  string `json:"model"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if strings.TrimSpace(args.Prompt) == "" {
		return nil, errors.New("prompt is required")
	}
	parent, taskStore, err := s.ownedAgentRun(args.RunID)
	if err != nil {
		return nil, err
	}
	taskStore.Close()
	model := args.Model
	if model == "" {
		model = parent.Model
	}
	parentID := parent.ID
	return s.submitAgentRun(agentRunArgs{
		Prompt: "Prior request:\n" + parent.What + "\n\nNew direction:\n" + args.Prompt,
		Agent:  parent.Agent, Model: model,
		CWD: parent.CWD, Label: "followup-" + parent.ID, TimeoutSeconds: parent.TimeoutSeconds,
	}, &parentID)
}

func (s *localMCPServer) toolAgentStop(arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		RunID string `json:"run_id"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	task, taskStore, err := s.ownedAgentRun(args.RunID)
	if err != nil {
		return nil, err
	}
	defer taskStore.Close()
	if agentRunTerminal(task.Status) {
		return agentRunStatusData(task), nil
	}
	activeValue, ok := activeMCPAgentRuns.Load(s.agentRunKey(args.RunID))
	active, valid := activeValue.(activeMCPAgentRun)
	if !ok || !valid || active.principal != s.clientPrincipal() {
		return nil, errors.New("run is no longer attached to this Wingthing process")
	}
	active.cancel()
	select {
	case <-active.done:
	case <-time.After(15 * time.Second):
		return nil, errors.New("agent run cancellation is still in progress")
	}
	latest, err := taskStore.GetTask(args.RunID)
	if err != nil || latest == nil {
		return nil, fmt.Errorf("reload stopped agent run: %w", err)
	}
	message := "stopped by MCP principal " + s.clientPrincipal()
	if err := taskStore.SetTaskError(args.RunID, message); err != nil {
		return nil, err
	}
	latest.Status = "failed"
	latest.Error = &message
	data := agentRunStatusData(latest)
	data["stopped"] = true
	return data, nil
}

func (s *localMCPServer) toolTerminalRename(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		Session string `json:"session"`
		Name    string `json:"name"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if args.Session == "" || args.Name == "" {
		return nil, errors.New("session and name are required")
	}
	if err := validateSessionName(args.Name); err != nil {
		return nil, err
	}
	session, err := s.resolveOwnedSession(ctx, args.Session)
	if err != nil {
		return nil, err
	}
	if err := ensureSessionNameAvailable(s.cfg, args.Name, session.ID); err != nil {
		return nil, err
	}
	if err := writeSessionName(filepath.Join(s.cfg.Dir, "eggs", session.ID), args.Name); err != nil {
		return nil, err
	}
	return map[string]any{"session": session.ID, "name": args.Name}, nil
}

func (s *localMCPServer) toolTerminalStop(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		Session string `json:"session"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if args.Session == "" {
		return nil, errors.New("session is required")
	}
	owned, err := s.resolveOwnedSession(ctx, args.Session)
	if err != nil {
		return nil, err
	}
	session, ec, err := openLocalEgg(ctx, s.cfg, owned.ID)
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
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if args.Name == "" {
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
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if args.Name == "" || strings.TrimSpace(args.Template) == "" {
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
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if args.TaskID == "" {
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
	if !s.ownsTask(task) {
		owner := task.Principal
		if owner == "" {
			owner = "human/default"
		}
		return nil, fmt.Errorf("task %s is owned by principal %q; caller is %q", task.ID, owner, s.clientPrincipal())
	}
	return taskData(task), nil
}

func (s *localMCPServer) ownsTask(task *store.Task) bool {
	if s.clientPrincipal() == "default" {
		return task.Principal == "" || task.Principal == "default"
	}
	return task.Principal == s.clientPrincipal()
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
		PromptName: promptName, PromptRevision: promptRevision, Principal: s.clientPrincipal(),
	}
	if s.unsandboxed {
		task.Isolation = "privileged"
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
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, false, err
	}
	if strings.TrimSpace(args.Prompt) == "" {
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
			Principal: s.clientPrincipal(),
		}
		if s.unsandboxed {
			task.Isolation = "privileged"
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
		RunAt: time.Now().UTC(), Status: "pending", CWD: cwd, Principal: s.clientPrincipal(),
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
		"principal": task.Principal,
	}
	if task.Model != "" {
		data["model"] = task.Model
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

func (s *localMCPServer) resolveWorkingDirectory(cwd string) (string, error) {
	resolved, err := resolveWorkingDirectory(cwd)
	if err != nil {
		return "", err
	}
	canonical := filepath.Clean(resolved)
	if evaluated, evalErr := filepath.EvalSymlinks(canonical); evalErr == nil {
		canonical = evaluated
	}
	if s.enforcePathBounds && len(s.allowedPaths) == 0 {
		return "", errors.New("this roost user has no configured workspace paths")
	}
	if len(s.allowedPaths) > 0 && !isUnderPaths(canonical, s.allowedPaths) {
		return "", fmt.Errorf("working directory %q is outside this user's roost paths", resolved)
	}
	return canonical, nil
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
