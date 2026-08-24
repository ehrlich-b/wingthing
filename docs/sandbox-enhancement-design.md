# Sandbox enhancement design

Status: design
Reviewed: 2026-08-09

Consolidates what we learned from building the egg sandbox, from the jailbreak
testing in `egg-sandbox-design.md`, and from evaluating Anthropic's
sandbox-runtime (`sandbox-runtime-evaluation.md`). Every change here is additive
and backwards compatible **except one deliberate tightening**, called out
explicitly in its own section.

## Part 1 — What we learned

### The sandbox is a policy engine, not a jail

The insight that has held up best: **the sandbox is `egg.yaml` plus auto-drilled
agent holes.** The user declares what the *task* needs; the system adds what the
*agent* needs from `agentProfiles`. Config authors never learn where Claude
stores its tokens.

The corollary we underweighted: because the holes are automatic, **nobody can see
them**. There is no way today to ask what the effective policy is. That makes the
sandbox unauditable by the person relying on it, and it makes every "is this
safe?" conversation a code-reading exercise.

### Platform asymmetry is the central defect

macOS enforces domain filtering: when the proxy is up, `apple.go` emits
`(deny network*)` and allows only `localhost:<proxyPort>`. Egress is genuinely
constrained.

Linux does not. `linux.go:341` strips `CLONE_NEWNET` whenever
`NetworkNeed >= NetworkLocal`, so any agent needing HTTPS gets the **full host
network**. The proxy still starts and `HTTPS_PROXY` is still exported, but nothing
enforces it. A `curl` that ignores the variable reaches anything.

So the same `egg.yaml` produces a real boundary on one platform and a suggestion
on the other. Documentation calls this "by design"; it is a defect with a
workaround, and it is the highest-value thing to fix.

### Advisory controls are not controls

`HTTPS_PROXY` is a request. Anthropic's srt hit the same wall and documents it:
programs that ignore proxy env vars "may bypass filtering in some edge cases."
The lesson generalizes — **any control the sandboxed process can decline is not a
control.** It belongs in the enforcement layer or it does not count.

### Deny-by-path has semantic holes

Two independent findings converge:

- srt documents that mandatory-deny paths only affect *existing* files, because
  bind-mount semantics cannot cover a path that does not exist yet.
- Our own finding #4: denying `~/.ssh` did not stop SSH, because `SSH_AUTH_SOCK`
  reached the agent socket without touching the denied directory. Fixed by
  stripping the variable, but the class remains: **a capability can arrive by a
  channel that is not a path.**

Our seccomp filter denies 27 syscalls, none socket-related. srt ships a filter
specifically to restrict Unix domain socket creation. That is the same class of
hole, and we have not closed it.

### Writable state is a persistence channel

Finding #5: `~/.claude/settings.json` is writable because the agent needs it, and
it configures hooks that run on *future* invocations — including outside the
sandbox. The overlay work on Linux (`setupOverlayHome`) already gives us the
mechanism to fix this properly, and it produces a second capability for free: the
overlay upper directory **is a diff of everything the session changed.**

We built an audit trail and did not notice we had one.

### Credentials are a design choice, not a constraint

`egg-sandbox-design.md` says "you can't hide the agent's credentials from the
agent." True of the *agent process*, false of the *task*. We already run a
privilege-broker pattern for Slide's tools. Applied to the agent's own API key,
the token never enters the sandbox at all.

## Part 2 — The compatibility contract

These do not change:

- **The `fs:` DSL.** `ro:P`, `rw:P`, `deny:P`, `deny-write:P`, bare path = `rw`.
  Parsing stays in `ParseFSRules`.
- **`network:` scalar and list forms.** `none`/`""` → no network. `"*"` → full
  network, unfiltered, forever. A domain list → filtered.
- **`env:` scalar and list forms**, including `"*"`.
- **`resources:`** keys and units.
- **`base:` inheritance**, including per-section masks and `base: none`.
- **Default config semantics** — `ro:/`, `rw:./`, cache dirs writable, sensitive
  dirs denied, `deny-write:./egg.yaml`.
- **`EnforcementError` with no silent fallback.** If the platform cannot enforce
  what was asked, the egg fails.

New configuration is expressed by growing `network:` into an optional mapping
form, exactly as `base:` already does (`BaseField.UnmarshalYAML` accepts a scalar
or an object). Existing scalar and list configs parse unchanged.

