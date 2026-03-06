# Native Agent Sandbox Landscape & Translation Strategy

## Context

Every major AI coding agent now ships built-in OS-level sandboxing. They're all converging on the same primitives wingthing already uses (seatbelt on macOS, landlock/seccomp/bwrap on Linux). This doc catalogs what each agent can and can't do natively, compares against egg.yaml's capabilities, and outlines the path toward wingthing as a sandbox orchestrator rather than a standalone sandbox.

The end state: egg.yaml is the universal sandbox spec. When an agent's native sandbox can enforce a capability, wingthing configures it. When it can't, wingthing enforces the gap. The wingthing layer gets thinner over time but never disappears.

## Agent Native Sandbox Capabilities

### Claude Code

**Technology:** Seatbelt on macOS, bubblewrap (`bwrap`) on Linux/WSL2. Network isolation via HTTP+SOCKS5 proxy running outside the sandbox, connected via Unix domain socket. Open-sourced as `@anthropic-ai/sandbox-runtime`.

**Configuration:** `settings.json` at multiple precedence levels (managed > CLI > local project > shared project > user). Array-valued settings merge across scopes. Enterprise delivery via MDM plist (macOS), registry (Windows), `/etc/claude-code/managed-settings.json` (Linux).

**Filesystem:**
- `sandbox.filesystem.allowWrite`: paths where sandboxed commands can write (beyond CWD)
- `sandbox.filesystem.denyWrite`: paths where writes are blocked (takes precedence over allowWrite)
- `sandbox.filesystem.denyRead`: paths where reads are blocked
- CWD + subdirectories writable by default
- Path prefixes: `//` = absolute from root, `~/` = home, `/` = relative to settings file
- macOS supports git-style globs (`*`, `**`, `?`, `[abc]`). Linux: literal paths only.

**Network:**
- `sandbox.network.allowedDomains`: domain allowlist with wildcards (e.g., `*.npmjs.org`)
- `sandbox.network.allowLocalBinding`: allow binding to localhost ports (default false)
- `sandbox.network.allowUnixSockets`: specific Unix socket paths
- `sandbox.network.allowManagedDomainsOnly`: lock to enterprise-managed list only
- Custom proxy ports for org MITM/inspection
- Domain filtering only — no IP filtering, no port-specific filtering, no SNI-based filtering. Domain fronting is a known bypass.

**Env vars:** None. No allowlist/denylist for sandboxed process environment. The sandboxed process inherits the parent's full environment. `env` key in settings controls Claude Code's own process (telemetry), not sandbox env filtering. Gap.

**Resource limits:** None. No CPU, memory, PID, or FD limits. Gap.

**Process isolation:** Inherited from bwrap/seatbelt. No PID namespace on macOS.

**Seccomp:** Unix socket filtering only (blocks connecting to arbitrary Unix sockets on Linux). No general syscall denylist. Gap compared to wingthing's 27+ denied syscalls.

**Audit:** None. Gap.

**Escape hatch:** When a sandboxed command fails, Claude can retry with `dangerouslyDisableSandbox`, routed through normal permission flow. Can be disabled with `allowUnsandboxedCommands: false`.

**Key schema:**
```json
{
  "sandbox": {
    "enabled": true,
    "autoAllowBashIfSandboxed": true,
    "excludedCommands": ["docker", "git"],
    "allowUnsandboxedCommands": true,
    "filesystem": {
      "allowWrite": ["//tmp/build", "~/.kube"],
      "denyWrite": ["//etc"],
      "denyRead": ["~/.aws/credentials"]
    },
    "network": {
      "allowedDomains": ["github.com", "*.npmjs.org"],
      "allowUnixSockets": ["/var/run/docker.sock"],
      "allowLocalBinding": false
    }
  }
}
```

### Cursor

**Technology:** Seatbelt on macOS, landlock + seccomp on Linux. WSL2 on Windows (runs the Linux sandbox inside WSL2). Background/cloud agents use isolated Ubuntu VMs on AWS — completely different model, no OS-level sandbox.

