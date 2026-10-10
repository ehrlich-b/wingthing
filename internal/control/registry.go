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
	ContractVersion  string  = "v1"
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
	Execution   map[string]any `json:"execution,omitempty"`

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
			tool = cloneTool(tool)
			if surface != SurfaceHTTPMCP && tool.Name == "agent_run" {
				tool.Execution = map[string]any{"taskSupport": "optional"}
			}
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
		{name: "conversation", surfaces: []Surface{SurfaceLocalMCP, SurfaceHTTPMCP, SurfaceDirectMCP}},
		{name: "agent_run", surfaces: []Surface{SurfaceLocalMCP, SurfaceHTTPMCP, SurfaceDirectMCP}},
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
			return cloneTool(tool), true
		}
	}
	return Tool{}, false
}

func cloneTool(tool Tool) Tool {
	tool.InputSchema = cloneContractMap(tool.InputSchema)
	tool.Annotations = cloneContractMap(tool.Annotations)
	tool.Execution = cloneContractMap(tool.Execution)
	tool.Surfaces = append([]Surface(nil), tool.Surfaces...)
	tool.AuditTargetKeys = append([]string(nil), tool.AuditTargetKeys...)
	return tool
}

func cloneContractMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = cloneContractValue(value)
	}
	return result
}

func cloneContractValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneContractMap(typed)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = cloneContractValue(item)
		}
		return result
	case []string:
		// Preserve the distinction between an explicitly declared empty array
		// and nil. JSON Schema clients observe that distinction: [] is a valid
		// default for an array property, while null violates the property's
		// declared type and changed the already-deployed MCP contract.
		result := make([]string, len(typed))
		copy(result, typed)
		return result
	default:
		return value
	}
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
		if target := firstAuditTarget(parsed, tool.AuditTargetKeys); target != "" {
			return target
		}
	}
	return firstAuditTarget(result, tool.AuditTargetKeys)
}

func auditRunIDs(value any) string {
	ids, ok := value.([]any)
	if !ok || len(ids) == 0 {
		return ""
	}
	bounded := make([]string, 0, 4)
	for index, item := range ids {
		id, ok := item.(string)
		if !ok {
			return ""
		}
		if index < 4 {
			runes := []rune(id)
			if len(runes) > 64 {
				id = string(runes[:61]) + "..."
			}
			bounded = append(bounded, id)
		}
	}
	target, _ := json.Marshal(struct {
		RunIDs []string `json:"run_ids"`
		Count  int      `json:"count"`
	}{bounded, len(ids)})
	return string(target)
}