```yaml
# All three remain valid and mean what they mean today.
network: none
network: "*"
network: ["*.anthropic.com", "github.com"]

# New optional third form.
network:
  domains: ["*.anthropic.com", "github.com"]
  local_ports: [11434]      # host loopback services to forward
  mode: enforce             # enforce | observe
  log: true
```

## Part 3 — The enhancements

### 3.1 Linux egress: keep the namespace, force the proxy

Reverse `cloneFlags()`. Retain `CLONE_NEWNET` for every level below
`NetworkFull`, leaving the jail with loopback and no route out. Egress becomes a
Unix socket bind-mounted into the jail, connected to `DomainProxy` on the host
side. An in-jail forwarder listens on loopback and relays over that socket.

This is the design srt shipped, confirming the approach. Two deliberate
divergences: we do the relay in-process in Go rather than depending on `socat`,
and we keep raw namespaces rather than requiring `bubblewrap`. `wt` stays a
single static binary.

**Fail closed.** If the proxy is unreachable or the agent ignores the proxy
settings, the connection fails. It must never fall back to unfiltered egress —
that is the failure mode that made the current Linux path fictional.

### 3.2 The loopback trap (compatibility-critical)

**Retaining the network namespace breaks local model providers**, and this is not
obvious. Inside a new netns, `127.0.0.1` is the *jail's* loopback, not the host's.
Ollama on `127.0.0.1:11434`, LiteLLM, and every `WT_PROVIDER_BASE_URL` pointing at
localhost become unreachable.

That would break the `ollama` profile, the provider-substitution work on this
branch, and `make test-provider-swap` — our release gate.

The fix is the same mechanism as egress: for each declared local port, bind-mount
a Unix socket and run an in-jail forwarder listening on `127.0.0.1:<port>` that
relays to the host's port. Declared ports only; nothing implicit.

For backwards compatibility, when `network:` is a plain domain list containing
loopback literals (`localhost`, `127.0.0.1`, `::1`) — which is exactly what the
current agent profiles emit — the resolver **infers** the local ports it needs
from the agent profile and the provider URL. Existing configs keep working with no
edit. `local_ports` is for anything the profile cannot infer.

### 3.3 SOCKS5 beside HTTP CONNECT

`DomainProxy` speaks HTTP CONNECT only, so non-HTTP TCP has no filtered path at
all. Once egress is actually forced through the proxy, anything that is not HTTP
would simply break. Adding a SOCKS5 listener on the same socket keeps `git://`,
SSH-to-forge, and database clients working under a domain policy.

Purely additive — no existing config selects a proxy protocol.

### 3.4 Close the `AF_UNIX` gap

With egress running over a bind-mounted socket, what else is reachable by
`AF_UNIX` inside the jail becomes a live question. Path-based sockets are bounded
by the mount namespace; abstract-namespace sockets are bounded by the network
namespace, which is one more reason to retain it.

Action: audit what sockets are reachable in a jail, and read srt's `apply-seccomp`
filter before copying its allow/deny split — the proxy connection is itself a Unix
socket, so a naive "deny AF_UNIX" breaks egress. Extend `deniedSyscallsCommon`
only with a verified split and an e2e test that proves the proxy still works.

### 3.5 Credential broker

The agent talks to a local endpoint that injects the API key on the way out; the
key never exists inside the sandbox. This composes with the domain proxy — the
same socket, one more responsibility — and closes known limitation #2, which the
current docs describe as unfixable.

Opt-in, additive: `credentials: broker` on the agent profile. Default remains
passthrough, so nothing changes for existing configs.

### 3.6 Egress log

`DomainProxy.ServeHTTP` already sees every CONNECT and logs only the blocked ones.
Log both, per session, and expose `wt egg net <id>`: what the agent contacted,
what was refused. Cheap, and it converts the proxy from a gate into evidence.

Pairs with the existing audit recording. Default on; `network.log: false` opts out.

### 3.7 Session diff and the approval gate

The overlay upper directory on Linux is already a complete record of what the
session wrote. Surface it as `wt egg diff <id>`.

Extending the overlay from `HOME` to the project directory turns this into an
approval gate: the agent works normally, and the operator reviews before anything
persists. This is a differentiated version of the worktree-isolation pattern the
market has settled on (`competitive-landscape.md`), and most of the mechanism
exists in `setupOverlayHome`.

Additive and opt-in — `fs: ["rw:./"]` keeps writing straight through by default.

### 3.8 Ephemeral agent home

Snapshot/restore exists (`internal/egg/snapshot.go`) and is a partial fix for the
persistence attack. The overlay makes the complete fix available: the agent's
config directory is a COW layer, changes are discarded on exit unless explicitly
persisted.

