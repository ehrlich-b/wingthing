# Headless to TUI handoff

Status: design, not implemented
Reviewed: 2026-08-09

## The workflow

Fire an agent headlessly, let it work, then jump into the *same conversation* in
a terminal when it needs a human:

```bash
wt run "fix the failing store test"      # headless, sandboxed, returns a task ID
wt attach --resume t-20260809-...        # TUI, same conversation, mid-thread
```

## Why it does not work today

The two halves exist. The link between them does not.

**Headless works.** Every adapter in `internal/agent/` has an exact
non-interactive invocation:

| Agent | Headless invocation | Structured output |
|---|---|---|
| claude | `claude -p <prompt> --output-format stream-json --verbose` | yes |
| codex | `codex exec <prompt> --json --skip-git-repo-check` | yes |
| cursor | `agent -p <prompt> --output-format stream-json` | yes |
| gemini | `gemini -p <prompt> --model <m>` | text |
| hermes | `hermes -z <prompt>` (`-t` toolsets) | text |
| opencode | `opencode run <prompt> [--dir]` | text |
| ollama | `ollama run <model>` via stdin | native structured tools |

**Resume works.** `internal/agent/catalog.go` declares a `ResumeFlag` for six of
seven agents, and `InteractiveInvocation()` already builds the resume argv
(including codex's `resume` subcommand form). `wt egg <agent> --resume <id>`
spawns it in a PTY.

**The handoff does not.** A headless run never learns or records the agent's own
session ID, so there is nothing to pass to `--resume`. Specifically:

- `internal/egg/agents.go` declares `SessionIDFlag: "--session-id"` for claude
  only, and `internal/agent/claude.go` does not pass it.
- The other six have no ID-injection flag at all. Their profiles carry
  `SessionDir` (`.codex/sessions`, `.gemini/tmp`, `.local/share/opencode`,
  `.hermes`, `.claude/projects`), which is the obvious hook for after-the-fact
  discovery, but nothing reads it.
- The task record persists `CWD`, `PromptName`, and `PromptRevision`. It has no
  column for the agent's session ID.

So today a headless run is a dead end: you get output, not a resumable thread.

## Design

### Two capture strategies

**Inject (claude).** Mint a UUID before the run, pass `--session-id <uuid>`.
Deterministic, no filesystem race, no discovery. This is the preferred path and
should be used wherever an agent offers it.

**Discover (codex, gemini, opencode, hermes, cursor).** Snapshot the profile's
`SessionDir` immediately before the run, diff after, and take the new entry.

Discovery is inherently racy — a concurrent session in the same home can create a
second entry. Mitigations, in order of preference:

1. Give each headless run its own `HOME` (the sandbox already supports a per-user
   home override via `Config.UserHome`), so the `SessionDir` diff has exactly one
   candidate by construction.
2. Failing that, pick the newest entry by mtime and record a `confidence` field.
3. Record nothing rather than a guess. A wrong session ID resumes the wrong
   conversation, which is worse than no handoff.

Ollama has no session concept. It should declare that and return an explicit
"resume unsupported" rather than a silent empty ID.

### Capability declaration, not a name switch

`Definition` should stop implying that every agent supports every mode. Add
declared capabilities so the runtime picks the richest available path and callers
can see what they are getting:

```go
type Capabilities struct {
    InteractivePTY   bool
    HeadlessText     bool
    StructuredEvents bool   // stream-json / --json
    SessionInject    bool   // can be told its session ID up front
    SessionDiscover  bool   // writes a discoverable session file
    Resume           bool
    ACP              bool   // speaks Agent Client Protocol
}
```

This is the shape `docs/agent-support.md` already argued for. It also makes the
matrix testable: a capability claim without a test is a lie.

### Storage

One migration, one column, plus provenance for how it was obtained:

```sql
-- 008_agent_session.sql
ALTER TABLE tasks ADD COLUMN agent_session_id TEXT;
ALTER TABLE tasks ADD COLUMN agent_session_source TEXT;  -- 'inject' | 'discover' | 'none'
```

It sits naturally beside the existing `prompt_name` / `prompt_revision`
provenance columns from `007_prompt_provenance.sql`.

### Surface

Per `ai-api-surface.md`, the capability is defined once and exposed three ways:

- CLI: `wt run` prints the task ID and resumability; `wt attach --resume <task-id>`
  resolves task to agent + session ID and spawns the PTY.
- MCP: `task_get` returns `agent_session_id` and `resumable`; a new
  `agent_resume` tool starts a terminal from a task.
- REST: `GET /api/v1/tasks/{id}` carries the same fields.

Note the indirection: callers should pass the **task ID**, not the agent's
session ID. The task ID is ours, stable, and already the unit of provenance. The
agent's session ID is a vendor detail that should not leak into the CLI surface.

## The better substrate: ACP

The per-agent capture work above is worth doing because it unblocks the workflow
with the CLIs as they exist. But it is a workaround for the absence of a
protocol, and that absence is ending.

The **Agent Client Protocol** (ACP) is Zed's open JSON-RPC standard for
connecting editors to coding agents. Introduced August 2025, co-launched with
JetBrains in January 2026, and the headline feature of Zed 1.0 in April 2026. The
registry lists 25+ agents including Claude Code, Codex, Gemini CLI, Copilot,
Cursor, Goose, OpenCode, and Qwen Code.

ACP answers this design's problem directly:

- `session/new` returns a session ID — no minting, no discovery, no race.
- `session/load` replays the **entire conversation history** as typed
  `session/update` notifications (`user_message_chunk`, `agent_message_chunk`),
  then confirms completion.
- `session/resume` reconnects without replay when history is not needed.
- Agents advertise `loadSession` and `sessionCapabilities.resume`, so the client
  can branch on real capability instead of a hardcoded table.

That also answers the second question worth asking here: *can we pull what is
happening in a conversation programmatically?* With ACP, yes, as typed events.
Scraping a fixed-size TUI gives rendered text, not turns, tool calls, or
completion state — exactly the screen-scraping failure mode
`local-first-architecture.md` warns against.

**Recommendation:** treat ACP as the target and the per-agent capture as the
bridge. An `acp` capability on `Definition`, an ACP client in `internal/agent/`,
and the same task-ID indirection above means the surface never changes when an
agent graduates from stream-json to ACP.

## Test plan

Per the three-tier bar in `CLAUDE.md`, at minimum:

- **Unit** — exact argv for injection (`claude -p … --session-id <uuid>`);
  `SessionDir` diff logic including the zero-new, one-new, and two-new cases;
  capability gating so an agent without resume returns an explicit refusal.
- **Integration** — run a mock agent that writes a session file, assert the task
  record captures the right ID and source; assert a second concurrent run in a
  shared home does not cross-attribute.
- **E2E** — real headless run, then real `--resume` in a PTY, asserting the
  resumed session shows prior conversation content. Gated as an opt-in canary
  behind the existing `*-real` binaries, since it needs credentials.

The e2e is the one that matters. Everything above it can pass while the actual
vendor flag has changed underneath.

## Phasing

1. Capability struct and per-agent declarations, with tests. No behavior change.
2. Injection path for claude. Smallest real win, deterministic.
3. Storage columns and `wt attach --resume <task-id>`.
4. Discovery path for the rest, with per-run home isolation.
5. ACP client behind the same capability gate.
