# Sandbox Reference

## Architecture

Platform-native sandbox. No fallback, no silent degradation.

```
sandbox.New(cfg) → newPlatform(cfg) → EnforcementError if platform can't enforce
```

| Backend | Platform | Isolation Mechanism |
|---------|----------|-------------------|
| Seatbelt | macOS | `sandbox-exec` with generated SBPL profiles |
| Linux Namespaces | Linux | CLONE_NEWUSER/NEWNS/NEWPID/NEWNET + seccomp BPF + cgroups v2 + rlimits |

If the platform cannot enforce the requested isolation, the egg fails with `EnforcementError`. No silent fallback.

Linux capability detection exercises the mount operations Wingthing depends
on inside a user+mount namespace. Creating `CLONE_NEWUSER` alone is insufficient
on Ubuntu 24.04: AppArmor may allow namespace creation and then move the child
into a profile that denies mounts. `wt doctor` reports that host state as
`NOT AVAILABLE`; `sudo wt doctor --fix` installs an executable-scoped AppArmor
profile. The fixer accepts a root-owned executable whose complete path is
root-writable-only, such as `/usr/local/bin/wt`. It refuses checkouts, user
homes, download directories, and temporary paths before writing the profile;
AppArmor path attachment does not bind the grant to a content hash.

`_deny_init` treats every policy operation as mandatory. It aborts before the
agent process exists when a mask, read-only bind, HOME boundary, jail mount,
pivot, old-root detach, or seccomp installation fails. After setup it reads
`/proc/self/mountinfo` and verifies every expected mask and writable hole. The
Linux E2E gate plants a unique readable canary inside a denied directory and
attempts the read from the agent's live namespace.

## What the Sandbox Enforces

### Narrowing agent profile domains

Agent profiles contribute the provider domains an agent usually needs. The
mapping form can suppress those additions for a tightly scoped provider:

```yaml
network:
  domains: []
  agent_domains: none
env:
  - OPENAI_API_KEY
  - WT_PROVIDER_BASE_URL
```

With `WT_PROVIDER_BASE_URL=https://api.arliai.com/v1`, the effective network
policy contains the exact host `api.arliai.com`. The default value for
`agent_domains` is `merge`, preserving existing scalar, list, and mapping
configurations. `wt egg explain <agent>` lists declared, automatic, derived,
and suppressed domains with their provenance.

Provider URLs use HTTPS. HTTP is accepted for loopback providers, and IP
literals are accepted for loopback addresses. Userinfo in provider URLs is
rejected.

### Both Platforms

| Feature | macOS | Linux |
|---------|-------|-------|
| Deny paths (.ssh, .aws, etc.) | SBPL rules | tmpfs overlays |
| Write isolation (HOME read-only) | SBPL rules | bind-mount read-only + writable holes |
| Network deny | SBPL `(deny network*)` | CLONE_NEWNET |
| Domain filtering | SBPL forces traffic through local CONNECT proxy | CLONE_NEWNET + inherited-FD loopback relay to CONNECT proxy |
| PID isolation | n/a | CLONE_NEWPID |

### Seccomp (Linux only)

BPF filter blocks 27+ syscalls across these categories:

| Category | Blocked Syscalls |
|----------|-----------------|
| Filesystem | mount, umount2, pivot_root |
| Module loading | init_module, finit_module, delete_module |
| Reboot/swap | reboot, swapon, swapoff, kexec_load, kexec_file_load |
| Process debug | ptrace |
| Namespace escape | setns, unshare |
| Container escape | open_by_handle_at (Shocker CVE-2014-3519) |
| eBPF / perf | bpf, perf_event_open, userfaultfd |
| Kernel keyring | keyctl, add_key, request_key |
| Misc privilege escalation | kcmp, lookup_dcookie, acct |
| Time manipulation | clock_settime, settimeofday |
| x86-only | iopl, ioperm, modify_ldt (amd64 only) |

Installed in `_deny_init` after mounts are complete, inherited by child processes. Prevents the agent from undoing deny-path overmounts.

### Resource Limits (Linux only)

Two enforcement layers: cgroups v2 for real limits, prlimit as belt+suspenders.

| Mechanism | What it limits | Config field |
|-----------|---------------|-------------|
| cgroups v2 `memory.max` | Real memory (RSS) | `resources.memory` |
| cgroups v2 `pids.max` | Process tree count | `resources.max_pids` |
| prlimit RLIMIT_AS | Virtual address space (4GB floor for JIT) | `resources.memory` |
| prlimit RLIMIT_CPU | CPU time | `resources.cpu` |
| prlimit RLIMIT_NOFILE | Open file descriptors | `resources.max_fds` |

Cgroups v2 requires delegation from the init system (systemd usually provides this). When unavailable, falls back to prlimit-only with a log warning. No defaults. Limits only apply when explicitly configured in egg.yaml.

macOS Seatbelt does not support resource limits.

## Known Limitations

### Network protocol coverage

Linux keeps `CLONE_NEWNET` for every network mode. The namespace has no default
route and receives only declared loopback listeners: an HTTP CONNECT proxy for
domain-filtered HTTPS and explicit `network.local_ports` forwards for host-local
services. Removing `HTTPS_PROXY` therefore does not restore network access.

The inherited relay currently carries TCP only. Domain-filtered arbitrary TCP,
UDP, ICMP, and other protocols are not yet available. `network: "*"` permits any
HTTP CONNECT destination, but it still does not create a general routed network
interface.

On WSL2 the same namespace relay is supported. If a particular WSL kernel rejects
one of the filesystem bind mounts, Wingthing names the failed operation and
refuses to launch; run inside a privileged Linux container or VM when an outer
filesystem boundary is required.

### Agent credentials are accessible

Claude needs `~/.claude/` writable. The sandbox mounts it read-write. A sandboxed task can read credentials there, but domain filtering limits where it can send them.

### HOME is readable

Write isolation makes HOME read-only but still readable. Add `deny:~/.secrets` to block specific paths.

### Agent config is writable

`~/.claude/` and similar dirs must be writable for the agent. A task could inject hooks into `settings.json` that persist after the session.

### Resource limits are Linux-only

macOS agents can consume unbounded CPU and memory.

See `docs/egg-sandbox-design.md` for full design details, agent profiles, and SBPL reference.
