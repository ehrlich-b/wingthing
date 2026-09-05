package control

func reviewJobTools() []Tool {
	object := func(properties map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	}
	text := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	integer := func(min, max int) map[string]any {
		return map[string]any{"type": "integer", "minimum": min, "maximum": max}
	}
	stringsArray := func() map[string]any {
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1, "maxItems": 32}
	}
	target := func() map[string]any {
		return object(map[string]any{"wing_id": text("Explicit execution wing ID"), "source": text("Canonical pre-existing source replica"), "model": text("Provider model")}, "wing_id", "source", "model")
	}
	spec := object(map[string]any{
		"request_id": text("Caller-chosen idempotency key, 8-80 letters, digits, hyphens or underscores"),
		"prompt":     text("One bounded implementation task"), "base_commit": text("Full 40-hex source commit on both replicas"),
		"implementer": target(), "reviewer": target(), "allowed_paths": stringsArray(), "test_argv": stringsArray(),
		"max_revisions": integer(0, 2), "run_seconds": integer(10, 3600), "test_seconds": integer(1, 1800), "timeout_seconds": integer(10, 14400),
	}, "request_id", "prompt", "base_commit", "implementer", "reviewer", "allowed_paths", "test_argv", "max_revisions", "run_seconds", "test_seconds", "timeout_seconds")
	change := object(map[string]any{"path": text("Exact relative file path"), "content": text("Base64 file bytes"), "delete": map[string]any{"type": "boolean"}, "executable": map[string]any{"type": "boolean"}}, "path")
	candidate := object(map[string]any{"base_commit": text("Full pinned commit"), "patch": text("Exact Git patch"), "sha256": text("SHA-256 of patch bytes"), "files": map[string]any{"type": "array", "maxItems": 32, "items": change}}, "base_commit", "patch", "sha256", "files")
	run := object(map[string]any{"prompt": text("Agent task"), "agent": text("codex"), "model": text("Admitted model"), "cwd": text("Must be empty; worker owns placement"), "label": text("Run label"), "timeout_seconds": integer(10, 3600)}, "prompt", "agent", "model", "timeout_seconds")
	read := map[string]any{"readOnlyHint": true, "destructiveHint": false, "openWorldHint": false}
	mutate := map[string]any{"readOnlyHint": false, "destructiveHint": false, "openWorldHint": true}
	surfaces := []Surface{SurfaceHTTPMCP, SurfaceDirectMCP}
	return []Tool{
		{Name: "review_job_submit", Title: "Submit implementation/review job", Description: "Admit one bounded, VM-owned implement/test/review workflow and immediately return a durable idempotent job ID. Requires explicit personal-wing operator policy; unavailable on shared or org hosts.", InputSchema: spec, Annotations: mutate, Grant: "review.run", Surfaces: surfaces, AuditTargetKeys: []string{"job_id", "request_id"}},
		{Name: "review_job_status", Title: "Inspect implementation/review job", Description: "Read owner-scoped lifecycle, child run IDs and artifact digests. An abandoned nonterminal coordinator record is reported as interrupted, never replayed.", InputSchema: object(map[string]any{"job_id": text("Durable coordinator job ID")}, "job_id"), Annotations: read, Grant: "review.read", Surfaces: surfaces, AuditTargetKeys: []string{"job_id"}},
		{Name: "review_job_result", Title: "Retrieve implementation/review evidence", Description: "Retrieve status or one bounded round artifact: exact patch, tests, independent review, or implementation output.", InputSchema: object(map[string]any{"job_id": text("Durable job ID"), "artifact": map[string]any{"type": "string", "enum": []string{"patch", "tests", "review", "implementation"}}, "round": integer(0, 2)}, "job_id"), Annotations: read, Grant: "review.read", Surfaces: surfaces, AuditTargetKeys: []string{"job_id"}},
		{Name: "review_workspace", Title: "Operate pinned review workspace", Description: "Prepare an owner-scoped replica, run an existing semantic agent with protected Git metadata, capture/apply an exact bounded patch, or run admitted tests without provider credentials or network. Explicit personal-wing operator policy is required.", InputSchema: object(map[string]any{"action": map[string]any{"type": "string", "enum": []string{"prepare", "capture", "apply", "run", "test"}}, "workspace_id": text("Worker-owned workspace handle"), "source": text("Operator-allowed source replica, prepare only"), "spec": spec, "candidate": candidate, "run": run}, "action"), Annotations: mutate, Grant: "review.workspace", Surfaces: surfaces, AuditTargetKeys: []string{"workspace_id"}},
	}
}