**Configuration:** `sandbox.json` at two levels: `~/.cursor/sandbox.json` (global), `<workspace>/.cursor/sandbox.json` (per-project, overrides global). Editor settings for auto-run mode. CLI config via `cli-config.json` with typed permission strings. Enterprise admins enforce network policies from web dashboard; deny lists always union, restrictive settings win.

**Filesystem:**
- Base type: `workspace_readwrite` (default — read/write within workspace, read system-wide)
- `additionalReadwritePaths`: extra writable paths
- `additionalReadonlyPaths`: extra readable paths
- `disableTmpWrite`: remove `/tmp` write access
- Protected paths always write-denied: `.git/hooks`, `.git/config`, `.vscode/`, `.cursor/*.json`, `.cursorignore`, `.code-workspace`
- Writable exceptions within `.cursor/`: `rules/`, `commands/`, `worktables/`, `skills/`, `agents/`
- `.cursorignore` enforced at OS level via landlock on Linux — files become truly inaccessible to sandboxed processes

**Network:**
- `networkPolicy.default`: `deny` (default)
- `networkPolicy.allow`: domain patterns, wildcards (`*.example.com`), CIDR notation (`10.0.0.0/8`)
- `networkPolicy.deny`: explicit deny list, takes precedence
- Private addresses (RFC 1918, `127.x`, `::1`, cloud metadata endpoints) blocked by default
- Three tiers: sandbox.json only, sandbox.json + Cursor defaults, allow all

**Env vars:** None. Full filesystem read access means the agent can `cat ~/.aws/credentials`, `cat ~/.npmrc`, etc. `.cursorignore` only blocks Cursor's file-reading tools, not shell commands. Documented weakness (Luca Becker, Nov 2025). Gap.

**Resource limits:** None for local sandbox. VMs have standard cgroup limits for background agents.

**Known CVEs:**
- CVE-2026-22708: shell builtins (`export`, `typeset`) bypass allowlists without approval
- CVE-2025-59944: case-sensitivity bypass on macOS/Windows allows writing to `.Cursor/MCP.json` for RCE

**Key schema:**
```json
{
  "type": "workspace_readwrite",
  "additionalReadwritePaths": [],
  "additionalReadonlyPaths": [],
  "disableTmpWrite": false,
  "networkPolicy": {
    "default": "deny",
    "allow": ["registry.npmjs.org", "*.example.com", "10.0.0.0/8"],
    "deny": ["evil.com"]
  }
}
```

### OpenAI Codex CLI

**Technology:** Seatbelt on macOS, landlock + seccomp on Linux (native, via `codex-linux-sandbox` helper binary). Optional bubblewrap via `features.use_linux_sandbox_bwrap = true`. AppContainer on Windows.

**Configuration:** TOML config at two levels: `~/.codex/config.toml` (user), `.codex/config.toml` (project, overrides user). CLI flags override config.

**Filesystem:**
- Three modes: `read-only`, `workspace-write` (default for `--full-auto`), `danger-full-access`
- `writable_roots`: additional writable paths beyond workspace
- `.git`, `.agents`, `.codex` directories carved out as read-only even in writable modes
- Landlock on Linux: global read, writes restricted to whitelisted roots + `/dev/null`
- `--add-dir <path>`: CLI flag to grant additional writable directories

**Network:**
- Binary on/off only. `network_access = true|false` in config.
- When off, seccomp blocks `SYS_connect`, `SYS_bind`, `SYS_listen`, `SYS_sendto`, `SYS_sendmsg`
- Allows `recvfrom` so tools like `cargo clippy` work with socketpairs
- No domain filtering, no port filtering, no proxy. Gap.
- Sets `CODEX_SANDBOX_NETWORK_DISABLED=1` env var for network-restricted runs

**Env vars:** Best in class. Rich `shell_environment_policy`:
- `inherit = "none"`: start with empty environment
- `set`: explicit key-value pairs
- `exclude`: glob patterns (e.g., `AWS_*`, `AZURE_*`, `*SECRET*`, `*TOKEN*`)
- `include_only`: allowlist
- `ignore_default_excludes`: opt out of automatic SECRET/TOKEN/KEY filtering
- Default auto-excludes variables matching common secret patterns

