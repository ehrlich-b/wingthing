# wingthing

[![ci](https://github.com/ehrlich-b/wingthing/actions/workflows/ci.yml/badge.svg)](https://github.com/ehrlich-b/wingthing/actions/workflows/ci.yml)

Wingthing is an agent manager for agents. Give Codex, Claude, or another parent
agent one typed control plane for starting and supervising durable agents across
all your machines. A person can inspect or take over the same sessions from a
terminal or browser.

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

To give the parent agent one qualified inventory across remote wings, log in on
the client machine and use the direct connector instead:

```bash
wt login

# Codex
codex mcp add wingthing -- wt mcp connect --client codex

# Claude Code
claude mcp add --scope user wingthing -- wt mcp connect --client claude
```

The agent calls `wing_list`, then supplies `wing_id` to every wing-owned tool.
`wingthing.ai` authenticates the peers, returns the access-filtered directory,
and carries the WebRTC offer/answer. MCP payloads travel directly to the selected
wing. The first native release expects a shared LAN or tailnet unless ICE servers
are configured. It never silently falls back to the hosted relay.

Direct control has explicit wing-side grants and per-principal spawn/session bounds.
The compatible defaults require no config; operators can narrow grants, change bounds,
or disable native control under `direct_mcp` in `wing.yaml`. Organization members
remain owner- and path-scoped, while owners/admins retain all configured paths. See
the [security model](docs/security.md#native-direct-mcp-authority) for the policy shape.

Hosted terminal relay is a separate transport decision. Existing configs remain
compatible, while a wing can refuse relayed payloads regardless of account
entitlement:

```bash
wt wing config set hosted_relay=deny
wt stop && wt start
```

Direct discovery/signaling still works; terminal and general control payloads must go
directly to the wing. The effective setting appears in wing capability metadata and
denials are audited without commands, paths, or payload content.

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

The product is an agent control plane over runtimes:

```text
parent agent: MCP --> agent manager --> selected wing --> sessions + runs
                                      ^
person: CLI or browser ----------------/  (inspect or take over)
```

| Term | Meaning |
| --- | --- |
| **Portal** | The unified inventory and controls exposed to an agent through MCP or to a person through the browser. |
| **Wing** | One machine running Wingthing. It owns the authoritative egg, terminal, and task state on that machine. |
| **Session** | A persistent interactive PTY for an agent, shell, or command. A person or model can detach and reattach. |
| **Run** | A supervised headless agent task with semantic status, events, output, and error data. It is separate from a PTY session. |
| **Egg** | The per-session execution boundary: process, PTY, sandbox policy, and local control socket. |
| **Roost** | The self-hosted bundle started by `wt roost start`: a portal/gateway and an embedded wing in one process. Other wings may register with its gateway. |

`wingthing.ai` is the hosted identity, directory, key-exchange, and connection
coordination service—roughly the control plane in a tailnet. It does not run the
agents. New free accounts use direct remote MCP; Pro adds the encrypted hosted
terminal and control relay. Local MCP, SSH, and self-hosted roosts do not require
it.

### Shared control contract

Local stdio, authenticated HTTP, and direct remote MCP derive their tool schemas
from one registry. The remote surface has no mutable current wing: every
wing-owned call requires `wing_id`, while `wing_list` is coordinator-owned. A
terminal created with MCP is the same durable egg a browser or CLI can inspect.

Headless runs do not yet have a browser view, locked wings still need a native
passkey ceremony, and independent self-hosted roosts do not yet federate their
directories. See the [direct agent manager design](docs/direct-agent-manager-design.md)
for the rollout and security boundary.

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

## Browser and hosted service

Browser access is optional:

```bash
wt login
wt start
open https://app.wingthing.ai
```

The wing connects outbound, so it needs no public inbound port. New free
accounts use the hosted site to register the wing and set up direct remote MCP;
the hosted browser terminal is not part of that free path. Pro and temporarily
grandfathered accounts may use the application-encrypted hosted terminal and
control relay. The coordinator sees account, routing, and connection metadata,
and the hosted browser still trusts JavaScript served by the service. Read
[security.md](docs/security.md) before making a stronger claim.

A portal may have several wings. The native CLI can query the same authorized
roster and probe each wing through the encrypted tunnel:

```bash
wt wings
wt wings --json

# Keep a separate state and login profile for another portal.
WINGTHING_DIR=~/.wingthing-lab wt login --roost https://lab.example.com
WINGTHING_DIR=~/.wingthing-lab wt wings --roost https://lab.example.com --json
```

The browser and native client select a wing by its stable `wing_id`. The hosted
directory aggregates every wing registered to the account or its organizations.
Peer-roost federation remains follow-up work.

## Shared roost

Run a self-hosted portal, gateway, and embedded wing in one process:

```bash
wt roost start --https
open https://localhost:8443
```

`--https` is an explicit, one-time local trust ceremony. WT creates a
localhost-only CA and server certificate on demand under
`~/.wingthing/local-tls`, keeps both private keys mode `0600` on this machine,
and installs only the public CA certificate in the current user's trust store.
The CA is name-constrained to localhost and loopback IPs. Inspect or undo the
trust change with `wt local-cert status` and `wt local-cert remove`.
macOS shows its native Certificate Trust Settings authorization dialog. Linux
uses the current user's Chromium NSS database and requires `certutil` from
`libnss3-tools` or `nss-tools`.
See [the local HTTPS design](docs/local_https.md) for the listener topology,
certificate lifecycle, and compatibility matrix.

Port 8443 is the browser listener. WT also keeps a loopback-only HTTP listener
on port 8080 for local wings and wings arriving through an SSH reverse tunnel;
it is not exposed to the LAN. Omit `--https` to preserve the previous local
HTTP behavior.

With an OAuth provider and public HTTPS URL, the same deployment becomes a
multi-user shared host. Each terminal and run has an owner, each OAuth client
has a separate actor ID, workspaces are bounded by `wing.yaml`, and provider
credentials live in the owner's agent home. Public/shared deployments continue
to terminate their externally provisioned HTTPS at Caddy, nginx, a VPN proxy,
Fly, or another ingress; they do not use WT's device-local CA.

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
