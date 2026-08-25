// Package control defines the transport-independent contract for Wingthing
// runtime operations. Adapters decide how to authenticate and transport a call;
// the operation name, schema, grant, annotations, and audit policy live here.
package control

import (
	"encoding/json"
	"strings"
)

// Surface identifies one adapter that can expose an operation.
type Surface string

const (
	SurfaceLocalMCP Surface = "local-mcp"
	SurfaceHTTPMCP  Surface = "http-mcp"
	// SurfaceDirectMCP is the native multi-wing adapter. Portal operations are
	// local to the coordinator, while every wing-owned operation carries an
	// explicit wing_id used to select a peer-to-peer transport.
	SurfaceDirectMCP Surface = "direct-mcp"
	ContractVersion          = "v1"
)

// Authority identifies the component that owns an operation's state and
// handler. Portal operations use gateway inventory; wing operations use the
// selected execution runtime.
type Authority string

const (
	AuthorityWing   Authority = "wing"
	AuthorityPortal Authority = "portal"
)

// AuditArgumentMode describes how an adapter may record tool arguments.
type AuditArgumentMode string

const (
	// AuditArgumentsDigest permits only a digest of the complete argument
	// envelope. Adapters may separately record the bounded target selected by
	// AuditTargetKeys.
	AuditArgumentsDigest AuditArgumentMode = "digest"
)

