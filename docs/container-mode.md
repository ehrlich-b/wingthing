# Container and VM Mode

Thinking out loud about what it would look like if `egg.yaml` could define a Dockerfile or VM config instead of using the OS-level sandbox.

## Why bother

The current sandbox is a deny list. Even with `base: none`, the host filesystem is visible - on macOS seatbelt starts with `(allow default)`, and on Linux the agent inherits the parent's mount namespace. You restrict access by denying specific paths. A container inverts this: start with an empty image, explicitly mount what you need. The agent can't see `~/.bash_history` because it doesn't exist in the container, not because we remembered to deny it.

Secondary: reproducible environments. A Dockerfile pins the exact toolchain. "Works on my machine" for agent sessions.

## The landscape

Everyone serious (Codex CLI, Claude Code CLI) is doing OS-level sandboxing for local interactive PTY sessions. Containers are offered as an optional upgrade (Docker Sandboxes) or for cloud-hosted agents (E2B/Firecracker). Nobody is doing containers-by-default for local use because the startup penalty hurts.

Codex CLI uses Seatbelt on macOS and Landlock+seccomp on Linux - same tier as wingthing. Claude Code uses bubblewrap on Linux and Seatbelt on macOS. Docker Sandboxes runs each agent in a microVM via Apple Virtualization.framework, with an MITM proxy for credential injection. E2B uses Firecracker for cloud-hosted sandboxes with <200ms boot.

## Runtimes

| Runtime | Startup | macOS | No daemon | Notes |
|---------|---------|-------|-----------|-------|
| Rootless Podman | ~130ms | Needs Linux VM | Yes (Linux) | Best Linux option. OCI compatible, bind mounts for bidirectional file access. |
| Apple Containers | ~700ms | macOS 26+ only | Yes | New in Tahoe. Sub-second boot but image unpacking is slow (beta). |
| Docker Desktop | Seconds+ | Yes (via VM) | No | Too heavy. Wrong dependency for a CLI tool. |
| Firecracker | ~125ms | No | Needs /dev/kvm | Linux-only, KVM required. Right for cloud/hosted, wrong for laptops. |

Podman on Linux, Apple Containers on macOS when Tahoe ships, keep seatbelt as default until then. Container mode as opt-in, not default.

## What the config would look like

```yaml
# opt into container mode
container:
  image: ubuntu:24.04
  # OR
  dockerfile: ./Dockerfile

# everything else is the same egg.yaml
fs:
  - "rw:./"
  - "ro:~/.gitconfig"
network:
  - "github.com"
env:
  - SSH_AUTH_SOCK
resources:
  memory: 4GB
  max_pids: 512
```

No `container:` key = current sandbox. Add it = container mode. Same format, different enforcement.

## How fs rules change meaning

In sandbox mode, the host filesystem is always visible (even with `base: none`) and `fs:` rules restrict access via denies and write isolation. In container mode, `fs:` rules are mounts from host into container - nothing from the host is visible unless you bring it in.

From the user's perspective, the intent is the same. Wingthing interprets the config differently per backend:

| Rule | Sandbox mode | Container mode |
|------|-------------|----------------|
| `rw:./` | Writable hole in ro HOME | Bind-mount CWD into container |
| `deny:~/.ssh` | tmpfs overmount | No-op (doesn't exist) |
| `ro:~/.gitconfig` | Already readable on host | Bind-mount read-only |
| Agent auto-injection | Mount agent config dirs rw | Same, as container volumes |

Default deny paths (`~/.ssh`, `~/.aws`, etc.) are free in container mode. They don't exist unless you mount them.

## Preserving login and agent state

Three mechanisms, all bind mounts:

**Agent config dirs** - `~/.claude/`, `~/.codex/`, etc. bind-mounted from host (or per-user home on relay) into the container. OAuth tokens persist on the host. Same as today's writable mount, just a container volume.

**Config snapshot/restore** - `snapshot.go` already snapshots `settings.json` before the session and restores on exit. Works unchanged since the bind-mounted file IS the host file.

**Overlay prefix matching** - the `.claude` / `.claude.json` problem. In container mode, bind-mount both the directory and known adjacent files. Simpler than overlayfs because the container's ephemeral layer handles new file creation. New files vanish on exit (good). Writes to bind-mounted files persist (correct).

## The agent binary problem

Current sandbox auto-detects the host binary and mounts it. In container mode, the host binary might not work inside the container (wrong libc, missing deps).

Options:
1. User installs agent in Dockerfile - explicit, no magic
2. Wingthing installs at container start - `install_agent: true`, cached in image layer
3. Bind-mount host binary - fragile, needs matching runtime (Node for Claude, etc.)

Option 1 as default, option 2 as convenience. Option 3 is too fragile.

## What stays the same

- Config inheritance chain (`base:`, per-section masks)
- Agent profile auto-injection (network holes, env vars, write dirs)
- Domain proxy (runs on host, container routes through HTTPS_PROXY)
- PTY attachment (container exec with PTY, same gRPC/socket plumbing)
- Session lifecycle (start, attach, detach, reattach, exit)
- Per-user home isolation for relay sessions

## What changes

- `sandbox.New()` gets a third backend alongside seatbelt and namespace
- `Exec()` builds `podman run` / `container exec` instead of a namespace-wrapped command
- `PostStart()` is a no-op (container runtime handles cgroups via OCI spec)
- `Destroy()` stops and removes the container
- `_deny_init` wrapper isn't needed (container handles mount isolation)
- New image build/cache layer

## Open questions

- Is ~130ms Podman startup acceptable for interactive PTY sessions? Current sandbox is ~0ms.
- Apple Containers is beta. How rough is it by Tahoe GA?
- Do users actually want to write Dockerfiles for agent sessions, or is this solving a problem nobody has?
- The current sandbox is already pretty good. Is the allow-list security model worth the complexity?
- How does this interact with the relay? Container images need to exist on the wing machine.
- Firecracker makes sense for a hosted offering but not for `wt egg` on a laptop. Is a hosted offering on the roadmap?
