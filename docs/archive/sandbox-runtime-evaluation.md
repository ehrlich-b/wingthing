# Evaluating Anthropic's sandbox-runtime (srt)

Status: historical decision record; the route-less Linux network design described here is implemented
Reviewed: 2026-08-27

The vendor comparison is preserved as the input to the decision. Current
Wingthing behavior is authoritative in [the sandbox reference](sandbox.md): Linux
now retains `CLONE_NEWNET`, has no default route, and exposes only an inherited-FD
loopback relay plus declared local ports.

## Verdict

**Do not adopt srt as a runtime. Do adopt its network design, and do adopt its
config format as a translation target.**

Running srt would cost us a static-binary install, resource limits, deep seccomp,
env filtering, audit, and per-user homes — to gain one thing we can build in Go in
a few hundred lines. Speaking its config, by contrast, is nearly free and buys
defense in depth.

## What srt actually is

| Property | Value |
|---|---|
| Implementation | TypeScript / Node.js — CLI (`srt`) plus a JS library |
| License | Apache-2.0 |
| Status | **Beta Research Preview**, in the `anthropic-experimental` org. "APIs and configuration formats may evolve." |
| Linux deps | `bubblewrap`, `socat`, `ripgrep` (+ `gcc`/`libseccomp-dev` on non-x64/arm64) |
| macOS deps | `ripgrep` |
| Config | JSON at `~/.srt-settings.json` — `network`, `filesystem`, `ignoreViolations` |
| Network | HTTP proxy + SOCKS5. Linux: bind-mounted Unix domain sockets, bridged with socat. macOS: localhost ports allowed by the Seatbelt profile. |
| Seccomp | Narrow and single-purpose — a prebuilt `apply-seccomp` binary (x64/arm64) whose filter blocks Unix domain socket creation |
| Resource limits | Not documented |
| Env var filtering | Not documented |

The core network idea: the sandbox gets a network namespace with no route out, so
the only egress is a bind-mounted Unix socket to a proxy running outside. That is
precisely the fix already proposed for our Linux gap — independently arrived at,
now shipped by a vendor.

## Capability comparison

Against the matrix in `native-sandbox-landscape.md`:

| Capability | wingthing | srt |
|---|---|---|
| FS ro/rw/deny | yes, per-file + regex | yes |
| Network domain filter | yes on macOS and Linux | yes, both |
| TCP tunnel filtering | yes (HTTP CONNECT; client support required) | yes (CONNECT + SOCKS5) |
| Network port filter | yes (macOS seatbelt) | no |
| Env var allowlist | yes | **no** |
| CPU / memory limits | yes (cgroups v2 + prlimit) | **no** |
| PID / FD limits | yes | **no** |
| Seccomp syscall filter | 27+ denied, cross-arch | Unix-socket only |
| PID namespace | yes | inherited from bwrap |
| Audit | yes | **no** |
| Agent auto-drilling | yes | **no** |
| Per-user homes | yes | **no** |
| Overlay HOME + selective persist | yes | **no** |
| Privileged tool socket injection | yes | **no** |
| Windows | no | yes (WFP) |
| Install footprint | one static Go binary | Node + 3 system packages |

At the snapshot, the remaining gaps were network-related. The route-less Linux
egress path has since closed the domain-filter enforcement gap without taking the
dependency. CONNECT can carry arbitrary TCP bytes, but there is no SOCKS or
general routed transport for clients that do not speak CONNECT; UDP and other
non-TCP protocols remain outside the current contract.

## Why not adopt the runtime

**1. It destroys the install story.** `wt` is one static Go binary; that is a
load-bearing part of local-first and of `install.sh`. Adopting srt means every
wingthing user installs Node, bubblewrap, socat, and ripgrep. Our current Linux
sandbox needs none of them — we use raw namespaces directly, which is strictly
fewer moving parts than bubblewrap.

**2. Inversion of control.** srt wants to be the outer wrapper (`srt <command>`).
Our sandbox is constructed *inside* `wt egg run`, which then owns PTY allocation,
the gRPC socket, VTE, and reattach. Wrapping with srt inserts a process layer
between the egg and the agent, and hands mount setup to a component that knows
nothing about our tool-socket bind mounts, per-user home overrides, or the overlay
persist-back of agent config.

