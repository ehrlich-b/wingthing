# Egg Sandbox Design: Auto-Drilled Agent Holes

## Core Principle

All sandbox rules are implicitly "AND what the tool needs to run."

The egg config defines what the **task** needs. The system automatically adds what the **agent** needs. The user never has to know Claude's internals to write a secure egg config. This is a major part of wingthing's value — eggs with pre-drilled holes for agent operation, blocking everything else.

```
Final sandbox = egg config constraints + auto-injected agent requirements
```

## Two Modes

### Default: Wide open, no --dangerously-skip-permissions

When no egg.yaml exists (or egg.yaml is minimal), the sandbox is permissive. The agent's own permission system (Claude's permission prompts) is the security boundary. The agent asks before doing anything dangerous.

```yaml
# Default egg config (implicit when no egg.yaml exists)
isolation: network     # full network access
mounts: ["~:rw"]      # full home writable
env:
  allow_all: true      # all env vars passed through
# No deny list, no resource limits, no dangerously_skip_permissions
```

This is the "just works" mode. No sandbox enforcement beyond what the agent itself provides. Users who don't care about isolation get a working agent with no friction.

### Lockdown: Tight sandbox WITH --dangerously-skip-permissions

When the egg.yaml configures real isolation, the **sandbox** becomes the security boundary. The agent gets `--dangerously-skip-permissions` because the sandbox is doing the restricting — Claude's permission prompts are redundant and just slow things down.

```yaml
# Lockdown egg config
isolation: standard
dangerously_skip_permissions: true
mounts: ["~/scratch/jail:rw"]
deny: ["~/.ssh", "~/.gnupg", "~/.aws", "~/.wingthing"]
env:
  allow: [ANTHROPIC_API_KEY]
resources:
  cpu: 120s
  memory: 1GB
  max_fds: 512
```

In lockdown, the system enforces EVERYTHING the config says, with zero holes except what the agent profile declares as necessary to function. The jailbreak test should show only agent-profile holes, nothing else.

### The relationship

```
Loose sandbox  +  no --dangerously-skip  =  agent is the security boundary
Tight sandbox  +  --dangerously-skip     =  sandbox is the security boundary
```

Never: tight sandbox without --dangerously-skip (pointless friction).
Never: no sandbox with --dangerously-skip (no security at all).

### Anti-pattern: poking holes for expediency

DO NOT disable isolation features to work around bugs. If the agent hangs because the sandbox blocks something it needs, the fix is to add a **surgical agent-profile hole** — not to rip out the entire network deny or write deny. Every hole must be:

1. Declared in the agent profile
2. As narrow as possible (regex for specific files, port-based for network)
3. Documented with WHY the agent needs it

If you can't figure out what the agent needs, add diagnostics. Don't open the floodgates.

## Agent Requirement Profiles

Each agent declares what it needs to function. These are additive — they punch specific holes in the egg's restrictions, not override them entirely.

### Claude

| Category | Requirement | Why |
|---|---|---|
| Network | Outbound HTTPS (port 443) + DNS (port 53) | API calls to api.anthropic.com, auto-updater |
| Env vars | ANTHROPIC_API_KEY, HOME, PATH, TERM | API auth, basic shell operation |
| FS write | ~/.claude/, ~/.claude.json*, ~/.cache/claude/ | Config, state, update staging |
| FS read | Agent binary tree, system libs, CWD | Execution |

### Codex (OpenAI)

| Category | Requirement | Why |
|---|---|---|
| Network | Outbound HTTPS + DNS | API calls to api.openai.com |
| Env vars | OPENAI_API_KEY, HOME, PATH, TERM | API auth |
| FS write | ~/.codex/ | Config/state |
| FS read | Agent binary tree, system libs, CWD | Execution |

### Gemini

| Category | Requirement | Why |
|---|---|---|
| Network | Outbound HTTPS + DNS | API calls to googleapis.com |
| Env vars | GEMINI_API_KEY or GOOGLE_API_KEY, HOME, PATH, TERM | API auth |
| FS write | ~/.gemini/ (TBD) | Config/state |
| FS read | Agent binary tree, system libs, CWD | Execution |

### Ollama (local)

| Category | Requirement | Why |
|---|---|---|
| Network | localhost:11434 only | Local inference server |
| Env vars | HOME, PATH, TERM | Basic operation |
| FS write | ~/.ollama/ | Model cache |
| FS read | Agent binary, system libs, CWD | Execution |

## How It Combines

