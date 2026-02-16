# wingthing

[![cinch](https://cinch.sh/badge/github.com/ehrlich-b/wingthing.svg)](https://cinch.sh/jobs/github.com/ehrlich-b/wingthing)

Sandboxed AI agents on your machine, accessible from anywhere over an encrypted, passkey-protected relay that can't read your data.

<video src="https://github.com/ehrlich-b/wingthing/raw/main/hero.mp4" width="100%"></video>

```
wt egg claude              # sandboxed Claude Code session
wt start                   # connect your machine to the relay
open app.wingthing.ai      # start sessions from any browser
```

## Three security domains

**The egg** protects you from the agent. Each session runs inside an OS-level sandbox - Seatbelt on macOS, user namespaces + seccomp on Linux. Filesystem access, network reach, and system calls are all controlled. No containers, no VMs.

**The wing** protects you from the relay. All traffic between your browser and your machine is E2E encrypted (X25519 + AES-GCM). The relay forwards ciphertext and can't read it. Wings connect outbound only - no open ports, no static IP, works behind any NAT or firewall. Lock your wing and sessions require a passkey on top of encryption - the relay can't start sessions on your behalf even if it wanted to.

**The roost** controls access. It handles login and routes connections to the right wing. It never sees terminal data, file contents, or session recordings.

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

Single binary, SQLite, no external deps. `--local` runs single-user mode with no login page.

```bash
wt serve --local           # start the relay
wt start --local           # connect a wing (separate terminal)
open localhost:8080         # start sessions
```

For multi-user, set up GitHub/Google OAuth and run `wt serve` without `--local`. See the [docs](https://wingthing.ai/docs#self-hosting).

## Docs

[wingthing.ai/docs](https://wingthing.ai/docs)

## License

MIT
