# wingthing

[![cinch](https://cinch.sh/badge/github.com/ehrlich-b/wingthing.svg)](https://cinch.sh/jobs/github.com/ehrlich-b/wingthing)

Sandboxed AI agents on your machine, accessible from anywhere over an encrypted, passkey-protected roost that can't read your data.

https://github.com/user-attachments/assets/f1f04caf-4b07-4298-ba76-db5b226c38f2


```
wt egg claude              # sandboxed Claude Code session
wt start                   # connect your machine to the roost
open app.wingthing.ai      # start sessions from any browser
```

## Three security domains

**The egg** is a sandboxed agent session on your machine. Each `wt egg <agent>` spawns a child process inside an OS-level sandbox (Seatbelt on macOS, user namespaces + seccomp on Linux). Same idea as containers but lighter weight. Filesystem access, network reach, and system calls are all controlled.

**The wing** is a daemon on your machine that connects outbound to a roost. All traffic between your browser and your wing is E2E encrypted (X25519 + AES-GCM) - the roost forwards ciphertext that it can't read. Wings connect outbound only, so they work behind any NAT or firewall. Lock your wing and sessions require a passkey on top of encryption.

**The roost** is the server. `app.wingthing.ai` is the hosted roost. Self-host with `wt roost` on your own machine - that runs the server and a wing together in one process, no separate daemon to manage. The roost handles login, routes connections to the right wing (or itself in self-hosted mode), and stores credentials. It never sees terminal data, file contents, or session recordings.

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

## Remote access

```
wt login                   # authenticate with GitHub or Google
wt start                   # background daemon
wt status                  # check it
wt stop                    # stop it
```

Open [app.wingthing.ai](https://app.wingthing.ai) to browse your wings, start sessions, and view history. Lock your wing with `wt wing lock` to require passkey auth before sessions start.

## Agents

`wt doctor` shows what's installed. Swap agents per-session.

| Agent | CLI |
|-------|-----|
| Claude Code | `claude` |
| Codex | `codex` |
| Cursor Agent | `agent` |
| Gemini | `gemini` |
| Ollama | `ollama` |

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
wt roost                   # server + wing, one command
open localhost:8080         # start sessions
```

For multi-user, add GitHub or Google OAuth env vars. See the [docs](https://wingthing.ai/docs#self-hosting).

## Docs

[wingthing.ai/docs](https://wingthing.ai/docs)

## License

MIT
