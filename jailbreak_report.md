# Jailbreak Report — v0.9.7

Tested 2026-02-11 on both macOS (seatbelt) and Linux (namespaces).
Both platforms now boot Claude successfully in the sandbox.

## Egg Config Under Test

```yaml
isolation: standard
mounts: ["~/scratch/jail:rw", "/tmp:rw"]
deny: ["~/.ssh", "~/.gnupg", "~/.aws", "~/.wingthing"]
env:
  allow: [ANTHROPIC_API_KEY, PATH, HOME, TERM]
cpu: 120s
memory: 1GB
max_fds: 512
```

## What Works

| Constraint | Mac | Linux | Mechanism |
|---|---|---|---|
| Deny ~/.ssh | YES | YES | seatbelt deny / tmpfs overlay |
| Deny ~/.gnupg | YES | YES | seatbelt deny / tmpfs overlay |
| Deny ~/.aws | YES | YES | seatbelt deny / tmpfs overlay |
| Deny ~/.wingthing | YES | YES | seatbelt deny / tmpfs overlay |
| Write isolation (home) | YES | **NO** | seatbelt subpath deny / not enforced |

## What's Broken

### 1. Network — BOTH PLATFORMS

`isolation: standard` should deny network. Instead, `agentNeedsNetwork("claude")` returns true, setting `AllowOutbound: true`, which skips the network deny entirely.

- Mac: `(deny network*)` never emitted
- Linux: `CLONE_NEWNET` stripped from clone flags

**Root cause:** `server.go` auto-sets `AllowOutbound` for cloud agents, overriding the user's explicit isolation level. Design tension: Claude literally needs network for API calls, but the config says no-network.

### 2. Read isolation — BOTH PLATFORMS

Entire home directory readable. The seatbelt profile only denies *writes* to HOME, not reads. Linux has no read restriction at all (no mount namespace isolation for reads).

- Mac: `(deny file-write* (subpath HOME))` — write-only deny, reads pass through
- Linux: no filesystem read isolation mechanism exists in current code

### 3. Filesystem writes — LINUX ONLY

Write access completely unrestricted on Linux. Successfully wrote to `~/`, `~/repos/`, `~/scratch/`, `/var/tmp/`. The mount whitelist from egg config is not enforced — Linux sandbox only uses env vars (HOME, TMPDIR) not actual mount restrictions.

**Root cause:** Linux sandbox sets `cmd.Dir` and env vars but doesn't restrict writes via bind mounts or any other mechanism. Only deny paths (tmpfs overlays) restrict anything.

### 4. Resource limits — BOTH PLATFORMS

None applied despite `cpu: 120s`, `memory: 1GB`, `max_fds: 512` in config.

- Mac: seatbelt has no resource limit support (needs separate mechanism)
- Linux: `prlimit` is called in `PostStart()` but only when `CPULimit`/`MemLimit`/`MaxFDs` are non-zero in `sandbox.Config`. These fields aren't being populated from the egg config.

**Root cause:** `server.go` passes `rc.CPULimit`, `rc.MemLimit`, `rc.MaxFDs` to `sandbox.Config`, but the egg config parser likely isn't populating the RunConfig fields from the YAML.

### 5. Environment filtering — BOTH PLATFORMS

20+ env vars visible despite `env.allow: [ANTHROPIC_API_KEY, PATH, HOME, TERM]`. Full host environment leaked including SHELL, USER, LANG, HOMEBREW_*, GIT_EDITOR, etc.

**Root cause:** `server.go` line 158: `envSlice = os.Environ()` when `len(rc.Env) == 0`. The env allowlist from egg config isn't being applied — it only filters when explicit env vars are provided via RunConfig.

### 6. Seccomp — LINUX ONLY

`/proc/self/status` shows `Seccomp: 0`. The BPF filter (`buildSeccompFilter()`) exists in code but is never installed. No `prctl(PR_SET_SECCOMP)` or `seccomp()` syscall is made.

**Root cause:** `buildSeccompFilter()` builds the BPF program but nothing calls it. Missing: apply the filter in `PostStart()` or via `SysProcAttr.

### 7. Jail supervisor writable — LINUX ONLY

`/home/pants/.local/bin/wt` is owned by the sandboxed user with rwx. Agent can replace the wt binary, meaning next jail launch has zero restrictions.

**Root cause:** wt binary lives in user-writable directory. Linux write isolation doesn't restrict writes to user-owned paths outside the mount list.

### 8. SSH agent socket accessible — LINUX ONLY

`/tmp/ssh-XXXXWDN57C/agent.204192` reachable from inside jail. Deny on `~/.ssh` doesn't block the agent socket in `/tmp`. No loaded keys in this test, but the socket is accessible.

## Priority Fix Order

1. **Network isolation** — Stop overriding user's isolation level. If agent needs network but config says no-network, error or warn, don't silently allow.
2. **Env filtering** — Apply allowlist from egg config. Filter `os.Environ()` to only permitted vars.
3. **Linux write isolation** — Enforce mount whitelist via bind mounts or similar mechanism.
4. **Resource limits** — Wire egg config fields through to sandbox.Config.
5. **Seccomp** — Install the BPF filter that's already built.
6. **Read isolation** — Add `file-read*` restrictions on Mac. Harder on Linux (needs mount namespace work).
7. **Supervisor protection** — Make wt binary read-only or install to root-owned path.
8. **SSH agent socket** — Add `/tmp/ssh-*` to default deny list or restrict /tmp visibility.