**Resource limits:** None. Parent-death signal via `prctl(PR_SET_PDEATHSIG)` prevents orphans, but no CPU/memory/PID limits.

**Key schema:**
```toml
sandbox_mode = "workspace-write"

[sandbox_workspace_write]
writable_roots = ["/home/user/.pyenv/shims"]
network_access = false

[shell_environment_policy]
inherit = "none"
set = { PATH = "/usr/bin" }
exclude = ["AWS_*", "AZURE_*", "*SECRET*"]
include_only = ["PATH", "HOME", "TERM"]
```

### Google Gemini CLI

**Technology:** Seatbelt on macOS (6 built-in profiles), Docker/Podman on Linux. No native OS-level sandbox on Linux — containerization required. Auto-selection: Docker > Podman > Seatbelt (macOS only).

**Configuration:** Mix of env vars, CLI flags, and JSON settings. `GEMINI_SANDBOX=true|docker|podman|seatbelt|<custom>`, `SEATBELT_PROFILE=<name>`, `.gemini/settings.json`.

**Filesystem (Seatbelt):**
- Permissive profiles: writes restricted to project dir, reads allowed system-wide
- Restrictive profiles: both reads and writes confined to project dir
- No dotdir carve-outs (unlike Codex)
- Custom profiles: `.gemini/sandbox-macos-<name>.sb` with raw SBPL syntax

**Filesystem (Docker/Podman):**
- Complete isolation
- Volume mounts via `SANDBOX_MOUNTS` env var (e.g., `/data:/data:ro`)

**Network:**
- Three modes per profile: `open` (full), `closed` (all sockets blocked including DNS), `proxied` (routed through proxy on port 8877)
- Proxied mode supports domain allowlisting via the proxy command
- 6 built-in combinations: `{permissive,restrictive}-{open,closed,proxied}`

**Env vars:** None in seatbelt mode. Docker/Podman: explicit passthrough via `SANDBOX_ENV`. Gap in native mode.

**Resource limits:** None in seatbelt mode. Docker/Podman: standard container cgroup limits.

**Key config:**
```
GEMINI_SANDBOX=seatbelt
SEATBELT_PROFILE=restrictive-proxied
SANDBOX_MOUNTS=/data:/data:ro,/config:/config:ro
SANDBOX_ENV=API_KEY,DEBUG
```

## Capability Matrix

| Capability | egg.yaml | Claude Code | Cursor | Codex | Gemini |
|-----------|----------|-------------|--------|-------|--------|
| FS ro/rw/deny paths | `fs: ["ro:/", "rw:./", "deny:~/.ssh"]` | allowWrite, denyWrite, denyRead | additionalRW/RO paths | writable_roots | Project-dir boundary only |
| FS path granularity | Per-file, regex | Per-path, globs (macOS) | Per-path | Per-path | Per-dir |
| Network domain filter | No (binary per-agent) | Yes (proxy, wildcards) | Yes (wildcards, CIDR) | No (binary on/off) | Partial (proxy mode) |
| Network port filter | Yes (macOS seatbelt) | No | No | No (seccomp binary) | No |
| Env var allowlist | Yes (explicit list) | **No** | **No** | Yes (globs, auto-exclude) | **No** (seatbelt) |
| CPU/memory limits | Yes (cgroups v2 + prlimit) | **No** | **No** | **No** | **No** (seatbelt) |
| PID limits | Yes (cgroups v2) | **No** | **No** | **No** | **No** (seatbelt) |
| FD limits | Yes (prlimit) | **No** | **No** | **No** | **No** (seatbelt) |
| Seccomp syscall filter | Yes (27+ denied) | Unix socket only | Yes | Yes | **No** (seatbelt) |
| PID namespace | Yes (CLONE_NEWPID) | Inherited | Inherited | Parent-death signal | **No** (seatbelt) |
| Audit | Yes | **No** | **No** | **No** | **No** |
| Agent-aware profiles | Yes (auto-drilled holes) | **No** | **No** | **No** | **No** |
| Enterprise/MDM | No | Yes (managed settings) | Yes (team admin dashboard) | No | No |

