package control

func conversationTools() []Tool {
	schema := func(p map[string]any, required ...string) map[string]any {
		out := map[string]any{"type": "object", "properties": p, "additionalProperties": false}
		if len(required) > 0 {
			out["required"] = required
		}
		return out
	}
	str := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	integer := func(max int) map[string]any { return map[string]any{"type": "integer", "minimum": 0, "maximum": max} }
	surfaces := []Surface{SurfaceLocalMCP, SurfaceHTTPMCP, SurfaceDirectMCP}
	read := map[string]any{"readOnlyHint": true, "destructiveHint": false, "openWorldHint": false}
	return []Tool{
		{Name: "conversation_bootstrap", Title: "Get parent MCP configuration", Description: "Return a reproducible local Claude MCP configuration bound to this owned task tree and current state directory. Does not add grants, mount files, or copy credentials.", InputSchema: schema(map[string]any{"conversation_id": str("Logical conversation ID")}, "conversation_id"), Annotations: read, Grant: "terminal.read", Surfaces: surfaces, AuditTargetKeys: []string{"conversation_id"}},
		{Name: "conversation_list", Title: "List persistent conversations", Description: "List owner-scoped logical parent and child conversations with exact wing and execution references. Does not launch agents.", InputSchema: schema(map[string]any{}), Annotations: read, Grant: "terminal.read", Surfaces: surfaces},
		{Name: "conversation_read", Title: "Read conversation task tree", Description: "Reconcile linked sessions and read durable child state deliveries after a cursor. Events remain replayable until explicitly checkpointed; read does not acknowledge them.", InputSchema: schema(map[string]any{"conversation_id": str("Logical conversation ID"), "after_cursor": integer(2147483647), "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 50}}, "conversation_id"), Annotations: read, Grant: "terminal.read", Surfaces: surfaces, AuditTargetKeys: []string{"conversation_id"}},
		{Name: "conversation_checkpoint", Title: "Checkpoint parent intent", Description: "Persist a bounded working checkpoint and acknowledged event cursor using compare-and-swap revision. This does not prompt or wake a provider.", InputSchema: schema(map[string]any{"conversation_id": str("Logical conversation ID"), "expected_revision": integer(2147483647), "after_cursor": integer(2147483647), "checkpoint": map[string]any{"type": "string", "maxLength": 32768}}, "conversation_id", "expected_revision", "after_cursor", "checkpoint"), Annotations: map[string]any{"readOnlyHint": false, "destructiveHint": false, "openWorldHint": false}, Grant: "terminal.send", Surfaces: surfaces, AuditTargetKeys: []string{"conversation_id"}},
		{Name: "conversation_wake", Title: "Configure parent child-state wake delivery", Description: "Explicitly opt an owned personal root into host delivery of bounded native child attention/completion observations. Omit enabled to inspect status. Explicit retry_not_sent permits another bounded cycle only after retained proof of no input; ambiguous delivery never retries. Delivery uses exact native prompt receipts, never answers permissions, and is independent from checkpoint acknowledgement.", InputSchema: schema(map[string]any{"conversation_id": str("Owned root conversation ID"), "enabled": map[string]any{"type": "boolean"}, "retry_not_sent": map[string]any{"type": "boolean", "description": "Explicitly authorize a further known-not-sent retry cycle; never allowed for pending or unconfirmed delivery"}}, "conversation_id"), Annotations: map[string]any{"readOnlyHint": false, "destructiveHint": false, "openWorldHint": false}, Grant: "terminal.send", Surfaces: surfaces, AuditTargetKeys: []string{"conversation_id"}},
	}
}
