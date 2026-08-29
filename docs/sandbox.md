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

On macOS, every `deny:` path emits both filesystem-denial and Unix-socket
`network-outbound` rules. This matters for discoverable SSH-agent and control
sockets: blocking reads of the socket pathname alone does not block `connect(2)`
to an already-open endpoint. Explicit socket allows are emitted first, so an
overlapping mandatory deny still wins. Linux masks denied socket paths inside
the mount namespace.

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
domain-filtered tunnels and explicit `network.local_ports` forwards for
host-local services. Removing `HTTPS_PROXY` therefore does not restore network
access.

CONNECT can carry arbitrary TCP bytes to any port on an allowed host; the
current policy filters the destination host, not its port. Software must honor
the HTTP proxy variables or explicitly speak CONNECT. There is no SOCKS proxy or
general routed interface for ordinary raw-socket clients, and UDP, ICMP, and
other non-TCP protocols are unavailable. `network: "*"` permits any TCP target
presented through CONNECT, but still creates no general route. Host-side CONNECT
and loopback relays are capped at 256 simultaneous
tunnels per sandbox; CONNECT headers and upstream dials also have finite timeouts.
Destroying the sandbox closes its inherited bridge and active proxy tunnels.
To prevent an allowed public hostname from becoming an SSRF route through DNS
rebinding, named domains may not resolve to loopback, link-local, RFC1918, IPv6
ULA, or CGNAT/tailnet space. An operator who intentionally needs a private
destination can list its IP literal; host-loopback services should use
`network.local_ports`.

On WSL2 the same namespace relay is supported. If a particular WSL kernel rejects
one of the filesystem bind mounts, Wingthing names the failed operation and
refuses to launch; run inside a privileged Linux container or VM when an outer
filesystem boundary is required.

#### Linux upgrade note

Older Linux releases did not keep the network namespace for every policy. After
upgrading, a workload that depended on raw sockets, UDP/ICMP, or an undeclared
destination will fail closed. `network: "*"` restores any-destination CONNECT
traffic, but intentionally does not restore a general route. A workload that
needs a service on the host loopback must declare its TCP port under
`network.local_ports`; listing only `localhost` does not forward every host
port. Use `wt egg explain <agent>` and, temporarily, `network.mode: observe` to
diagnose missing CONNECT destinations. Observe mode does not restore raw
network protocols.

### Agent credentials are accessible

Claude needs `~/.claude/` writable. The sandbox mounts it read-write. A sandboxed task can read credentials there, but domain filtering limits where it can send them.

### HOME is readable

Write isolation makes HOME read-only but still readable. Add `deny:~/.secrets` to block specific paths.

### Agent config is writable

`~/.claude/` and similar dirs must be writable for the agent. A task could inject hooks into `settings.json` that persist after the session.

### Resource limits are Linux-only

macOS agents can consume unbounded CPU and memory.

See `docs/egg-sandbox-design.md` for full design details, agent profiles, and SBPL reference.