## Translation Architecture

### The Model

```
egg.yaml (universal spec)
    │
    ├── Translate to native config (where agent covers the capability)
    │   ├── Claude Code → settings.json sandbox block
    │   ├── Cursor → sandbox.json
    │   ├── Codex → config.toml sandbox + env policy
    │   └── Gemini → env vars + custom .sb profile
    │
    └── Enforce via wingthing sandbox (where agent has gaps)
        ├── Env var filtering (all except Codex)
        ├── Resource limits (all)
        ├── Deep seccomp (Claude Code, Gemini)
        ├── Audit (all)
        └── Agent-aware auto-drilling (all)
```

### Per-Agent Translation Map

#### Claude Code

| egg.yaml | Translates to | Native? |
|----------|--------------|---------|
| `fs: ["rw:./project"]` | `sandbox.filesystem.allowWrite: ["./project"]` | Yes |
| `fs: ["deny:~/.ssh"]` | `sandbox.filesystem.denyRead: ["~/.ssh"]` | Yes |
| `fs: ["deny-write:./egg.yaml"]` | `sandbox.filesystem.denyWrite: ["./egg.yaml"]` | Yes |
| `env: [ANTHROPIC_API_KEY]` | — | No, wingthing enforces |
| `resources: {cpu: 120s}` | — | No, wingthing enforces |
| Network port filtering | `sandbox.network.allowedDomains` (domain-level, not port) | Partial — different granularity |

#### Cursor

| egg.yaml | Translates to | Native? |
|----------|--------------|---------|
| `fs: ["rw:./scratch"]` | `additionalReadwritePaths: ["./scratch"]` | Yes |
| `fs: ["ro:./docs"]` | `additionalReadonlyPaths: ["./docs"]` | Yes |
| Network (per-domain) | `networkPolicy.allow: ["api.anthropic.com"]` | Yes |
| `env: [ANTHROPIC_API_KEY]` | — | No, wingthing enforces |
| `resources: {memory: 1GB}` | — | No, wingthing enforces |

#### Codex

| egg.yaml | Translates to | Native? |
|----------|--------------|---------|
| `fs: ["rw:./scratch"]` | `writable_roots = ["./scratch"]` | Yes |
| `env: [ANTHROPIC_API_KEY, PATH]` | `include_only = ["ANTHROPIC_API_KEY", "PATH"]` | Yes |
| Network on/off | `network_access = false` | Yes |
| Network domain filtering | — | No, wingthing enforces |
| `resources: {cpu: 120s}` | — | No, wingthing enforces |

#### Gemini

| egg.yaml | Translates to | Native? |
|----------|--------------|---------|
| `fs: ["rw:./project"]` | Custom `.sb` profile with write allow rules | Yes (macOS, manual SBPL) |
| Network closed | `SEATBELT_PROFILE=restrictive-closed` | Yes |
| Network proxied | `SEATBELT_PROFILE=restrictive-proxied` + proxy config | Yes |
| `env: [GEMINI_API_KEY]` | `SANDBOX_ENV=GEMINI_API_KEY` | Docker only, not seatbelt |
| `resources: {memory: 1GB}` | Docker `--memory=1g` | Docker only |

### Decision: When to Use Native vs Wingthing Sandbox

```
For each egg.yaml capability:
  1. Can the agent's native sandbox enforce it?
  2. Is the native enforcement at least as strict as wingthing's?
  3. Is there a clean config translation?

If all three: configure natively, skip wingthing enforcement for that capability.
If any fail: wingthing enforces.
```

Capabilities that will likely ALWAYS need wingthing enforcement:
- **Audit** — no agent does this
- **Agent-aware auto-drilling** — inherently a wingthing concept
- **Resource limits** — no agent does this natively (except Gemini-in-Docker)
- **Deep seccomp** — agents have shallow or no syscall filtering

