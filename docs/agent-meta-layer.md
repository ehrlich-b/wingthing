# Wingthing as an agent meta-access layer

Status: implemented local and self-hosted slices, with portal convergence in
design

Reviewed: 2026-08-24

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

The current local surface has 27 tools:

| Group | Tools |
| --- | --- |
| Discovery | `wingthing_capabilities`, `sandbox_explain` |
| Messages | `message_send`, `message_list`, `message_wait` |
| Sessions | `terminal_list`, `terminal_read`, `terminal_send`, `terminal_wait`, `terminal_start`, `agent_start`, `terminal_rename`, `terminal_stop` |
| Runs | `agent_run`, `agent_status`, `agent_wait`, `agent_result`, `agent_events`, `agent_steer`, `agent_stop` |
| Prompt workflows | `prompt_list`, `prompt_get`, `prompt_save`, `prompt_run`, `task_get`, `prompt_loop`, `swarm_run` |

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

1. Extract one transport-independent wing control package from the MCP adapter.
2. Define each operation once with schema, grant, bound, annotations, and audit
   redaction.
3. Put the contract behind a wing-owned local socket.
4. Carry the same request and response through the encrypted external-wing
   tunnel.
5. Add explicit `portal_id` and `wing_id` resource references.
6. Give the browser a combined session and run inventory.
7. Add workspace, preview, credential, and durable-memory references without
   turning Wingthing into a mandatory file-sync product.

The product test is concrete: can an LLM operate Wingthing without scraping the
web UI, and can a person see, understand, and take over everything it did?
