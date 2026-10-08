# Wingthing as an agent meta-access layer

Status: implemented local and self-hosted slices, with portal convergence in
design

Reviewed: 2026-08-28

## Thesis

Wingthing is the stable interface through which people and LLMs reach agent
runtimes. It is not another agent, a universal prompt format, or a cloud
scheduler.

Claude Code, Codex, Gemini, Hermes, OpenCode, Cursor, and Ollama keep their own
models, tools, context, and interaction styles. Wingthing gives them a small
common control plane:

- discover installed runtimes and their requirements;
- inspect the sandbox that will apply;
- start or reattach to a persistent terminal;
- submit a supervised headless run with semantic state;
- wait for, steer, stop, and read a run;
- repeat bounded work or execute a bounded dependency graph; and
- let a person and an LLM inspect the same owned resources.

That is the useful meaning of agent meta-access layer: access to agents without
pretending the agents are interchangeable.

## Object model

| Object | Meaning | Authority |
| --- | --- | --- |
| Portal | Client-facing inventory and controls in a browser, CLI, or MCP client | Adapter over a gateway and one or more wings |
| Wing | One execution runtime with local process, workspace, agent-home, terminal, and task state | Wing |
| Session | Persistent interactive PTY for an agent, shell, or command | Wing egg store |
| Run | Supervised headless agent task with semantic status, events, output, and errors | Wing task store |
| Egg | Per-session process, PTY, sandbox policy, and local control socket | Wing |
| Prompt asset | Named, versioned prompt plus variables and runtime defaults | Prompt store |
| Loop | Bounded sequence of prompt tasks | Task orchestrator |
| Swarm | Bounded dependency DAG of prompt tasks | Task orchestrator |
| Roost | Self-hosted portal/gateway with an embedded wing | One deployment |

A session is not a run. Terminal output is ANSI state and may contain a TUI,
shell, compiler, or model. A run has an explicit lifecycle and semantic result.
Use a session when a person may attach. Use a run when a caller needs reliable
task state.

This distinction prevents three category errors:

1. PTY activity does not mean working or done.
2. A group of people is not a swarm execution namespace.
3. A hosted route does not own the process or workspace.

## Human and LLM parity

The target is several thin clients over one control contract:

```text
human CLI ---------\
browser ------------+--> wing control contract --> sessions + runs
LLM through MCP ----/
```

No client should scrape another client's UI. A model should list sessions, read
a snapshot, wait for output, start an agent, or submit a graph through closed
schemas. A person should be able to inspect and interrupt the same resources.

The current implementation proves part of this:

- local stdio MCP and the CLI operate local wing state;
- local MCP sessions appear in the browser when that wing is connected to the
  selected portal;
- self-hosted HTTP MCP calls the roost's embedded wing with authenticated
  owner and actor identity; and
- native direct MCP selects external wings explicitly and carries the shared
  terminal, run, message, and sandbox operation subset over WebRTC; and
- the browser can aggregate sessions from several external wings registered to
  one gateway.

Two gaps remain. Headless runs have no browser view. HTTP MCP cannot select an
external wing from the portal roster and controls only the roost's embedded
wing.

## MCP interfaces

Register the local stdio server:

```bash
codex mcp add wingthing -- wt mcp stdio --client codex
claude mcp add --scope user wingthing -- wt mcp stdio --client claude
```

It implements MCP JSON-RPC over stdin and stdout. Protocol messages use standard
output; diagnostics use standard error. Tools use closed JSON Schemas and
return structured content plus a serialized text fallback.

The current local operation set is defined and tested in `internal/control`:

| Group | Tools |
| --- | --- |
| Discovery | `wingthing_capabilities`, `sandbox_explain` |
| Messages | `message_send`, `message_list`, `message_wait` |
| Sessions | `terminal_list`, `terminal_read`, `terminal_send`, `terminal_wait`, `terminal_start`, `agent_start`, `terminal_rename`, `terminal_stop` |
| Runs | `agent_run`, `agent_status`, `agent_wait`, `agent_wait_any`, `agent_result`, `agent_events`, `agent_steer`, `agent_stop` |
| Prompt workflows | `prompt_list`, `prompt_get`, `prompt_save`, `prompt_run`, `task_get`, `prompt_loop`, `swarm_run` |

Headless runs now have a dedicated detached `wt egg supervise-run` process,
independent of the submitting MCP host. The task store records its PID before
`agent_run` returns. Later clients of the same principal on the same wing use
the existing run ID for status, wait, results, events and cancellation. The
supervisor retains the submitted sandbox policy and caller's shared-host home
and path boundary. No PTY or browser terminal is created.

`agent_run.prompt` reaches the worker verbatim, without Wingthing's memory index,
daily thread or schedule/memory output instructions. Prompt and skill workflows
retain their existing assembly. Run labels accept human-readable text and are
stored unchanged in creation metadata and label events; filesystem paths use
generated run IDs. Isolation is resolved and frozen before creation, so the
initial response and later status agree with execution (`standard` by default,
or `privileged` under an explicit `--unsandboxed` outer boundary).