**3. Capability regression on exactly our differentiators.** Resource limits, deep
seccomp, env allowlists, and audit are the columns where our own landscape doc
says we beat every agent vendor. srt has none of them. Adopting it trades our
lead for someone else's.

**4. It is a research preview that says its config format may change.** Taking
`~/.srt-settings.json` as our enforcement contract means an upstream refactor
becomes our migration. Apache-2.0 makes vendoring legal, but vendoring a Node
codebase into a Go project is not a maintenance win.

**5. It does not fix the thing people assume it fixes.** srt has the *same*
Ubuntu 24.04 AppArmor problem we do — `apparmor_restrict_unprivileged_userns`
strips capabilities from the new user namespace, and the documented workaround is
the same sysctl or AppArmor profile we already emit guidance for. That confirms
our TODO item is a platform constraint, not a wingthing bug.

**6. The lock-in argument applies here too.** The reason wingthing has room to
exist is that people do not want their agent runtime owned by one vendor. Making
Anthropic's research preview our enforcement layer is a softer version of the same
bet we are arguing against.

### What about an optional second backend?

Rejected. A third `newPlatform` implementation selected when srt is present means
two enforcement paths to test across the whole e2e matrix, and the weaker path
(no cgroups, no env filtering, no audit) silently becomes the default on any
machine that happens to have Node installed. The testing bar in `CLAUDE.md` makes
this expensive, and the payoff is a capability we can build directly.

## What we took

**The network architecture.** Keep `CLONE_NEWNET` instead of stripping it, give
the jail no route out, and bind-mount a Unix socket to `DomainProxy` running on
the host side. This is the removed behavior that `linux.go` gave up on at the
time of the review:

```go
// Strip network namespace for agents that need network access.
if s.cfg.NetworkNeed >= NetworkLocal {
    flags &^= syscall.CLONE_NEWNET
}
```

We already have the pieces: `DomainProxy` with wildcard matching, socket
bind-mounts into the jail (commit `51b00c5`), and `AllowSockets` for the macOS
side. Go now carries the inherited-FD relay in-process — **socat is not needed**, which is
one fewer dependency than srt requires.

**SOCKS5 alongside HTTP CONNECT.** Our proxy's CONNECT tunnel can carry arbitrary
TCP bytes, but applications must either honor the HTTP proxy variables or speak
CONNECT themselves. srt also exposes SOCKS5, which supports a wider set of
off-the-shelf clients. That remains a useful compatibility addition, not a
stronger kernel boundary.

**The Unix-socket attack surface.** srt ships a seccomp filter specifically to
restrict Unix domain socket creation. Our filter denies 27 syscalls and **none of
them are socket-related**. Once egress runs over a bind-mounted socket, what else
inside the jail is reachable by `AF_UNIX` becomes a live question. Read their
filter before copying it — the exact allow/deny split matters, since the proxy
connection is itself a Unix socket — but treat this as a gap to audit, not a
detail to skip.

**Their honest limitations, as our test cases.** srt documents that programs
ignoring proxy env vars may fail or bypass filtering in edge cases, and that
mandatory-deny paths only affect existing files because of bind-mount semantics.
Both are exactly the failure modes our e2e sandbox battery should assert against
once we ship the proxy path. An agent that ignores `HTTPS_PROXY` must *fail
closed*, not silently reach the internet.

## The real adoption opportunity: translation, not execution

This is Phase 2 of `native-sandbox-landscape.md`, and srt makes it concrete.

Claude Code sandboxes itself using srt. Rather than running srt ourselves, we can
**generate its config** from `egg.yaml` — write a `~/.srt-settings.json` (or the
equivalent `settings.json` sandbox block) into the session directory so the
agent's own sandbox is configured by our policy.

That gives us:

- defense in depth — two independent layers enforcing one policy
- no new runtime dependency, since Claude Code brings its own srt
- a real answer to "egg.yaml is the universal sandbox spec"
- graceful degradation — if their format changes, we lose a redundant layer, not
  our enforcement

Our sandbox stays the boundary. Theirs becomes a second opinion we configure.

## Open questions

- Exact allow/deny split in their seccomp filter — needs a source read, not a
  README read.
- Whether their macOS Seatbelt profile handles anything our `apple.go` misses,
  particularly around `mDNSResponder` and keychain access.
- Whether `ignoreViolations` implies a violation-reporting channel we could
  consume for audit.
