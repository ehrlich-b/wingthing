# wingthing

[![ci](https://github.com/ehrlich-b/wingthing/actions/workflows/ci.yml/badge.svg)](https://github.com/ehrlich-b/wingthing/actions/workflows/ci.yml)

Persistent, sandboxed agent terminals on machines you control. Use them locally,
reattach from your terminal over SSH, or opt into encrypted browser access.

https://github.com/user-attachments/assets/f1f04caf-4b07-4298-ba76-db5b226c38f2


```
wt terminal --name work              # persistent sandboxed shell
wt terminal --name api -- npm run dev # persistent arbitrary command
wt egg claude --name research        # persistent sandboxed agent
# Ctrl+B Q detaches without stopping it
wt attach                             # list live local sessions
wt attach --select                    # choose interactively
wt attach research                    # names or immutable IDs both work
wt attach research --remote box       # reattach over ordinary SSH
```

No account or hosted service is required. If you want browser access from
anywhere, `wt start` connects your machine outbound to the optional
`app.wingthing.ai` relay.

## Runtime and security domains

**The egg** is a sandboxed agent session on your machine. Each `wt egg <agent>` spawns a child process inside an OS-level sandbox (Seatbelt on macOS, user namespaces + seccomp on Linux). Same idea as containers but lighter weight. Filesystem access, network reach, system calls, and resource usage are all controlled.

**The wing** is the durable runtime on your machine. It owns eggs and their
session state even when no client is attached. Local and SSH clients do not need
a hosted service. When browser access is enabled, all traffic between the
browser and wing is E2E encrypted (X25519 + AES-GCM); the relay forwards
ciphertext it cannot read.

**A roost** is a deployment bundle: a wing plus the gateway and web service in
one process. Self-host one with `wt roost start` for a shared workstation, team
appliance, or homelab. `wingthing.ai` offers the gateway/web side as an optional
hosted service; it is not required for local or SSH use.

## Sandbox

Out of the box, the sandbox is opinionated: CWD is writable, home is read-only, sensitive directories (`~/.ssh`, `~/.gnupg`, `~/.aws`, etc.) are denied, and only essential env vars are passed through. A local [CONNECT proxy](https://en.wikipedia.org/wiki/HTTP_tunnel) enforces domain-level filtering - agents can only reach their own API, not the entire internet. Claude gets `api.anthropic.com`, Ollama gets `localhost`, Gemini gets `*.googleapis.com`. Agent binaries, config directories, network rules, and env vars are all auto-detected.

Drop an `egg.yaml` in your project to customize. Configs are additive - you only declare what you're changing from the defaults.

```yaml
# egg.yaml
fs:
  - "ro:~/.ssh"       # overrides the default deny for ~/.ssh
network:
  - "github.com"      # add a domain on top of agent defaults
env:
  - SSH_AUTH_SOCK      # pass SSH agent socket
```

Use `base: none` for a blank slate. Use the [sandbox builder](https://wingthing.ai) on the homepage to generate configs visually.

## Native reattach

`wt terminal` starts a shell or any command in the same durable PTY runtime used
for agents. Detach from `wt terminal` or `wt egg` with `Ctrl+B`, then `Q`. The
egg keeps running in its own process session.

```bash
wt terminal --name work
wt terminal --name dev-server -- npm run dev
wt egg claude --name research

wt attach                         # list local sessions
wt attach --select                # native interactive picker
wt attach work                    # attach by name or ID
wt attach work --remote box       # `box` comes from ~/.ssh/config
```

The remote host needs `wt` installed. SSH keeps ownership of authentication,
host verification, bastions, ports, and VPN/tailnet routing. Use
`--remote-binary /path/to/wt` if it is not on the remote `PATH`.

The local terminal API is also scriptable:

```bash
wt session ps --json
wt session read dev-server
wt session send dev-server r --enter
wt session wait dev-server --contains ready
wt session rename dev-server frontend
wt session kill frontend
```

LLMs and editor clients can use the same local runtime through MCP:

```bash
wt mcp stdio
```

It exposes typed tools for terminal list/read/send/wait, persistent agent start,
versioned prompt templates, one-shot runs, durable task inspection, bounded
loops, and dependency-aware swarms. It is local-user access: no account,
browser, or hosted router is
involved. See [the agent meta-layer design](docs/agent-meta-layer.md) and the
[supported-agent evidence matrix](docs/agent-support.md).

The opt-in release smoke battery model-swaps Claude Code, Codex, Hermes, and
OpenCode onto local Qwen models, runs each directly and through Wingthing, then
exercises MCP prompt, saved-prompt, loop, and swarm paths. It asserts exact
artifacts rather than trusting exit codes: see [live release E2E](docs/release-e2e.md).

Named prompts are ordinary local assets with immutable content revisions:

```bash
wt prompt save review --file review.prompt --variable target --agent codex --cwd "$PWD"
wt prompt list
wt prompt run review --var target=internal/parser
```

## Optional browser access

```
wt login                   # authenticate with GitHub or Google
wt start                   # background daemon
wt status                  # check it
wt stop                    # stop it
```

Open [app.wingthing.ai](https://app.wingthing.ai) to browse your wings, start
sessions, and view history. Lock your wing with `wt wing lock` to require
passkey auth before sessions start.

## Agents

`wt doctor` shows what's installed. Swap agents per-session.

| Agent | CLI |
|-------|-----|
| Claude Code | `claude` |
| Codex | `codex` |
| Cursor Agent | `agent` |
| Gemini | `gemini` |
| Hermes Agent | `hermes` |
| Ollama | `ollama` (`qwen3:4b` default) |
| OpenCode | `opencode` |

## Install

```bash
curl -fsSL https://wingthing.ai/install.sh | sh
```

Or build from source (Go 1.25+, Node.js):

```bash
git clone https://github.com/ehrlich-b/wingthing.git
cd wingthing && make check
```

Update with `wt update`.

## Self-hosting

Single binary, SQLite, no external deps.

```bash
wt roost start             # server + wing, one command
open localhost:8080         # start sessions
```

For multi-user, add GitHub or Google OAuth env vars. See the [docs](https://wingthing.ai/docs#self-hosting).

## Docs

[wingthing.ai/docs](https://wingthing.ai/docs)

## License

MIT