Example: egg config says `isolation: standard` (no network), `mounts: [~/scratch/jail:rw]`, agent is Claude.

```
Egg says:                          Agent needs:
  no network                         HTTPS outbound + DNS
  write only ~/scratch/jail          write ~/.claude*, ~/.cache/claude
  deny ~/.ssh, ~/.gnupg, ~/.aws     (no conflict)
  env: ANTHROPIC_API_KEY, PATH...   env: ANTHROPIC_API_KEY, HOME, PATH, TERM

Result:
  network: HTTPS outbound (443) + DNS (53) only — not wide open
  writes:  ~/scratch/jail + ~/.claude* + ~/.cache/claude — not all of HOME
  denies:  ~/.ssh, ~/.gnupg, ~/.aws (takes precedence over everything)
  env:     union of egg allowlist + agent requirements, nothing else
  reads:   CWD + agent binary + system libs (broad by default, deny list restricts sensitive)
```

## v0.9.7 Jailbreak Bugs (FIXED in v0.9.8)

All five issues below were fixed in v0.9.8:

1. **Env passthrough** — `server.go` fell back to `os.Environ()` when `rc.Env` was empty. Fixed: always use `rc.Env`, merge agent profile vars + essentials from host.

2. **Blanket network override** — `AllowOutbound: true` defeated isolation for all cloud agents. Fixed: replaced with `NetworkNeed` enum (None/Local/HTTPS/Full) from agent profiles.

3. **Resource limits not wired** — `egg.go` didn't pass `--cpu`/`--memory`/`--max-fds` flags. Fixed: flags added, passed through from egg config.

4. **Seccomp never installed** — `buildSeccompFilter()` existed but was never called. Fixed: installed in `_deny_init` via `PR_SET_NO_NEW_PRIVS` + `SYS_SECCOMP` before child exec.

5. **Linux write isolation** — Still deferred. Requires remount-ro + bind-mount work in `_deny_init`.

## SBPL Network Filtering Reference (macOS)

Discovered via testing on macOS. sandbox-exec uses SBPL (Sandbox Profile Language).

### What SBPL CAN filter

| Filter | Syntax | Tested |
|---|---|---|
| Port (TCP) | `(remote tcp "*:443")` | Yes, blocks other ports |
| Port (UDP) | `(remote udp "*:53")` | Yes |
| Multi-port | `(remote tcp "*:80" "*:443")` | Yes, space-separated |
| All TCP | `(remote tcp)` | Yes |
| All IP | `(remote ip)` | Yes |
| Localhost only | `(remote ip "localhost:*")` | Yes, blocks external |
| Exclude localhost | `(require-not (remote ip "localhost:*"))` | Yes |
| Local bind port | `(local tcp "*:8888")` | Yes |
| Unix socket path | `(remote unix-socket (path-literal "..."))` | Yes |

### What SBPL CANNOT filter

| Filter | Result |
|---|---|
| Specific IP address | Rejected: "host must be * or localhost" |
| Domain name | Rejected: "host must be * or localhost" |
| CIDR range | Not supported |

### Critical: DNS on macOS

DNS resolution goes through `/private/var/run/mDNSResponder` (Unix domain socket), NOT UDP port 53. Any profile that needs name resolution MUST include:

```scheme
(literal "/private/var/run/mDNSResponder")
```

### NetworkNeed profiles (implemented in v0.9.8)

**NetworkNone:**
```scheme
(deny network*)
```

**NetworkLocal (ollama):**
```scheme
(deny network*)
(allow network-outbound (literal "/private/var/run/mDNSResponder") (remote ip "localhost:*"))
```

**NetworkHTTPS (claude, codex, gemini, cursor):**
```scheme
(deny network*)
(allow network-outbound (literal "/private/var/run/mDNSResponder") (remote tcp "*:443" "*:80"))
```

**NetworkFull:** no deny.

### Linux network status

Linux uses CLONE_NEWNET for network isolation. Port-level filtering in user namespaces requires iptables, which isn't available without CAP_NET_ADMIN. Current approach: strip CLONE_NEWNET for any agent that needs network (Local/HTTPS/Full all get full network). Port filtering deferred until veth+iptables support is added.

## Remaining Work

1. **Linux write isolation** — remount home read-only in `_deny_init`, bind-mount writable paths
2. **Linux network pinholes** — veth pair + iptables in namespace for port-level filtering
3. **macOS resource limits** — seatbelt can't do rlimits; need `setrlimit` in PostStart
