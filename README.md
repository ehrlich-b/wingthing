# wingthing

[![cinch](https://cinch.sh/badge/github.com/ehrlich-b/wingthing.svg)](https://cinch.sh/jobs/github.com/ehrlich-b/wingthing)

Run AI agents sandboxed on your machine. Access them from anywhere.

```
wt egg claude              # sandboxed Claude Code session
wt wing -d                 # make your machine reachable remotely
open app.wingthing.ai      # start terminals from your browser
```

## Sandboxed agents

`wt egg` runs any supported agent inside an isolated sandbox with full terminal support. The sandbox IS the permission boundary — agents get `--dangerously-skip-permissions` because the sandbox constrains what they can do.

Each session is its own process with its own sandbox. Detach and reattach without losing state.

```
wt egg claude              # Claude Code, sandboxed
wt egg codex               # Codex, sandboxed
wt egg ollama              # Ollama, sandboxed (free, local)
wt egg list                # see active sessions
wt egg stop <id>           # kill a session
```

### Sandbox

| Platform | Method |
|----------|--------|
| macOS | Seatbelt (sandbox-exec) |
| Linux | User namespaces + seccomp |

Configure via `egg.yaml` in your project or `~/.wingthing/egg.yaml`:

```yaml
isolation: network          # strict, standard, network, privileged
mounts:
  - ~/repos:rw
  - ~/docs:ro
deny:
  - ~/.ssh
  - ~/.gnupg
```

The sandbox auto-discovers where your agent binary and config live — you don't need to mount them explicitly.

Levels: `strict` (no network, minimal fs), `standard` (no network, mounted dirs), `network` (network + mounted dirs), `privileged` (no sandbox).

## Remote access

`wt wing` connects your machine outbound to the relay via WebSocket. No port forwarding, no static IP, works behind any NAT. Open `app.wingthing.ai` to start sandboxed terminals on your machine from any device.

```
wt login                   # authenticate with GitHub
wt wing -d                 # start as background daemon
wt wing status             # check daemon status
wt wing stop               # stop daemon
```

```
Browser (xterm.js) <-> Relay (dumb pipe) <-> Your machine
```

Sessions are end-to-end encrypted (ECDH + AES-GCM). The relay forwards opaque bytes — it never reads your data.

## Agents

`wt doctor` detects what's installed. Swap agents with a flag.

| Agent | CLI | Notes |
|-------|-----|-------|
| Claude Code | `claude` | Anthropic |
| Codex | `codex` | OpenAI |
| Cursor Agent | `agent` | Cursor |
| Ollama | `ollama` | Free, local, offline |

## Skills

Markdown prompt templates with YAML frontmatter. Curated, version-controlled, agent-agnostic.

```
wt run --skill compress --agent ollama
wt skill list
wt skill add skills/compress.md
```

## Install

```bash
curl -fsSL https://wingthing.ai/install.sh | sh
```

Or with Go:

```bash
go install github.com/ehrlich-b/wingthing/cmd/wt@latest
```

Or from source (requires Go 1.25+ and Node.js):

```bash
git clone https://github.com/ehrlich-b/wingthing.git
cd wingthing && make check    # test + build → ./wt
```

Update: `wt update`

## Self-hosting

Single binary, SQLite, no external dependencies.

```bash
wt serve --addr :8080                       # start relay
wt wing --relay http://localhost:8080        # connect to it
```

## License

MIT
