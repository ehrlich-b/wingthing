# wingthing

[![ci](https://github.com/ehrlich-b/wingthing/actions/workflows/ci.yml/badge.svg)](https://github.com/ehrlich-b/wingthing/actions/workflows/ci.yml)

Wingthing is a local-first, agent-first manager for durable agent runs and
terminals. Start by giving Codex, Claude, or another parent agent typed MCP
control of agents on the same machine, using the code and provider login already
there. Add remote machines or a browser only when the work requires them.

The agents run where the code and hardware already live. Wingthing keeps their
terminals alive, records semantic runs as durable tasks, applies sandbox policy,
and gives each caller an owner, actor, grant set, bound, and audit trail.

https://github.com/user-attachments/assets/f1f04caf-4b07-4298-ba76-db5b226c38f2

Choose the smallest route that fits:

1. An agent manages local agents through stdio MCP.
2. A person starts a local sandboxed agent terminal.
3. An agent reaches another machine through direct remote MCP.
4. A person who needs a browser runs a self-hosted roost.
5. An entitled account may optionally use the hosted `wingthing.ai` browser relay.

## 1. Local agent control: stdio MCP

Install Wingthing, then register its local MCP server with the agent that will
coordinate the work:

```bash
curl -fsSL https://wingthing.ai/install.sh | sh

# Codex
codex mcp add wingthing -- wt mcp stdio --client codex

# Claude Code
claude mcp add --scope user wingthing -- wt mcp stdio --client claude
```

The installer verifies the release checksum and confirms that the downloaded
binary implements the command surface shown by the website before replacing an
existing installation.

Restart the client after registration. Ask it to call
`wingthing_capabilities` before it starts work. The model can then:

- discover installed agent CLIs and their runtime requirements;
- start a persistent agent terminal that a person can reattach to;
- submit a headless `agent_run` with a provider and model, then wait for its
  semantic result without parsing terminal output;
- coordinate bounded prompt loops and dependency graphs;
- exchange owner-scoped messages with another authenticated agent client; and
- inspect the effective sandbox before launching anything.

The child agents use existing project directories and provider credentials on
this computer. Wingthing does not clone the code or copy a provider login. The
local server uses the current OS user's authority. `--client` supplies ownership
and audit attribution inside Wingthing, not a new operating-system security
boundary. Optional grants and spawn bounds live in `~/.wingthing/clients.yaml`.

## 2. Local human terminal: sandboxed agent

Use the same runtime directly when a person wants the terminal:

```bash
cd /path/to/existing/project
wt egg claude --name research

# Ctrl+B Q detaches without stopping the process
wt attach research
```

The provider CLI must already be installed and authenticated for the current OS
user. The project must already exist. No Wingthing account or wing daemon is
required.

The CLI also exposes persistent shells, arbitrary commands, and raw terminal
operations:

```bash
wt terminal --name work                # persistent sandboxed shell
wt terminal --name api -- npm run dev  # persistent arbitrary command

# Ctrl+B Q detaches without stopping the process
wt attach                              # list live sessions
wt attach work                         # reattach by name or ID

wt session ps --json
wt session read api --json
wt session send api r --enter --json
wt session wait api --contains ready --json
wt session rename api frontend --json
wt session kill frontend --json
```

Terminal snapshots are ANSI state. Wingthing does not infer that an agent is
done because a string appeared on screen. Use `agent_run`, `agent_wait`, and
`agent_result` when the caller needs semantic task state.

## 3. Different machines: direct remote MCP

To give the parent agent one qualified inventory across remote wings, first log
in and start Wingthing on every execution machine. Install and authenticate each
provider CLI there as the OS user who will own its runs:

```bash
wt login
wt start
```

Then log in on the parent-agent machine and add the direct connector (if it is
also an execution wing, the commands above already satisfy its wing setup):

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

The direct connector is the route to choose when execution moves to another
machine. It does not provide a browser display. Use `agent_run` for a semantic
result, or `agent_start` when a person will later attach through the execution
machine's CLI or SSH.

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

### Place every run explicitly

| Question | Wingthing's contract |
| --- | --- |
| **Execution** | Select the wing that will run the process. Direct remote calls require its `wing_id`; Wingthing never substitutes another wing. |
| **Workspace** | Pass a `cwd` that already exists on that wing. Wingthing does not clone, create, or synchronize repositories. |
| **Display** | Use `agent_run` for semantic status and a final result without a live browser view. Use `agent_start` when the task needs a PTY that a person can attach to from the CLI, SSH, or an entitled/self-hosted browser. |
| **Credentials** | The provider CLI reads credentials from the execution owner's agent home on that wing. Shared hosts separate those homes by owner. Do not put provider tokens or SSH keys in prompts or MCP arguments. |
| **Durable memory** | Wingthing stores task, result, message, and thread records in `~/.wingthing/wt.db`, session state under `~/.wingthing/eggs`, and optional prompt memory under `~/.wingthing/memory`. Provider-native history stays in the provider home. These are wing-local unless the operator arranges replication. |

`WINGTHING_DIR` changes the `~/.wingthing` state root. A terminal survives client
detachment, but not an unplanned host restart. A run's record and result persist;
an active headless run still depends on the supervising Wingthing process.

`wingthing.ai` can supply identity, an access-filtered directory, key exchange,
and connection coordination for direct remote MCP. It does not run the agents or
carry direct MCP payloads. Local MCP, local terminals, SSH, and self-hosted roosts
do not require it.

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

On Linux, even `network: "*"` uses the route-less namespace and permits any TCP
target presented through HTTP CONNECT; it is not a general routed interface for
ordinary raw-socket clients, UDP, or programs that ignore proxy configuration.
CONNECT policies filter hosts rather than ports. See [sandbox limitations](docs/sandbox.md#network-protocol-coverage).

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

## 4. Browser visibility: self-hosted roost

When a person needs a browser, run the browser portal locally first:

```bash
wt roost start --https
open https://localhost:8443
```

This self-hosted route needs no Wingthing account or hosted-relay entitlement.
For a remote execution machine, keep the portal on localhost and carry its wing
connection over SSH; follow the
[self-hosted remote-browser recipe](patterns/personal-remote-wing/INSTRUCTIONS.md).
For several people, configure OAuth, HTTPS, and an exact enrollment list as
described below.

A portal may have several wings. The native CLI can query the same authorized
roster and probe each wing through the encrypted tunnel:

```bash
wt wings
wt wings --json

# Keep a separate state and login profile for another portal.
WINGTHING_DIR=~/.wingthing-lab wt login --roost https://lab.example.com
WINGTHING_DIR=~/.wingthing-lab wt wings --roost https://lab.example.com --json
```

The browser and native client select a wing by its stable `wing_id`. A
self-hosted gateway can list its embedded wing and other wings registered with
that same gateway. Independent roosts do not federate their directories.

### Self-hosted roost details

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
WT verifies the trust-store result before reporting the CA as installed; on
macOS it evaluates the generated leaf under the `localhost` SSL policy. A
failed or incomplete trust change is not cached as success.
See [the local HTTPS design](docs/local_https.md) for the listener topology,
certificate lifecycle, and compatibility matrix.

Port 8443 is the browser listener. WT also keeps a loopback-only HTTP listener
on port 8080 for local wings and wings arriving through an SSH reverse tunnel;
it is not exposed to the LAN. Omit `--https` to keep an HTTP browser origin, but
single-user/no-login mode still rewrites the implicit listener to
`127.0.0.1:8080` and refuses an explicitly non-loopback address. Authenticated
organization and hosted listeners keep their configured bind behavior.

Because local mode deliberately has no human login, WT also rejects non-loopback
Host headers, cross-origin browser mutations, and cross-origin browser WebSocket
handshakes. Native wings and CLI clients without browser Origin headers remain
compatible. These checks are defense in depth around the loopback-only bind, not
permission to publish a local-mode listener.

With an OAuth provider and public HTTPS URL, the same deployment becomes a
multi-user shared host. Each terminal and run has an owner, each OAuth client
has a separate actor ID, workspaces are bounded by `wing.yaml`, and provider
credentials live in the owner's agent home. Public/shared deployments continue
to terminate their externally provisioned HTTPS at Caddy, nginx, a VPN proxy,
Fly, or another ingress; they do not use WT's device-local CA.

OAuth proves identity; it does not decide who belongs on a private roost. Set an
exact email enrollment list on every shared roost:

```bash
export WT_ROOST_ALLOWED_EMAILS=alice@example.com,bob@example.com
```

Only those accounts may finish login, use tokens, see wings, or authorize MCP.
If the list is empty, any account accepted by the configured OAuth provider can
enroll. Set the list on every internet-reachable private roost unless the
provider or ingress already enforces the same membership boundary.

An LLM connects to its HTTP MCP endpoint:

```bash
codex mcp add lab --url https://lab.example.com/mcp
codex mcp login lab
```

This command shape follows the current
[Codex MCP documentation](https://learn.chatgpt.com/docs/extend/mcp). Claude Code
uses `claude mcp add --scope user --transport http lab
https://lab.example.com/mcp`.

## 5. Hosted browser relay: entitled and optional

Use the hosted browser only when an account already has hosted-relay access and
the selected wing's effective `hosted_relay` policy is `allow`:

```bash
wt login
wt start
open https://app.wingthing.ai
```

The wing connects outbound, so it needs no public inbound port. The hosted
browser terminal is not part of the free direct-MCP path. In hosted-browser mode,
the service relays application-encrypted terminal and control payloads, sees
account and routing metadata, and supplies the browser JavaScript. Read
[security.md](docs/security.md) for the exact boundary.

Account entitlement and wing policy are separate. A wing can refuse hosted
payload relay:

```bash
wt wing config set hosted_relay=deny
wt stop && wt start
```

Direct discovery and signaling still work. The effective setting appears in wing
capability metadata, and denials are audited without commands, paths, or payload
content.

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

Or build from source with Go 1.26.6+ and Node.js:

```bash
git clone https://github.com/ehrlich-b/wingthing.git
cd wingthing
make check
```

Update an installed binary with `wt update`.

## Documentation

- [Agent-facing Wingthing skill](SKILL.md)
- [Choose a usage pattern](https://wingthing.ai/patterns)
- [Web documentation](https://wingthing.ai/docs)
- [Historical LLM-first architecture review](docs/llm-first-review.md)
- [Test strategy and commands](docs/testing.md)
- [Agent meta-layer](docs/agent-meta-layer.md)
- [AI API surface](docs/ai-api-surface.md)
- [Security model](docs/security.md)

## License

MIT