Capabilities trending toward native:
- **Filesystem isolation** — all agents handle this, with varying granularity
- **Network domain filtering** — Claude Code and Cursor are ahead of wingthing here
- **Env var filtering** — Codex is ahead, others will likely follow

### Implementation Phases

**Phase 1: Detect native sandbox capabilities.** When spawning an egg, check which agent is running and what sandbox features it supports. This is metadata in the agent profiles (`internal/egg/agents.go`), not runtime detection.

**Phase 2: Generate native config alongside wingthing sandbox.** When starting an egg with Claude Code, write a `settings.json` with the translated sandbox config into the session directory. Same for Cursor's `sandbox.json`, Codex's `config.toml`. The agent picks up its native config. Wingthing still wraps the agent in its own sandbox for gap enforcement.

**Phase 3: Thin out wingthing enforcement.** For capabilities where native + wingthing would double-enforce (e.g., filesystem deny on both layers), prefer the native layer and skip the wingthing enforcement. The wingthing layer becomes a safety net that only activates when the native layer can't cover something.

**Phase 4: Non-agent sandboxed terminals.** egg.yaml works for any process, not just AI agents. `wt egg bash`, `wt egg python`, `wt egg node`. The translation layer is irrelevant for non-agent processes — wingthing's full sandbox applies. This is the wingthing.io product.

## Sandboxed Terminals (wingthing.io)

The AI agent is a configuration, not the product. The product is:

- **Sandboxed terminal sessions accessible from anywhere.** egg.yaml defines the sandbox. The thing running inside can be Claude, Cursor, Codex, bash, python, node, anything.
- **Remote access via wing/relay.** E2E encrypted, passkey authenticated, P2P with relay fallback. Already built.
- **Audit.** Full session recording. Already built.
- **Multi-user path ACLs.** Already built (v0.113.0: strict whitelist).
- **Privilege broker pattern.** Give sandboxed processes access to specific APIs without exposing credentials. Already built (Slide shim pattern).

The AI agents are the killer app that gets people in the door. The infrastructure underneath — sandbox, remote access, audit, ACLs — is useful without them.

wingthing.io = the non-AI branding. "Sandboxed terminals, accessible from anywhere." The AI part is: "and you can run Claude/Cursor/Codex inside them."

## References

- Anthropic: [Claude Code Sandboxing Docs](https://code.claude.com/docs/en/sandboxing)
- Anthropic: [Making Claude Code More Secure and Autonomous](https://www.anthropic.com/engineering/claude-code-sandboxing)
- Anthropic: [sandbox-runtime (GitHub)](https://github.com/anthropic-experimental/sandbox-runtime)
- Cursor: [Agent Sandboxing Blog](https://cursor.com/blog/agent-sandboxing)
- Cursor: [Terminal/Sandbox Docs](https://cursor.com/docs/agent/terminal)
- Cursor: [Changelog 2.5: Network Access Controls](https://cursor.com/changelog/2-5)
- OpenAI: [Codex Security](https://developers.openai.com/codex/security/)
- OpenAI: [Codex CLI Reference](https://developers.openai.com/codex/cli/reference/)
- OpenAI: [Codex Advanced Configuration](https://developers.openai.com/codex/config-advanced/)
- Google: [Gemini CLI Sandbox Docs](https://google-gemini.github.io/gemini-cli/docs/cli/sandbox.html)
- Linux Kernel: [Landlock Docs](https://docs.kernel.org/userspace-api/landlock.html)
- Pierce Freeman: [A Deep Dive on Agent Sandboxes](https://pierce.dev/notes/a-deep-dive-on-agent-sandboxes)
- Luca Becker: [When Sandboxing Leaks Your Secrets](https://luca-becker.me/blog/cursor-sandboxing-leaks-secrets/)
- Agent Safehouse: [Cursor Agent Sandbox Analysis](https://agent-safehouse.dev/docs/agent-investigations/cursor-agent)
