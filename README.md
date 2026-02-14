# wingthing

[![cinch](https://cinch.sh/badge/github.com/ehrlich-b/wingthing.svg)](https://cinch.sh/jobs/github.com/ehrlich-b/wingthing)

Sandboxed AI agents on your machine, accessible from anywhere.

```
wt egg claude              # sandboxed Claude Code session
wt start                   # connect your machine to the relay
open app.wingthing.ai      # start sessions from any browser
```

## How it works

`wt egg` spawns an agent inside an OS-level sandbox. macOS uses Seatbelt (`sandbox-exec`). Linux uses user namespaces and seccomp. No containers, no VMs.

Every session runs in its own sandboxed process. Detach and reattach without losing state.

```
wt egg claude              # Claude Code
wt egg codex               # Codex
wt egg ollama              # local inference, free, offline
wt egg list                # active sessions
wt egg stop <id>           # kill one
```

### Sandbox

Out of the box, the sandbox is opinionated: CWD is writable, home is read-only, sensitive directories (`~/.ssh`, `~/.gnupg`, `~/.aws`, etc.) are denied, network is off, and only essential env vars are passed through. The agent's binary and config directory are auto-mounted - you don't declare those.

Drop an `egg.yaml` in your project to customize. Configs are additive - you only declare what you're changing from the defaults.

```yaml
# egg.yaml - add SSH access and network on top of defaults
fs:
  - "ro:~/.ssh"       # overrides the default deny for ~/.ssh
network: "*"           # open network
env:
  - SSH_AUTH_SOCK      # pass SSH agent socket
```

Use `base: none` for a blank slate if you want full control. Use the [sandbox builder](https://wingthing.ai) on the homepage to generate configs visually.

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