func firstAuditTarget(values map[string]any, keys []string) string {
	for _, key := range keys {
		if key == "run_ids" {
			if target := auditRunIDs(values[key]); target != "" {
				return target
			}
			continue
		}
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

	tools := []Tool{
		{
			Name: "wingthing_capabilities", Title: "Wingthing capabilities",
			Description: "Discover supported and installed agent CLIs plus the local runtime primitives available on this machine.",
			InputSchema: objectSchema(map[string]any{}), Annotations: readOnly,
			Grant: "capabilities.read", Surfaces: both,
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
			Description: "List live Wingthing sessions with stable IDs, labels, process kind, agent, activity, working directory, and hook-derived status: working, blocked, idle, done, exited, or unknown. Older eggs and unsupported agents report unknown. Defaults to local sessions; remote selects one configured SSH name on an unrestricted MCP connection.",
			InputSchema: objectSchema(map[string]any{
				"remote": stringProperty("Configured SSH remote name; omit for local sessions only"),
			}), Annotations: readOnly,
			Grant: "terminal.read", Surfaces: both, AuditTargetKeys: []string{"remote"},
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
			Name: "session_status", Title: "Read native session lifecycle",
			Description: "Read native interactive agent state, exact provider identity, readiness, and the durable event head. Unsupported providers report unknown; terminal silence is never completion. Archived sessions remain readable.",
			InputSchema: objectSchema(map[string]any{"session": stringProperty("Wingthing session ID or unique label/prefix")}, "session"), Annotations: readOnly,
			Grant: "terminal.read", Surfaces: both, AuditTargetKeys: []string{"session"},
		},
		{
			Name: "session_read", Title: "Read session conversation events",
			Description: "Read bounded exact-provider native transcript and lifecycle events after a durable cursor. Raw provider records are included when within bounds. Completion refers to the foreground turn, separately from process survival.",
			InputSchema: objectSchema(map[string]any{
				"session":      stringProperty("Wingthing session ID or unique label/prefix"),
				"after_cursor": map[string]any{"type": "integer", "minimum": 0, "default": 0},
				"limit":        map[string]any{"type": "integer", "minimum": 1, "maximum": 200, "default": 50},
			}, "session"), Annotations: readOnly,
			Grant: "terminal.read", Surfaces: both, AuditTargetKeys: []string{"session"},
		},
		{
			Name: "session_wait", Title: "Wait for native session evidence",
			Description: "Wait for native lifecycle evidence or events after a durable cursor, with explicit matched/timed_out. State ready means the native provider reported session initialization, not a screen guess. Use after_cursor to exclude an earlier completed turn.",
			InputSchema: objectSchema(map[string]any{
				"session":         stringProperty("Wingthing session ID or unique label/prefix"),
				"after_cursor":    map[string]any{"type": "integer", "minimum": 0, "default": 0},
				"state":           map[string]any{"type": "string", "enum": []string{"ready", "starting", "working", "idle", "completed", "needs_input", "failed", "unknown"}},
				"timeout_seconds": map[string]any{"type": "number", "minimum": 0.1, "maximum": 3600, "default": 30},
			}, "session"), Annotations: readOnly,
			Grant: "terminal.read", Surfaces: both, AuditTargetKeys: []string{"session"},
		},
		{
			Name: "session_prompt", Title: "Submit retry-safe native session prompt",
			Description: "Reserve a caller request ID before submitting one prompt to a natively ready exact-provider interactive session. Identical retries never resend; changed arguments are rejected. Report native_receipt_observed only when exact provider human-user transcript text matches after the reservation cursor. This is a text receipt, not a provider request-specific causal acknowledgement. Timeout or lost connection remains unconfirmed. not_sent with definitely_not_sent=true requires explicit proof that no input attempt occurred; use a new request ID for a deliberate later submission.",
			InputSchema: objectSchema(map[string]any{
				"session":         stringProperty("Wingthing session ID or unique label/prefix"),
				"request_id":      map[string]any{"type": "string", "minLength": 1, "maxLength": 128, "description": "Unique caller retry ID; identical retries reuse the ID and all arguments"},
				"input":           map[string]any{"type": "string", "minLength": 1, "maxLength": 65536, "description": "UTF-8 prompt (65536 byte runtime bound); multiline text is pasted as one submission"},
				"timeout_seconds": map[string]any{"type": "number", "minimum": 0.1, "maximum": 60, "default": 15},
			}, "session", "request_id", "input"), Annotations: modelCall,
			Grant: "terminal.send", Surfaces: both, AuditTargetKeys: []string{"session"},
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
			Name: "session_fork", Title: "Fork a provider session",
			Description: "Start a new named Claude session from an owned live or ended provider conversation without changing the source. Inherits its workspace, model and captured egg policy; linked conversations become siblings.",
			InputSchema: objectSchema(map[string]any{
				"session": stringProperty("Source session ID or label"),
				"name":    stringProperty("New session name; omit for a generated name"),
			}, "session"), Annotations: mutating,
			Grant: "terminal.start", Surfaces: both, AuditTargetKeys: []string{"session"},
		},
		{
			Name: "terminal_start", Title: "Start persistent terminal",
			Description: "Start a durable shell or command terminal with no deadline under the MCP server's declared isolation mode and return immediately with its session ID.",
			InputSchema: objectSchema(map[string]any{
				"command": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "default": []string{}, "description": "Executable and arguments; omit to start $SHELL"},
				"cwd":     stringProperty("Working directory; defaults to the MCP server's current directory"),
				"label":   stringProperty("Optional stable human-readable session label"),
			}), Annotations: mutating,
			Grant: "terminal.start", Surfaces: both, AuditTargetKeys: []string{"session"},
		},
		{
			Name: "agent_start", Title: "Start persistent agent terminal",
			Description: "Start a supported agent in a durable PTY with no deadline under the MCP server's declared isolation mode and return immediately with its session ID.",
			InputSchema: objectSchema(map[string]any{
				"agent":                  stringProperty("Supported agent name; required unless resume_session is set"),
				"model":                  stringProperty("Provider model name, such as opus or gpt-5.6-terra"),
				"cwd":                    stringProperty("Working directory; defaults to the MCP server's current directory"),
				"label":                  stringProperty("Optional stable human-readable session label"),
				"unattended":             map[string]any{"type": "boolean", "description": "Enable the agent's unattended permission mode", "default": false},
				"scoped_mcp":             map[string]any{"type": "boolean", "description": "Inject one wing-owned scoped mailbox into a sandboxed Claude parent"},
				"conversation_role":      map[string]any{"type": "string", "enum": []string{"parent", "child"}, "description": "Create a persistent personal conversation; Claude currently supported"},
				"parent_conversation_id": stringProperty("Owned logical parent conversation ID; inherited by conversation-bound MCP connections"),
				"request_id":             stringProperty("Required unique retry key for linked conversation launches; identical retries reuse the same execution"),
				"resume_session":         stringProperty("Ended owned Claude root execution to continue; send conversation_role parent, input and request_id without other launch fields"),
				"input":                  stringProperty("Exact follow-up message for resume_session, at most 64 KiB"),
				"args": map[string]any{
					"type": "array", "items": map[string]any{"type": "string"}, "default": []string{},
					"description": "Extra arguments passed to the agent CLI verbatim, after Wingthing's own flags. Use the agent's native syntax, for example [\"--model\",\"sonnet\"] for claude or [\"-m\",\"gpt-5.6-terra\"] for codex.",
				},
			}), Annotations: modelCall,
			Grant: "terminal.start", Surfaces: both, AuditTargetKeys: []string{"session"},
		},
		{
			Name: "agent_run", Title: "Run agent task",
			Description: "Durably admit an agent run in a wing-owned attachable egg and return its owner-scoped run and session IDs. Use agent_wait and agent_result to observe the foreground turn.",
			InputSchema: objectSchema(map[string]any{
				"prompt":          stringProperty("Task for the agent"),
				"idempotency_key": stringProperty("Optional owner-scoped retry key for durable admission"),
				"agent":           stringProperty("Supported agent name, such as codex or claude"),
				"model":           stringProperty("Provider model name, such as gpt-5.6-terra or opus"),
				"cwd":             stringProperty("Working directory; defaults to the MCP server's current directory"),
				"label":           stringProperty("Short human-readable purpose recorded with the run"),
				"timeout_seconds": map[string]any{
					"type": "integer", "default": 0,
					"anyOf":       []any{map[string]any{"const": 0}, map[string]any{"minimum": 10}},
					"description": "Provider process timeout in seconds; omit or use 0 for no deadline. Positive values must be at least 10; no upper cap.",
				},
			}, "prompt", "agent"), Annotations: modelCall,
			Grant: "agent.run", Surfaces: both, AuditTargetKeys: []string{"run_id"},
		},
		{
			Name: "agent_status", Title: "Get agent run status",
			Description: "Read bounded lifecycle metadata for one run owned by this MCP principal, including its deadline or no deadline.",
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
			Name: "agent_wait_any", Title: "Wait for any agent run",
			Description: "Wait until any of 1-64 owned agent runs reaches a terminal state. Return finished runs and pending IDs; on timeout return no finished runs without an error. Unknown or foreign IDs appear only as indistinguishable errors. max_wait_hint_seconds: 110; keep each call under 110 seconds to stay within client tool-call timeouts and loop over pending IDs.",
			InputSchema: objectSchema(map[string]any{
				"run_ids": map[string]any{
					"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1, "maxItems": 64,
					"description": "Wingthing agent run IDs owned by this caller",
				},
				"timeout_seconds": map[string]any{"type": "number", "minimum": 0.1, "maximum": 600, "default": 30},
			}, "run_ids"), Annotations: readOnly,
			Grant: "agent.read", Surfaces: both, AuditTargetKeys: []string{"run_ids"},
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
				"cursor": map[string]any{"type": "integer", "minimum": 0, "description": "Replay older events before this cursor"},
			}, "run_id"), Annotations: readOnly,
			Grant: "agent.read", Surfaces: both, AuditTargetKeys: []string{"run_id"},
		},
		{
			Name: "agent_steer", Title: "Steer agent run",
			Description: "Queue an owner-scoped follow-up run that receives the prior request and result plus new direction.",
			InputSchema: objectSchema(map[string]any{
				"run_id":          stringProperty("Run to follow up"),
				"idempotency_key": stringProperty("Stable retry key for the follow-up"),
				"prompt":          stringProperty("New direction for the agent"),
				"model":           stringProperty("Optional model override for the follow-up"),
			}, "run_id", "prompt"), Annotations: modelCall,
			Grant: "agent.run", Surfaces: both, AuditTargetKeys: []string{"run_id"},
		},
		{
			Name: "agent_stop", Title: "Stop agent run",
			Description: "Durably cancel an owner-scoped run and its unstarted follow-ups, then await egg process-group cleanup and report surviving descendants.",
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
			Name: "wing_list", Title: "List available wings",
			Description: "List wings available through this MCP adapter, including their stable wing IDs and control transport. Local stdio lists its personal wing.",
			InputSchema: objectSchema(map[string]any{}), Annotations: readOnly,
			Grant: "wing.read", Surfaces: []Surface{SurfaceLocalMCP, SurfaceHTTPMCP, SurfaceDirectMCP}, Authority: AuthorityPortal,
		},
	}
	for index, tool := range tools {
		if tool.Name == "wing_list" {
			prefix := append([]Tool(nil), tools[:index]...)
			prefix = append(prefix, conversationTools()...)
			tools = append(prefix, tools[index:]...)
			break
		}
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