// Tool is one versioned Wingthing control operation. MCP adapters serialize
// the public fields directly. The remaining fields govern authorization,
// transport exposure, and audit redaction.
type Tool struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`

	Version         string            `json:"-"`
	Grant           string            `json:"-"`
	Surfaces        []Surface         `json:"-"`
	Authority       Authority         `json:"-"`
	AuditArguments  AuditArgumentMode `json:"-"`
	AuditTargetKeys []string          `json:"-"`
}

// ToolsForAuthority returns the operations implemented by one authority and
// exposed through the requested surface.
func ToolsForAuthority(surface Surface, authority Authority) []Tool {
	var tools []Tool
	for _, tool := range Tools(surface) {
		if tool.Authority == authority {
			tools = append(tools, tool)
		}
	}
	return tools
}

var catalog = buildTools()

// Tools returns the ordered operation set exposed by one adapter surface.
func Tools(surface Surface) []Tool {
	tools := make([]Tool, 0, len(catalog))
	for _, tool := range catalog {
		if tool.Supports(surface) {
			if surface == SurfaceDirectMCP && tool.Authority == AuthorityWing {
				tool.InputSchema = withWingTarget(tool.InputSchema)
			}
			tools = append(tools, tool)
		}
	}
	return tools
}

// OperationNames returns the stable operation names exposed by one surface.
func OperationNames(surface Surface) []string {
	tools := Tools(surface)
	names := make([]string, len(tools))
	for index, tool := range tools {
		names[index] = tool.Name
	}
	return names
}

// ObjectKinds returns the semantic resource kinds visible through one
// surface. The order is stable for capability responses.
func ObjectKinds(surface Surface) []string {
	type objectKind struct {
		name     string
		surfaces []Surface
	}
	objects := []objectKind{
		{name: "wing", surfaces: []Surface{SurfaceHTTPMCP, SurfaceDirectMCP}},
		{name: "terminal", surfaces: []Surface{SurfaceLocalMCP, SurfaceHTTPMCP, SurfaceDirectMCP}},
		{name: "agent_run", surfaces: []Surface{SurfaceLocalMCP, SurfaceHTTPMCP, SurfaceDirectMCP}},
		{name: "message", surfaces: []Surface{SurfaceLocalMCP, SurfaceHTTPMCP, SurfaceDirectMCP}},
		{name: "prompt_asset", surfaces: []Surface{SurfaceLocalMCP}},
		{name: "task", surfaces: []Surface{SurfaceLocalMCP}},
		{name: "loop", surfaces: []Surface{SurfaceLocalMCP}},
		{name: "swarm", surfaces: []Surface{SurfaceLocalMCP}},
		{name: "sandbox_policy", surfaces: []Surface{SurfaceLocalMCP, SurfaceHTTPMCP, SurfaceDirectMCP}},
	}
	var names []string
	for _, object := range objects {
		for _, candidate := range object.surfaces {
			if candidate == surface {
				names = append(names, object.name)
				break
			}
		}
	}
	return names
}

// Lookup returns one operation definition by stable name.
func Lookup(name string) (Tool, bool) {
	for _, tool := range catalog {
		if tool.Name == name {
			return tool, true
		}
	}
	return Tool{}, false
}

// Supports reports whether an operation is exposed by the adapter surface.
func (t Tool) Supports(surface Surface) bool {
	for _, candidate := range t.Surfaces {
		if candidate == surface {
			return true
		}
	}
	return false
}

// AuditTarget extracts the first bounded resource label approved by the
// operation definition. Full arguments remain digest-only.
func AuditTarget(name string, arguments json.RawMessage, result map[string]any) string {
	tool, ok := Lookup(name)
	if !ok {
		return ""
	}
	var parsed map[string]any
	if json.Unmarshal(arguments, &parsed) == nil {
		if target := firstString(parsed, tool.AuditTargetKeys); target != "" {
			return target
		}
	}
	return firstString(result, tool.AuditTargetKeys)
}

func firstString(values map[string]any, keys []string) string {
	for _, key := range keys {
		if value, ok := values[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func buildTools() []Tool {
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
	destructive := map[string]any{"readOnlyHint": false, "destructiveHint": true, "openWorldHint": false}
	both := []Surface{SurfaceLocalMCP, SurfaceHTTPMCP, SurfaceDirectMCP}
	local := []Surface{SurfaceLocalMCP}

	tools := []Tool{
		{
			Name: "wingthing_capabilities", Title: "Wingthing capabilities",
			Description: "Discover supported and installed agent CLIs plus the local runtime primitives available on this machine.",
			InputSchema: objectSchema(map[string]any{}), Annotations: readOnly,
			Grant: "capabilities.read", Surfaces: both,
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
			Grant: "message.send", Surfaces: both, AuditTargetKeys: []string{"reply_to", "message_id"},
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
			Grant: "message.read", Surfaces: both, AuditTargetKeys: []string{"after_id", "message_id"},
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
			Grant: "message.read", Surfaces: both, AuditTargetKeys: []string{"after_id", "message_id"},
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
			Grant: "sandbox.read", Surfaces: both,
		},
		{
			Name: "terminal_list", Title: "List persistent terminals",
			Description: "List live local Wingthing sessions with stable IDs, labels, process kind, agent, activity, and working directory.",
			InputSchema: objectSchema(map[string]any{}), Annotations: readOnly,
			Grant: "terminal.read", Surfaces: both,
		},
		{
			Name: "terminal_read", Title: "Read terminal snapshot",
			Description: "Read the current ANSI snapshot of one persistent terminal. This is raw terminal state, not semantic agent state.",
			InputSchema: objectSchema(map[string]any{
				"session": stringProperty("Session ID, unique ID prefix, or label"),
			}, "session"), Annotations: readOnly,
			Grant: "terminal.read", Surfaces: both, AuditTargetKeys: []string{"session"},
		},
		{
			Name: "terminal_send", Title: "Send terminal input",
			Description: "Send text to a persistent PTY, optionally followed by Enter.",
			InputSchema: objectSchema(map[string]any{
				"session": stringProperty("Session ID, unique ID prefix, or label"),
				"input":   stringProperty("Text to send"),
				"enter":   map[string]any{"type": "boolean", "description": "Append Enter after the text", "default": false},
			}, "session", "input"), Annotations: mutating,
			Grant: "terminal.send", Surfaces: both, AuditTargetKeys: []string{"session"},
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
			Grant: "terminal.read", Surfaces: both, AuditTargetKeys: []string{"session"},
		},
		{
			Name: "terminal_start", Title: "Start persistent terminal",
			Description: "Start a durable shell or command terminal under the MCP server's declared isolation mode and return immediately with its session ID.",
			InputSchema: objectSchema(map[string]any{
				"command": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "default": []string{}, "description": "Executable and arguments; omit to start $SHELL"},
				"cwd":     stringProperty("Working directory; defaults to the MCP server's current directory"),
				"label":   stringProperty("Optional stable human-readable session label"),
			}), Annotations: mutating,
			Grant: "terminal.start", Surfaces: both, AuditTargetKeys: []string{"session"},
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
			Grant: "terminal.start", Surfaces: both, AuditTargetKeys: []string{"session"},
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
			Grant: "agent.run", Surfaces: both, AuditTargetKeys: []string{"run_id"},
		},
		{
			Name: "agent_status", Title: "Get agent run status",
			Description: "Read bounded lifecycle metadata for one run owned by this MCP principal.",
			InputSchema: objectSchema(map[string]any{
				"run_id": stringProperty("Wingthing agent run ID"),
			}, "run_id"), Annotations: readOnly,
			Grant: "agent.read", Surfaces: both, AuditTargetKeys: []string{"run_id"},
		},
		{
			Name: "agent_wait", Title: "Wait for agent run",
			Description: "Wait without polling until an agent run reaches a terminal state or the requested timeout expires.",
			InputSchema: objectSchema(map[string]any{
				"run_id":          stringProperty("Wingthing agent run ID"),
				"timeout_seconds": map[string]any{"type": "number", "minimum": 0.1, "maximum": 3600, "default": 30},
			}, "run_id"), Annotations: readOnly,
			Grant: "agent.read", Surfaces: both, AuditTargetKeys: []string{"run_id"},
		},
		{
			Name: "agent_result", Title: "Read agent result",
			Description: "Read the final semantic output or error for one completed run, with an explicit response bound.",
			InputSchema: objectSchema(map[string]any{
				"run_id":    stringProperty("Wingthing agent run ID"),
				"max_chars": map[string]any{"type": "integer", "minimum": 1, "maximum": 200000, "default": 50000},
			}, "run_id"), Annotations: readOnly,
			Grant: "agent.read", Surfaces: both, AuditTargetKeys: []string{"run_id"},
		},
		{
			Name: "agent_events", Title: "Read agent run events",
			Description: "Read bounded lifecycle events for one owned run.",
			InputSchema: objectSchema(map[string]any{
				"run_id": stringProperty("Wingthing agent run ID"),
				"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 200, "default": 50},
			}, "run_id"), Annotations: readOnly,
			Grant: "agent.read", Surfaces: both, AuditTargetKeys: []string{"run_id"},
		},
		{
			Name: "agent_steer", Title: "Steer agent run",
			Description: "Queue an owner-scoped follow-up run that receives the prior request and result plus new direction.",
			InputSchema: objectSchema(map[string]any{
				"run_id": stringProperty("Run to follow up"),
				"prompt": stringProperty("New direction for the agent"),
				"model":  stringProperty("Optional model override for the follow-up"),
			}, "run_id", "prompt"), Annotations: modelCall,
			Grant: "agent.run", Surfaces: both, AuditTargetKeys: []string{"run_id"},
		},
		{
			Name: "agent_stop", Title: "Stop agent run",
			Description: "Cancel an active owner-scoped run and its provider process tree.",
			InputSchema: objectSchema(map[string]any{
				"run_id": stringProperty("Wingthing agent run ID"),
			}, "run_id"), Annotations: destructive,
			Grant: "agent.stop", Surfaces: both, AuditTargetKeys: []string{"run_id"},
		},
		{
			Name: "terminal_rename", Title: "Rename persistent terminal",
			Description: "Assign a stable human-readable label to a terminal owned by this MCP principal.",
			InputSchema: objectSchema(map[string]any{
				"session": stringProperty("Session ID, unique ID prefix, or current label"),
				"name":    stringProperty("New session label"),
			}, "session", "name"), Annotations: mutating,
			Grant: "terminal.rename", Surfaces: both, AuditTargetKeys: []string{"session"},
		},
		{
			Name: "terminal_stop", Title: "Stop persistent terminal",
			Description: "Stop one Wingthing session and its process tree.",
			InputSchema: objectSchema(map[string]any{
				"session": stringProperty("Session ID, unique ID prefix, or label"),
			}, "session"), Annotations: destructive,
			Grant: "terminal.stop", Surfaces: both, AuditTargetKeys: []string{"session"},
		},
		{
			Name: "prompt_list", Title: "List saved prompts",
			Description: "List current named prompt assets with immutable revisions, variables, default agents, and working directories.",
			InputSchema: objectSchema(map[string]any{}), Annotations: readOnly,
			Grant: "prompt.read", Surfaces: local,
		},
		{
			Name: "prompt_get", Title: "Get saved prompt",
			Description: "Read the current or an immutable historical revision of a named prompt asset.",
			InputSchema: objectSchema(map[string]any{
				"name":     stringProperty("Prompt asset name"),
				"revision": stringProperty("Optional immutable revision; current revision when omitted"),
			}, "name"), Annotations: readOnly,
			Grant: "prompt.read", Surfaces: local, AuditTargetKeys: []string{"name"},
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
			Grant: "prompt.save", Surfaces: local, AuditTargetKeys: []string{"name"},
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
			Grant: "prompt.run", Surfaces: local, AuditTargetKeys: []string{"prompt_name", "task_id"},
		},
		{
			Name: "task_get", Title: "Get prompt task",
			Description: "Get structured status, output, error, timing, agent, and dependency data for a Wingthing task.",
			InputSchema: objectSchema(map[string]any{
				"task_id": stringProperty("Wingthing task ID"),
			}, "task_id"), Annotations: readOnly,
			Grant: "prompt.read", Surfaces: local, AuditTargetKeys: []string{"task_id"},
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
			Grant: "prompt.run", Surfaces: local, AuditTargetKeys: []string{"task_id"},
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
			Grant: "prompt.run", Surfaces: local, AuditTargetKeys: []string{"name", "task_id"},
		},
		{
			Name: "wing_list", Title: "List portal wings",
			Description: "List every connected wing the authenticated portal user may access, including whether HTTP MCP can currently control it.",
			InputSchema: objectSchema(map[string]any{}), Annotations: readOnly,
			Grant: "wing.read", Surfaces: []Surface{SurfaceHTTPMCP, SurfaceDirectMCP}, Authority: AuthorityPortal,
		},
	}
	for index := range tools {
		tools[index].Version = ContractVersion
		if tools[index].Authority == "" {
			tools[index].Authority = AuthorityWing
		}
		tools[index].AuditArguments = AuditArgumentsDigest
	}
	return tools
}

func withWingTarget(schema map[string]any) map[string]any {
	copy := make(map[string]any, len(schema))
	for key, value := range schema {
		copy[key] = value
	}
	properties := map[string]any{}
	if current, ok := schema["properties"].(map[string]any); ok {
		for key, value := range current {
			properties[key] = value
		}
	}
	properties["wing_id"] = map[string]any{
		"type": "string", "minLength": 1,
		"description": "Stable ID of the wing that owns this operation",
	}
	copy["properties"] = properties
	required := []string{"wing_id"}
	if current, ok := schema["required"].([]string); ok {
		required = append(required, current...)
	}
	copy["required"] = required
	return copy
}
