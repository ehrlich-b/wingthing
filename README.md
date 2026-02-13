# wingthing

[![cinch](https://cinch.sh/badge/github.com/ehrlich-b/wingthing.svg)](https://cinch.sh/jobs/github.com/ehrlich-b/wingthing)

Sandboxed AI agents on your machine, accessible from anywhere.

```
wt egg claude              # sandboxed Claude Code session
wt start                   # connect your machine to the relay
open app.wingthing.ai      # start sessions from any browser
```

## How it works

`wt egg` spawns an agent inside an OS-level sandbox. The sandbox is the permission boundary - agents run with `--dangerously-skip-permissions` because they can't escape the sandbox anyway.

macOS uses Seatbelt (`sandbox-exec`). Linux uses user namespaces and seccomp. No containers, no VMs.

Each session gets its own process and sandbox. Detach and reattach without losing state.

```
wt egg claude              # Claude Code
wt egg codex               # Codex
wt egg ollama              # local inference, free, offline
wt egg list                # active sessions
wt egg stop <id>           # kill one
```

### Configuration

Drop an `egg.yaml` in your project or `~/.wingthing/egg.yaml`:

```yaml
isolation: network
fs:
  - "rw:~/repos"
  - "ro:~/docs"
deny:
  - "~/.ssh"
  - "~/.gnupg"
```

Four isolation levels: `strict` (no network, minimal fs), `standard` (no network, mounted dirs), `network` (full network + mounts), `privileged` (no sandbox).

The sandbox auto-discovers where agent binaries and configs live. You don't mount those yourself.

## Remote access

`wt wing` opens an outbound WebSocket to the relay. No port forwarding, no static IP, works behind NAT.

```
wt login                   # authenticate with GitHub
wt start                   # background daemon
wt status                  # check it
wt stop                    # stop it
```

```
browser (xterm.js) <-> relay (dumb pipe) <-> your machine
```

The relay never sees your data. Sessions are end-to-end encrypted (ECDH + AES-GCM). The relay forwards opaque bytes.

Open `app.wingthing.ai` to browse your connected wings, start sessions, and view session history.

## Agents

`wt doctor` shows what's installed. Swap agents per-session.

| Agent | CLI |
|-------|-----|
| Claude Code | `claude` |
| Codex | `codex` |
| Cursor Agent | `agent` |
| Ollama | `ollama` |
| Gemini | `gemini` |

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
wt serve --addr :8080
wt wing --relay http://localhost:8080
```

## License

MIT
