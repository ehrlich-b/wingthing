# Sandbox enhancement design

Status: implementation record plus remaining roadmap; Linux rootless egress enforcement shipped
Reviewed: 2026-08-27

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

Linux formerly stripped `CLONE_NEWNET` whenever an agent needed connectivity, so
any agent needing HTTPS received the **full host network**. The proxy still
started and `HTTPS_PROXY` was exported, but nothing enforced it. A `curl` that
ignored the variable reached anything. Section 3.1 is now implemented; this
paragraph records the defect that motivated it.

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
- **`network:` scalar and list forms.** `none`/`""` → no network. `"*"` → any
  destination supported by the platform transport. A domain list → filtered.
  On Linux the transport is currently HTTP CONNECT plus declared loopback TCP;
  it is not a general routed interface.
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
  agent_domains: merge      # merge (default) | none
```

## Part 3 — The enhancements

### 3.1 Linux egress: keep the namespace, force the proxy

`cloneFlags()` now retains `CLONE_NEWNET` for every network level, including
`NetworkFull`, leaving the jail with loopback and no route out. Before clone, the
parent creates a Unix socketpair beside `DomainProxy`. The child inherits only
one endpoint, raises loopback, and listens on the proxy and declared local ports.
Accepted TCP sockets cross the socketpair with `SCM_RIGHTS`; the host validates
the requested listener before dialing. The bridge FD is close-on-exec before the
agent starts.

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

The fix is the same inherited-FD mechanism as egress: for each declared local
port, `_deny_init` listens on `127.0.0.1:<port>` and relays to that exact host
loopback port. Declared ports only; nothing implicit.

For backwards compatibility, when `network:` is a plain domain list containing
loopback literals (`localhost`, `127.0.0.1`, `::1`) — which is exactly what the
current agent profiles emit — the resolver **infers** the local ports it needs
from the agent profile and the provider URL. Existing configs keep working with no
edit. `local_ports` is for anything the profile cannot infer.

### 3.3 SOCKS5 beside HTTP CONNECT (roadmap)

`DomainProxy` speaks HTTP CONNECT only. A CONNECT tunnel can carry any TCP
protocol and currently permits any destination port on an allowed host, but the
application must honor the HTTP proxy variables or explicitly speak CONNECT.
Adding a SOCKS5 listener on the same socket would let more `git://`, SSH, and
database clients work without application-specific CONNECT configuration.

Purely additive — no existing config selects a proxy protocol.

### 3.4 Close the `AF_UNIX` gap (roadmap)

With egress running over a bind-mounted socket, what else is reachable by
`AF_UNIX` inside the jail becomes a live question. Path-based sockets are bounded
by the mount namespace; abstract-namespace sockets are bounded by the network
namespace, which is one more reason to retain it.

Action: audit what sockets are reachable in a jail, and read srt's `apply-seccomp`
filter before copying its allow/deny split — the proxy connection is itself a Unix
socket, so a naive "deny AF_UNIX" breaks egress. Extend `deniedSyscallsCommon`
only with a verified split and an e2e test that proves the proxy still works.

### 3.5 Credential broker (roadmap)

The agent talks to a local endpoint that injects the API key on the way out; the
key never exists inside the sandbox. This composes with the domain proxy — the
same socket, one more responsibility — and closes known limitation #2, which the
current docs describe as unfixable.

Opt-in, additive: `credentials: broker` on the agent profile. Default remains
passthrough, so nothing changes for existing configs.

### 3.6 Egress log (partially implemented)

`DomainProxy.ServeHTTP` now retains a bounded in-memory record of CONNECT attempts.
That record is exercised by tests but is not yet attached to a durable session or
exposed through a `wt egg net` command. A future slice should persist allowed and
refused attempts per session and add a read-only CLI/MCP view. There is no shipped
`network.log` configuration field.

### 3.7 Session diff and the approval gate (roadmap)

The overlay upper directory on Linux is already a complete record of what the
session wrote. Surface it as `wt egg diff <id>`.

Extending the overlay from `HOME` to the project directory turns this into an
approval gate: the agent works normally, and the operator reviews before anything
persists. This is a differentiated version of the worktree-isolation pattern the
market has settled on (`competitive-landscape.md`), and most of the mechanism
exists in `setupOverlayHome`.

Additive and opt-in — `fs: ["rw:./"]` keeps writing straight through by default.

### 3.8 Ephemeral agent home (roadmap)

Snapshot/restore exists (`internal/egg/snapshot.go`) and is a partial fix for the
persistence attack. The overlay makes the complete fix available: the agent's
config directory is a COW layer, changes are discarded on exit unless explicitly
persisted.

Opt-in via `home: ephemeral`, because some workflows legitimately want the agent
to remember. Default stays persistent.

### 3.9 Make the policy visible (implemented)

`wt egg explain [--config egg.yaml]` renders the effective policy: mounts, denies,
network need, resolved domains, forwarded local ports, and — critically — **which
holes were auto-drilled for the agent and why.**

