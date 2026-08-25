# wingthing

[![ci](https://github.com/ehrlich-b/wingthing/actions/workflows/ci.yml/badge.svg)](https://github.com/ehrlich-b/wingthing/actions/workflows/ci.yml)

Run Claude Code, Codex, Cursor, Gemini, Hermes, Ollama, and OpenCode behind one
local control plane. A person can use it from a terminal or browser. An LLM can
start, supervise, steer, and stop work through MCP.

The agents run where the code and hardware already live. Wingthing keeps their
terminals alive, records semantic runs as durable tasks, applies sandbox policy,
and gives each caller an owner, actor, grant set, bound, and audit trail.

https://github.com/user-attachments/assets/f1f04caf-4b07-4298-ba76-db5b226c38f2

## Give an agent access to your agents

Install Wingthing, then register its local MCP server with the agent that will
coordinate the work:

```bash
curl -fsSL https://wingthing.ai/install.sh | sh

# Codex
codex mcp add wingthing -- wt mcp stdio --client codex

# Claude Code
claude mcp add --scope user wingthing -- wt mcp stdio --client claude
```

Restart the client after registration. Ask it to call
`wingthing_capabilities` before it starts work. The model can then:

- discover installed agent CLIs and their runtime requirements;
- start a persistent agent terminal that a person can reattach to;
- submit a headless `agent_run` with a provider and model, then wait for its
  semantic result without parsing terminal output;
- coordinate bounded prompt loops and dependency graphs;
- exchange owner-scoped messages with another authenticated agent client; and
- inspect the effective sandbox before launching anything.

The local server uses the current OS user's authority. `--client` supplies
ownership and audit attribution inside Wingthing, not a new operating-system
security boundary. Optional grants and spawn bounds live in
`~/.wingthing/clients.yaml`.

## Use the same runtime yourself

```bash
wt terminal --name work               # persistent sandboxed shell
wt terminal --name api -- npm run dev  # persistent arbitrary command
wt egg claude --name research         # persistent sandboxed agent

# Ctrl+B Q detaches without stopping the process
wt attach                              # list live sessions
wt attach research                     # reattach by name or ID
wt attach research --remote box        # reattach over ordinary SSH
```

The CLI exposes the raw terminal layer for scripts:

```bash
wt session ps --json
wt session read api --json
wt session send api r --enter --json
wt session wait api --contains ready --json
wt session rename api frontend --json
wt session kill frontend --json
```

Terminal snapshots are ANSI state. Wingthing doesn't infer that an agent is
done because a string appeared on screen. Use `agent_run`, `agent_wait`, and
`agent_result` when the caller needs semantic task state.

## The runtime model

The product is easier to understand as a portal over runtimes:

```text
person: CLI or browser --\
                         +--> portal/control surface --> wing --> sessions + runs
LLM: MCP ---------------/
```

| Term | Meaning |
| --- | --- |
| **Portal** | A client-facing view and control surface. The hosted browser is at `app.wingthing.ai`; a self-hosted deployment serves its own browser and HTTP MCP endpoint. |
| **Wing** | One machine running Wingthing. It owns the authoritative egg, terminal, and task state on that machine. |
| **Session** | A persistent interactive PTY for an agent, shell, or command. A person or model can detach and reattach. |
| **Run** | A supervised headless agent task with semantic status, events, output, and error data. It is separate from a PTY session. |
| **Egg** | The per-session execution boundary: process, PTY, sandbox policy, and local control socket. |
| **Roost** | The self-hosted bundle started by `wt roost start`: a portal/gateway and an embedded wing in one process. Other wings may register with its gateway. |

`wingthing.ai` is the hosted portal and gateway. Personal wings may register
with it for browser reachability. It doesn't run those agents and isn't required
for local, MCP, or SSH use.

### Current convergence boundary

The browser and MCP surfaces share live session state when they address the same
wing. A terminal created with MCP appears in the web portal, and either client
can operate the same egg subject to ownership policy.

Two gaps remain:

- headless MCP runs live in the wing task store but have no browser view; and
- a self-hosted portal can display several registered wings, while its HTTP MCP
  tools currently control only the embedded wing.

Independent self-hosted portals are not federated. A client selects one by URL,
and an LLM can register several HTTP MCP servers under different names. See the
[LLM-first architecture review](docs/llm-first-review.md) for the proposed
single control contract and target model.

## Sandbox

Each egg resolves an `egg.yaml` policy. The defaults make the working directory
writable, mount home read-only, deny common credential directories, strip the
environment, and allow only the network domains required by the selected agent.

- macOS uses Seatbelt.
- Linux uses user and PID namespaces, mount isolation, seccomp, and cgroups when
  available.
- A local CONNECT proxy applies domain rules. Linux enforcement limits are
  documented in the [security model](docs/security.md).

Project policy is additive:

```yaml
# egg.yaml
fs:
  - "ro:~/.ssh"        # override the default deny for ~/.ssh
network:
  - "github.com"       # add a domain to the agent profile
env:
  - SSH_AUTH_SOCK       # pass the SSH agent socket
```

Use `wt egg explain <agent> --json` or the MCP `sandbox_explain` tool to inspect
the resolved boundary before launch. Put a default policy at
`~/.wingthing/egg.yaml`, or use `base: none` for a blank slate.

If the machine is already an access-segregated VM, `--unsandboxed` declares the
outer VM as the security boundary. Wingthing still provides persistence and the
control plane, reports `outer-boundary` to MCP clients, and records that mode in
the audit log.

## Browser access

Browser access is optional:

```bash
wt login
wt start
open https://app.wingthing.ai
```

The wing connects outbound, so it needs no inbound port. Terminal and wing API
payloads are application-encrypted between the shipped browser and wing. The
gateway sees routing metadata, and the hosted browser trusts JavaScript served
by the service. Read [security.md](docs/security.md) before making a stronger
claim.

A portal may have several wings. The native CLI can query the same authorized
roster and probe each wing through the encrypted tunnel:

```bash
wt wings
wt wings --json

# Keep a separate state and login profile for another portal.
WINGTHING_DIR=~/.wingthing-lab wt login --roost https://lab.example.com
WINGTHING_DIR=~/.wingthing-lab wt wings --roost https://lab.example.com --json
```

The browser and native client select a wing by its stable `wing_id`. There is no
peer-roost discovery or cross-portal session aggregation today.

## Shared roost

Run a self-hosted portal, gateway, and embedded wing in one process:

```bash
wt roost start
open http://localhost:8080
```

With an OAuth provider and public HTTPS URL, the same deployment becomes a
multi-user shared host. Each terminal and run has an owner, each OAuth client
has a separate actor ID, workspaces are bounded by `wing.yaml`, and provider
credentials live in the owner's agent home.

An LLM connects to its HTTP MCP endpoint:

```bash
codex mcp add lab --url https://lab.example.com/mcp
codex mcp login lab
```

This command shape follows the current
[Codex MCP documentation](https://learn.chatgpt.com/docs/extend/mcp). Claude Code
uses `claude mcp add --scope user --transport http lab
https://lab.example.com/mcp`.

## Supported agents

`wt doctor` reports what is installed. Interactive sessions support:

| Agent | CLI |
| --- | --- |
| Claude Code | `claude` |
| Codex | `codex` |
| Cursor Agent | `agent` |
| Gemini CLI | `gemini` |
| Hermes Agent | `hermes` |
| Ollama | `ollama` (`qwen3:4b` default) |
| OpenCode | `opencode` |

Support has several evidence levels: catalog, exact headless invocation,
synthetic PTY lifecycle, real sandbox startup, live model completion, and MCP
orchestration. See [supported agent evidence](docs/agent-support.md) for the
latest checked-in verification snapshot and [testing](docs/testing.md) for the
promotion policy.

## Install and build

```bash
curl -fsSL https://wingthing.ai/install.sh | sh
```

Or build from source with Go 1.25+ and Node.js:

```bash
git clone https://github.com/ehrlich-b/wingthing.git
cd wingthing
make check
```

Update an installed binary with `wt update`.

## Documentation

- [Choose a usage pattern](https://wingthing.ai/patterns)
- [Web documentation](https://wingthing.ai/docs)
- [LLM-first architecture review](docs/llm-first-review.md)
- [Test strategy and commands](docs/testing.md)
- [Agent meta-layer](docs/agent-meta-layer.md)
- [AI API surface](docs/ai-api-surface.md)
- [Security model](docs/security.md)

## License

MIT