Opt-in via `home: ephemeral`, because some workflows legitimately want the agent
to remember. Default stays persistent.

### 3.9 Make the policy visible

`wt egg explain [--config egg.yaml]` renders the effective policy: mounts, denies,
network need, resolved domains, forwarded local ports, and — critically — **which
holes were auto-drilled for the agent and why.**

This is the answer to the first learning. It is read-only, additive, and it is
what makes every other item in this doc reviewable.

### 3.10 Native sandbox as a second opinion

Generate the agent's own sandbox config from `egg.yaml` (srt's settings JSON for
Claude Code, and the equivalents catalogued in `native-sandbox-landscape.md`), so
two independent layers enforce one policy. Our sandbox stays the boundary; theirs
becomes redundancy we configure. No runtime dependency, and graceful degradation
if their format churns.

## Part 4 — The one intentional break

**On Linux, a domain-list `network:` policy currently yields unrestricted egress.
After 3.1 it yields filtered egress.** Any workload that quietly relied on the
hole — pulling from an undeclared registry, `git push` over SSH, a webhook to an
unlisted host — stops working.

This is the "less anything insecure" carve-out. It is also the entire point: the
policy starts meaning what it says. But it will break real setups, so it ships
with a migration path rather than a flag day.

**`mode: observe`.** The proxy runs and logs every allowed and denied domain, but
egress is not constrained. Operators run their real workloads, read
`wt egg net <id>`, and add the domains they actually need.

Sequence:

1. Ship `observe` as the default on Linux, with a startup warning naming the
   session and pointing at `wt egg net`.
2. Ship `enforce` as the default on Linux one release later. macOS is already
   enforcing and does not change.
3. `network: "*"` remains the permanent, explicit, documented escape hatch, and
   its meaning never changes.

Two things must be true before step 2: the loopback forwarder (3.2) works, and
SOCKS5 (3.3) exists. Without them, "enforce" breaks local models and all non-HTTP
traffic, and we would have traded a security defect for an availability one.

## Part 5 — Phasing

| Phase | Contents | Breaks anything? |
|---|---|---|
| 1 | `wt egg explain` (3.9), egress log (3.6) | No — read-only |
| 2 | Loopback forwarder (3.2), SOCKS5 (3.3) | No — additive |
| 3 | Linux netns egress in `observe` mode (3.1) | No — logs only |
| 4 | `enforce` default on Linux | **Yes — intended** |
| 5 | `AF_UNIX` audit (3.4), credential broker (3.5) | No — opt-in |
| 6 | Session diff (3.7), ephemeral home (3.8) | No — opt-in |
| 7 | Native translation (3.10) | No — redundant layer |

Phase 1 first on purpose: it is impossible to argue about the later phases without
being able to see the effective policy, and the egress log is what tells operators
which domains to declare before enforcement lands.

## Part 6 — Test plan

Per the three-tier bar in `CLAUDE.md`, sandbox claims need E2E — an enforcement
feature that is available but broken is a failure, never a skip.

**Unit.** Effective-policy resolution: domain merge, loopback inference from
profiles and `WT_PROVIDER_BASE_URL`, `network:` mapping form parsing, and that
scalar/list forms produce byte-identical configs to today (this is the
backwards-compatibility guard, and it should be table-driven over real `egg.yaml`
fixtures).

**Integration.** Proxy allow/deny decisions including wildcards; SOCKS5 handshake;
forwarder relays loopback to a fake host service; `observe` mode logs without
blocking.

**E2E, on a real kernel, inside the jail:**

- `curl https://<undeclared-host>` **fails** under `enforce`.
- A client that ignores `HTTPS_PROXY` **fails closed** rather than reaching the
  network. This is the test that proves the fix is real.
- `curl http://127.0.0.1:11434` reaches the host's ollama through the forwarder.
- A declared domain succeeds; the egress log records both outcomes.
- Existing sandbox battery still passes: deny paths, home write isolation,
  seccomp, `/root` denial, block devices.

**Regression gate.** `make test-provider-swap` must still pass end-to-end after
Phase 3. It is the only test that exercises real local models through the sandbox,
so it is the canary for the loopback trap.

## Open questions

- srt's exact `AF_UNIX` allow/deny split (source read required).
- Whether the in-jail forwarder should be a separate static helper or a re-exec of
  `wt` like `_deny_init` — the latter is fewer artifacts but a larger process.
- Whether `observe` should be time-boxed, so a session cannot sit unenforced
  forever because someone forgot to flip it.