This is the answer to the first learning. It is read-only, additive, and it is
what makes every other item in this doc reviewable.

### 3.10 Native sandbox as a second opinion (roadmap)

Generate the agent's own sandbox config from `egg.yaml` (srt's settings JSON for
Claude Code, and the equivalents catalogued in `native-sandbox-landscape.md`), so
two independent layers enforce one policy. Our sandbox stays the boundary; theirs
becomes redundancy we configure. No runtime dependency, and graceful degradation
if their format churns.

## Part 4 — Intentional Linux network tightening

**Before the 3.1 implementation, a Linux domain-list `network:` policy yielded
unrestricted egress. It now yields filtered CONNECT egress.** Any workload that
quietly relied on the hole—pulling from an undeclared registry or calling an
unlisted webhook—stops working.

There is a second, explicit compatibility change: on Linux `network: "*"` now
allows any TCP destination presented through HTTP CONNECT, but no longer creates
a general routed interface. Ordinary raw-socket clients, UDP, ICMP, and programs
that ignore proxy configuration fail. macOS `network: "*"` retains its unrestricted Seatbelt
network behavior. This asymmetry is visible in `wt egg explain`; it must not be
described as byte-for-byte runtime compatibility.

This is the "less anything insecure" carve-out. It is also the entire point: the
policy starts meaning what it says. But it will break real setups, so it ships
with a migration path rather than a flag day.

**`mode: observe`.** The proxy permits CONNECT destinations outside the declared
domain set and records them in its bounded in-memory event buffer. It does not
restore a raw route, so programs that ignore proxy configuration still fail. The
event buffer does not yet have a user-facing per-session command; operators must
currently use proxy log output and `wt egg explain` while diagnosing policy.

The shipped sequence differs from the original rollout sketch below: `enforce` is
the default, while `mode: observe` is an explicit diagnostic choice. Both retain
the route-less namespace, so ignoring `HTTPS_PROXY` still provides no alternate
network path. The list is kept as design history:

1. Ship `observe` as the default on Linux, with a startup warning naming the
   session and pointing at the planned per-session egress view.
2. Ship `enforce` as the default on Linux one release later. macOS is already
   enforcing and does not change.
3. `network: "*"` remains the explicit any-CONNECT-destination value on Linux;
   it is not a promise of arbitrary network protocols.

The loopback forwarder (3.2) shipped before enforcement. SOCKS5 (3.3) did not, so
non-CONNECT traffic remains a documented limitation rather than a supported
compatibility path.

## Part 5 — Phasing

| Phase | Contents | Breaks anything? |
|---|---|---|
| 1 | `wt egg explain` (3.9), bounded proxy event buffer (part of 3.6) | No — read-only |
| 2 | Loopback forwarder (3.2) | No — additive |
| 3 | Linux route-less netns and inherited proxy relay (3.1) | **Yes — intended security tightening** |
| 4 | User-facing egress history and SOCKS5/general TCP (3.3, 3.6) | No — additive |
| 5 | `AF_UNIX` audit (3.4), credential broker (3.5) | No — opt-in |
| 6 | Session diff (3.7), ephemeral home (3.8) | No — opt-in |
| 7 | Native translation (3.10) | No — redundant layer |

The implemented pieces expose the effective policy before launch and retain a
bounded event record inside the proxy. Durable per-session egress inspection is
still required before documentation can tell users to query historical attempts.

## Part 6 — Test plan

Per the three-tier bar in `CLAUDE.md`, sandbox claims need E2E — an enforcement
feature that is available but broken is a failure, never a skip.

**Unit.** Effective-policy resolution: domain merge, loopback inference from
profiles and `WT_PROVIDER_BASE_URL`, `network:` mapping form parsing, and that
scalar/list forms produce byte-identical configs to today (this is the
backwards-compatibility guard, and it should be table-driven over real `egg.yaml`
fixtures).

**Integration.** Proxy allow/deny decisions including wildcards; forwarder relays
loopback to a fake host service; `observe` mode records without blocking. Add a
SOCKS5 handshake test when that protocol is implemented.

**E2E, on a real kernel, inside the jail:**

- `curl https://<undeclared-host>` **fails** under `enforce`.
- A client that ignores `HTTPS_PROXY` **fails closed** rather than reaching the
  network. This is the test that proves the fix is real.
- `curl http://127.0.0.1:11434` reaches the host's ollama through the forwarder.
- A declared domain succeeds; the proxy event buffer records both outcomes.
- Existing sandbox battery still passes: deny paths, home write isolation,
  seccomp, `/root` denial, block devices.

**Regression gate.** `make test-provider-swap` must pass end-to-end after any
relay change. It is the only test that exercises real local models through the
sandbox, so it is the canary for the loopback trap.

## Open questions

- srt's exact `AF_UNIX` allow/deny split (source read required).
- SOCKS5 or another explicit proxy path for TCP clients that cannot use CONNECT,
  without weakening the route-less namespace guarantee.
- Whether `observe` should be time-boxed, so a session cannot sit unenforced
  forever because someone forgot to flip it.