An explicit filesystem rule that masks the headless agent binary fails before
execution with the effective egg.yaml policy, cwd, binary and matching `deny:`
rule, plus a pointer to `sandbox_explain`. Seatbelt's `deny:/` masks all binaries;
Linux's `deny:/` is an allowlist jail and does not by itself deny mounted runtimes.
This diagnostic does not grant access or alter network policy: scalar
`network: none` still merges agent domains under the established policy contract.

`orphaned` is a new **terminal** run status: the supervisor was lost and the
provider's exit is unknown. `agent_wait` and `agent_wait_any` finish for it,
`agent_result.ready` is true, and received messages remain in `output` and
`agent_events` (`agent_message` events). A known provider failure stays
`failed`, carrying its final error and received agent messages. Losing the
supervisor does not prove the provider exited and does not restart it. Legacy
in-process runs are also reconciled to `orphaned` if their recorded host dies.
Tool names, arguments and existing result fields remain unchanged; callers
must recognize the additional terminal status. Old binaries cannot supervise
or stop these detached runs; upgrade the MCP host before using that lifecycle.

Codex `turn.failed`, `error` and completed error items contribute their final
provider message to the existing result `error` field, alongside process exit
diagnostics. A later completed turn clears transient retry errors. Secret egg
environment values travel in a 0600 one-shot file removed after reading, never
in the spawned wrapper's argv; the legacy internal `--env` input remains
accepted for compatibility, but launchers do not emit it.

The local MCP process has the operating-system authority of the user that
launched it. The client name controls ownership and audit attribution inside
Wingthing; it is not independent OS authentication. A mode-0600
`~/.wingthing/clients.yaml` can restrict client names, grants, and spawn
bounds.

A pre-isolated VM or container can use:

```bash
wt mcp stdio --client CLIENT --unsandboxed
```

This is a server-wide authority decision. Sessions and tasks then run with the
VM user's authority. Capabilities and audit rows report `outer-boundary`.

For a self-hosted roost with OAuth:

```bash
codex mcp add lab --url https://lab.example.com/mcp
codex mcp login lab
```

That HTTP endpoint currently controls the roost's embedded wing. Register
several independent roost URLs under distinct names if the parent LLM needs
several targets. There is no peer-roost discovery or federation.

## Loop and swarm semantics

A loop:

1. runs the base prompt;
2. stores the task and output;
3. adds the output to the next iteration as a dependency result;
4. stops if `until_contains` matches; and
5. otherwise stops at `max_iterations`, capped at 12.

The runtime guarantees dependency delivery and the hard bound. It does not
guarantee that a model follows instructions contained in a dependency result.

A swarm is a DAG:

```text
research-a --\
              +--> synthesis --> review
research-b --/
```

- Independent ready nodes may run concurrently.
- Dependency outputs are injected into downstream prompts.
- Unknown dependencies, duplicate IDs, self-dependencies, and cycles are
  rejected before a model starts.
- A failed node prevents dependent nodes from running.
- A graph is capped at 16 nodes and four workers.
- Every parent and child is a durable task record.

This is enough for map/reduce, independent investigation, synthesis, and staged
review without inventing a general workflow language.

## Safety and authority

LLM access must not mean invisible ambient authority.

- Tool annotations identify read-only, mutating, destructive, and open-world
  operations.
- Agent invocations use the same sandbox resolution as `wt run`.
- Timeouts cancel the process group, including descendants.
- Stderr is bounded.
- Loops, graph size, and concurrency have server-side caps.
- Each task records its absolute working directory and resolved isolation.
- Principals receive grants, spawn bounds, ownership, and audit attribution.

Local principals are useful protection against accidental cross-client access.
They do not constrain a malicious process that can read the same files or local
sockets. Remote transport authentication is also not sufficient by itself;
every operation still needs authorization and target policy.

## Placement belongs in the contract

An absolute `cwd` quietly binds execution to one workspace replica. Remote
workflows need to state five independent choices:

1. execution wing;
2. workspace identity and replica;
3. display or preview destination;
4. credential source; and
5. durable memory source.

Current Wingthing only routes execution. The working directory and untracked
files must already exist on the selected wing. The proposed workspace and
qualified resource model is in
[the LLM-first architecture review](llm-first-review.md).

## Next work

1. Continue extracting the handlers themselves into a transport-independent wing
   control package. The shared registry already owns operation metadata and schemas.
2. Put the contract behind a wing-owned local socket.
3. Converge the native direct WebRTC subset and the browser's bespoke encrypted
   tunnel onto one wing-owned request/response service.
4. Extend the direct connector's explicit `wing_id` qualification into one shared
   portal/session/run resource model; add `portal_id` when several portals can be
   aggregated by one client.
5. Give the browser a combined session and run inventory.
6. Add workspace, preview, credential, and durable-memory references without
   turning Wingthing into a mandatory file-sync product.

