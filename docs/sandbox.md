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

## What the Sandbox Enforces

### Both Platforms

| Feature | macOS | Linux |
|---------|-------|-------|
| Deny paths (.ssh, .aws, etc.) | SBPL rules | tmpfs overlays |
| Write isolation (HOME read-only) | SBPL rules | bind-mount read-only + writable holes |
| Network deny | SBPL `(deny network*)` | CLONE_NEWNET |
| Domain filtering | SBPL forces traffic through local CONNECT proxy | CONNECT proxy via HTTPS_PROXY (cooperative) |
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

### Linux: Full network when agent needs HTTPS

`isolation: standard` creates CLONE_NEWNET, but cloud agents (Claude, Codex, etc.) need HTTPS. Linux strips CLONE_NEWNET entirely because unprivileged user namespaces can't do port-level filtering. A CONNECT proxy provides cooperative domain filtering.

macOS enforces port-level filtering at the OS level: TCP 443/80 + mDNSResponder.

### Agent credentials are accessible

Claude needs `~/.claude/` writable. The sandbox mounts it read-write. A sandboxed task can read credentials there, but domain filtering limits where it can send them.

### HOME is readable

Write isolation makes HOME read-only but still readable. Add `deny:~/.secrets` to block specific paths.

### Agent config is writable

`~/.claude/` and similar dirs must be writable for the agent. A task could inject hooks into `settings.json` that persist after the session.

### Resource limits are Linux-only

macOS agents can consume unbounded CPU and memory.

See `docs/egg-sandbox-design.md` for full design details, agent profiles, and SBPL reference.
