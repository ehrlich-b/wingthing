# Sandbox: Current State and Problems

## Architecture

Three-tier sandbox with platform detection:

```
sandbox.New(cfg) → try newPlatform(cfg) → fallback to newFallback(cfg)
```

| Backend | Platform | Isolation Mechanism |
|---------|----------|-------------------|
| Apple Containers | macOS 26+ | `container init/exec` — per-task Linux VMs |
| Linux Namespaces | Linux (CAP_SYS_ADMIN) | CLONE_NEWNS/PID/NET + seccomp BPF + rlimits |
| Fallback | Any | Isolated tmpdir, restricted TMPDIR env var. Not a real sandbox. |

## Isolation Levels

| Level | Network | Filesystem | Linux Cloneflags |
|-------|---------|-----------|-----------------|
| strict | No | Minimal, all mounts forced read-only | CLONE_NEWNS + CLONE_NEWPID + CLONE_NEWNET |
| standard | No | Mounted dirs only, respects per-mount ReadOnly | CLONE_NEWNS + CLONE_NEWPID |
| network | Yes | Mounted dirs only | CLONE_NEWNS + CLONE_NEWPID (same as standard!) |
| privileged | Yes | Full host access | 0 (no namespaces, skips sandbox entirely) |

## Problems

### 1. Egg doesn't use sandbox for PTY processes

**Severity: Critical**

`server.go:Spawn()` creates the PTY via `exec.Command(binPath, args...)` then `pty.StartWithSize(cmd, ...)` — this runs the agent directly on the host. The sandbox is created *after* the process starts and stored on the session struct but never wraps the agent execution.

The sandbox `Exec()` API returns a configured `*exec.Cmd` (with namespaces/seccomp on Linux, or `container exec` wrapper on Apple). For PTY sessions, we need to:
1. Call `sb.Exec()` to get the sandboxed cmd
2. Pass that cmd to `pty.StartWithSize()`

This is a design issue: `sb.Exec()` returns an `*exec.Cmd` which is exactly what `pty.StartWithSize` expects, so the fix is straightforward — just reverse the order and pipe the sandbox cmd into the PTY.

**Fix location:** `internal/egg/server.go:Spawn()`

### 2. Linux network isolation is broken for `network` level

**Severity: Medium**

`network` and `standard` produce identical clone flags:
```go
case Standard:
    return syscall.CLONE_NEWNS | syscall.CLONE_NEWPID
case Network:
    return syscall.CLONE_NEWNS | syscall.CLONE_NEWPID  // identical!
```

The intent is that `standard` blocks network and `network` allows it. But neither sets `CLONE_NEWNET`. The difference is only checked in `hasNetwork()` for Apple Container's `--network` flag. On Linux, both levels have identical behavior — full host network access.

To actually block network on `standard`/`strict` on Linux, `standard` needs `CLONE_NEWNET` too (like `strict` already has), and `network` stays without it. Currently it's backwards.

**Fix location:** `internal/sandbox/linux.go:cloneFlags()`

### 3. Linux rlimits are defined but never applied

**Severity: High**

`rlimits()` in `linux.go` returns the limit pairs (CPU=120s, AS=512MB, NOFILE=256) but `sysProcAttr()` never calls it. The rlimits function exists, the values are defined, but they're never set on the `SysProcAttr`. The process runs with inherited limits.

```go
func (s *linuxSandbox) sysProcAttr() *syscall.SysProcAttr {
    attr := &syscall.SysProcAttr{
        Cloneflags: s.cloneFlags(),
    }
    // rlimits() is never called here
    filter := buildSeccompFilter()
    if len(filter) > 0 {
        attr.AmbientCaps = []uintptr{}
    }
    return attr
}
```

**Fix location:** `internal/sandbox/linux.go:sysProcAttr()`

### 4. Seccomp filter is built but never installed

**Severity: High**

`buildSeccompFilter()` constructs a valid BPF program but `sysProcAttr()` never attaches it. There's no `SysProcAttr.SeccompFilter` or `prctl(PR_SET_SECCOMP)` call. The BPF program is constructed and discarded. Go's `syscall.SysProcAttr` doesn't have a seccomp field — you'd need to install it via a child process wrapper or `Pdeathsig` + init code.

**Fix location:** Needs a wrapper binary or `SECCOMP_SET_MODE_FILTER` via prctl in the child. This is a bigger lift.

### 5. Linux mount namespace mounts are prepared but never executed

**Severity: High**

`mountPaths()` computes bind mount arguments but nothing calls `mount(2)`. After `CLONE_NEWNS`, the child inherits the parent's mount namespace copy. Without explicit bind mounts and pivot_root/chroot, the child sees the entire host filesystem.

This is the same class of problem as seccomp — the data structures exist but the actual syscalls are never made. Needs an init process or wrapper that executes the mounts after clone.

**Fix location:** `internal/sandbox/linux.go` — needs child-side mount execution

### 6. Fallback sandbox provides no meaningful isolation

**Severity: Low (known/documented)**

The fallback is documented as "not a real sandbox." It sets `TMPDIR` and `Dir` to an isolated tmpdir but passes through the full host environment. An agent in fallback mode has full disk/network/process access. This is acceptable as a last resort but should be logged prominently.

### 7. Resource limits are hardcoded, not configurable

**Severity: Medium**

CPU (120s), memory (512MB), and fd limits (256) are compile-time constants. There's no way for a skill or session to request different limits. Interactive PTY sessions especially need different limits than batch skill execution — an interactive Claude session shouldn't be killed after 120s of CPU time.

The egg's `SpawnRequest` proto needs fields for per-session resource limits. The sandbox `Config` struct needs limit overrides.

**Fix location:** `internal/sandbox/sandbox.go:Config`, `proto/egg.proto:SpawnRequest`

### 8. Apple Container timeout leaks cancel func

**Severity: Low**

```go
func (s *appleSandbox) Exec(...) (*exec.Cmd, error) {
    if s.cfg.Timeout > 0 {
        ctx, cancel = context.WithTimeout(ctx, s.cfg.Timeout)
        _ = cancel // caller owns the cmd lifecycle; context handles TTL
    }
```

The cancel func is discarded. The comment says "caller owns the cmd lifecycle" but the caller never gets the cancel func. The timeout context will be collected by GC eventually but this is a resource leak pattern. Should either return the cancel or use `AfterFunc`.

**Fix location:** `internal/sandbox/apple.go:Exec()`

## Fix Priority

1. **Egg sandbox wiring** — processes run unsandboxed today (critical, easy fix)
2. **Linux rlimits + seccomp application** — defined but not applied (high, moderate lift)
3. **Linux mount execution** — prepared but not executed (high, bigger lift)
4. **Linux network isolation** — standard/network have identical behavior (medium, easy fix)
5. **Configurable resource limits** — hardcoded constants (medium, proto + sandbox config)
6. **Fallback logging** — make it obvious when fallback is used (low, trivial)
7. **Apple cancel leak** — minor resource leak (low, trivial)