The product test is concrete: can an LLM operate Wingthing without scraping the
web UI, and can a person see, understand, and take over everything it did?

### Interactive native session lifecycle

`session_status`, `session_read`, and `session_wait` expose a persistent
interactive session independently of the ANSI terminal tools. CLI equivalents
are `wt session status`, `transcript`, and `await`, each with `--json`.
Every remote call still selects an explicit execution wing. These tools inherit
`terminal.read` ownership and path checks, including archived-session reads.

Claude records are bound to the provider session ID assigned at launch. Native
JSONL records and observational native hooks are imported into the private egg
`lifecycle.jsonl` journal with monotonic durable sequence numbers. Hook payloads
are atomically published inside Claude's existing writable provider directory;
launch-only `--settings` preserves supplied settings, existing hooks, and
`disableAllHooks`, without editing a host settings file or expanding the sandbox.
The provider home is persisted in egg metadata so isolated owner histories remain
separate. Provider records containing a different session ID are not disclosed.

Read responses contain `lifecycle.events`, `cursor` (last delivered event),
`head_cursor` (journal head), `state_cursor` (the event establishing current
state), and `has_more`. Save the delivered cursor and drain bounded pages. State
waits inspect `state_cursor` independently of transcript pagination. Pass the
head cursor captured before sending a new prompt when waiting for that turn,
so an earlier completed response cannot satisfy the wait. `matched` and
`timed_out` are explicit; cancellation still cancels the wait.

Readiness means the native provider reported session initialization or prompt
submission; it does not promise a terminal composer is visible. `prompt_submitted`
is the native UserPromptSubmit observation, before provider processing and other
user hooks, not a guarantee of eventual acceptance. `completed` from Claude is a
foreground `end_turn` or Stop observation, not completion of an arbitrary
delegated goal. Other Stop hooks may continue the conversation; background tasks
reported by Stop produce `idle` with an explicit reason. Later native prompt/tool
hooks restore `working`. Native hook transitions take precedence over delayed
transcript flushes, and durable process failure/cancellation overrides both.
An unknown process exit remains `unknown`, never success. Unsupported providers
(including Codex for now) declare unknown agent semantics and retain the existing
raw terminal tools. No status derives completion from PTY silence.

Fixture coverage verifies native hook command/stdin/spool/journal integration,
concurrent readers, exact same-directory identity, partial JSONL retry, reconnect
cursor replay, delayed transcript flushes, wait pagination, process cancellation,
owner/path enforcement and strict MCP arguments. Real authenticated provider
startup/composer behavior remains a separate opt-in canary; fixtures do not
establish vendor login behavior. Journal reads currently scan local history;
indexing and retention controls are follow-up work for very long conversations.

`session_prompt` / `wt session prompt --request-id ID --json` add a separate
semantic send path. The caller supplies stable request ID, exact destination,
text, and a bounded receipt wait (100 ms–60 s, 15 s default). Up to 64 KiB of
UTF-8 text is bracketed-pasted as one submission; unsupported terminal control
bytes are rejected. Raw `terminal_send` remains available for intentional
terminal interaction.

A session-scoped gate serializes requests. Wingthing persists and syncs the
request ID, exact-provider spec hash and drained native head cursor before any
transport attempt. Identical request retries never resend; changing the prompt,
destination or wait bound rejects reuse. A crash between reservation and delivery
remains explicitly unconfirmed. The input connection stays open through receipt
observation. An eager writer claim is confirmed before any input is enqueued;
the preview central writer lease excludes competing attachments until release.
Native readiness and the reserved provider identity are checked again while
this attachment owns input.
Stable retains its existing attachment behavior. Neither channel turns a text
match into a provider request-specific causal acknowledgement.

`receipt.native_receipt_observed` means an exact-provider, non-synthetic,
non-meta, non-summary, non-sidechain human user JSONL message matched the text
after reservation. A UserPromptSubmit hook, terminal echo or successful stream
write cannot satisfy it. `transport_enqueued` and byte count are separate
transport facts. After an input attempt, timeout, read failure or lost connection
preserves the reservation and reports `unconfirmed`; retry with the same arguments to inspect
for a late receipt. `provider_request_acknowledged` remains false: Claude does
not carry Wingthing's request ID, so identical concurrent human text cannot be
causally attributed to a particular request. Reservation provides at-most-once
sending through this control path, not global exactly-once execution or proof
that the provider accepted an arbitrary delegated goal.

A preflight refusal can return `status: "not_sent"` together with
`definitely_not_sent: true` only when the actual sender explicitly reports that
it never attempted input. A busy writer, changed readiness or replaced provider
before the first input send preserves a bounded actionable reason. The sender
clears this evidence immediately before attempting that send: zero enqueued
bytes, a send error or a crash cannot prove that input was unsent. Terminal
`not_sent` retries return the saved result without reading the current provider
or sending again. A person can retain the draft, resolve the condition and
deliberately submit with a new request ID; an existing ID never resends.
