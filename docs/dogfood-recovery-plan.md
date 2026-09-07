# Wingthing self-dogfood recovery plan

To: Bryan

Date: 2026-09-01

Status: recovery memo; no broad-rollout recommendation

## Verdict

Wingthing is **not self-dogfood ready yet**. The engineering evidence is strong,
and several canaries are genuinely impressive, but successful canaries do not
establish a reliable habit. Bryan still cannot hand an ordinary piece of daily
work to Wingthing with confidence that setup will be ready, completion will be
unambiguous, output will stay bounded, and failure will return him cleanly to
direct local work.

The next milestone is not another feature tour. It is three routine workdays in
which Bryan chooses Wingthing because it is the easiest safe way to delegate,
not because the test requires Wingthing to be used.

This memo separates three kinds of evidence:

- **Field-proven:** a real client, real binary, real provider, and real host
  crossed the boundary being claimed.
- **Integration-proven:** deterministic tests establish a protocol or component
  contract, but not Bryan's ordinary workflow.
- **Absent or unproven:** the contract is missing, or the relevant real-world
  path has not been exercised.

## What the evidence actually says

### Field-proven

The direct-control field report establishes these facts:

- One native connector listed an external physical macOS wing and the Bryan
  Ubuntu shared-roost wing as distinct `direct-webrtc` targets. It started real
  Codex on the Mac and Claude 2.1.243 on Bryan, observed
  `WINGTHING_PHYSICAL_MAC_OK` and `WINGTHING_PHYSICAL_BRYAN_OK`, and stopped only
  the two returned session IDs.
- On Bryan, a real outer Claude Sonnet exercised discovery, launch, wait,
  recovery, messaging, rename, and terminal read. Inner semantic run
  `t-20260825-222236-c9d5f8f2` returned `INNER_SONNET_OK_5bcaa8e`. It wrote
  `inner-sonnet-5bcaa8e.txt` with SHA-256
  `db13a3ab18d317662c5d14adb1cba918d90fdcba06a3da11598e4848b3739f54`,
  and a fresh outer agent sent durable message
  `msg-77d372dd-3094-46b4-8bb4-db3fac8cf8ec`.
- Direct terminal `2505ba6b` survived connector replacement. Roost restarts
  preserved its PID and two sessions that predated the feature deployment.
  With `hosted_relay: deny`, direct terminal reads continued while hosted PTY
  and tunnel payloads were rejected, and denial audit records contained no
  terminal or tunnel content.
- Bryan's organization/browser revalidation passed 17/17 checks with no console
  errors, page errors, or failed requests. It covered HTTPS, admin/member
  identity, role-based path filtering, mobile rendering, and a real terminal
  open, identity lock, resize, detach, reattach, and end lifecycle. The temporary
  identities and sessions were removed and SQLite integrity returned `ok`.
  Bryan was restored to `hosted_relay: allow` because it is also the browser/org
  compatibility canary.
- The native Ubuntu 24.04 WSL2 battery ran six installed agent CLIs and proved
  the non-root sealed jail, private PID namespace, secret and denied-path
  isolation, blocked mount calls, and proxy-bypass denial. This is strong WSL
  sandbox evidence. It is **not** evidence that a parent used the direct remote
  semantic workflow on WSL; the product brief and testing matrix call that gate
  out separately.

The field work also found and fixed defects that synthetic tests had missed:
MCP `_meta` rejection, shared-host command lookup and Claude credentials, PTY
text-plus-Enter framing, ineffective SIGTERM reporting, misleading sandbox
diagnosis, client/daemon architecture mismatch, and custom-roost URL/path
disclosure behavior. That history is evidence for keeping live dogfood in the
release gate, not evidence that no more such defects remain.

There is also a negative local field result. A bounded `agent_run` through this
branch attempted to write this memo as run
`t-20260901-231129-b8d52d4752054832`. It entered the macOS sandbox, then lost
its supervising MCP process before returning a typed result. The durable task
remained `running`, no memo was written, and the caller inspected SQLite to
understand the outcome. That is direct evidence that the current supervision
contract is not yet dependable.

### Integration-proven

The repository has broad deterministic coverage: unit and package tests,
`make test-integ`, race detection, vet, Debian and Ubuntu sandbox batteries,
organization-mode browser tests, compatibility fixtures, and provider-swap
tests. The shared operation registry pins local and HTTP MCP schemas. Direct
tests cover qualified wing IDs, authorization, reconnect, and payload routing.

The current local MCP implementation has useful semantic pieces:

- `agent_run` returns an owner-scoped stable run ID;
- `agent_status`, `agent_wait`, and `agent_result` expose durable task state and
  a bounded semantic result or error;
- a dead supervising process can cause a pending/running record to be marked
  failed when a later caller reloads it; and
- `agent_start` and the terminal operations provide a durable interactive PTY.

Those are components of the desired workflow. They have not yet been assembled
into one reliable parent-to-child experience. In particular, the provider-swap
smoke covers older prompt tools rather than the current run lifecycle, and the
testing matrix still records real OAuth-client semantic runs on the shared roost
as missing.

### Absent or not established

The following should not be described as shipped personal workflow:

- one typed preflight covering executable/version, model, provider auth, TTY
  needs, workspace trust, sandbox capability, and project toolchain;
- a first-class, idempotent workspace/worktree preparation transaction;
- a single run object joining semantic state, error, bounded result, artifacts,
  and an inspectable or take-over terminal;
- typed artifact references from semantic runs;
- reliable headless-to-interactive handoff to the same Codex or Claude
  conversation; the design document explicitly says provider session capture is
  not implemented;
- a tested direct semantic path on the physical WSL rig;
- fresh-human enrollment and real OAuth-client semantic runs on the
  organization host;
- native direct access to locked or passkey-protected wings;
- browser-direct terminal transport, peer-roost federation, workspace or memory
  synchronization, and remote schedule/service-identity/delivery controls; and
- evidence that Bryan voluntarily used the flow for ordinary work over several
  days without manual repair.

## Failures already observed

These are product failures even when the underlying subsystem behaved as
designed.

1. **Mandatory or ambient delegation disrupted unrelated work.** Wingthing was
   allowed to become an automatic route merely because it was available or was
   being discussed. That inserted orchestration into work that did not need it,
   added failure modes, and made the parent less useful. No transport canary
   measures this failure.
2. **Direct `wt run` had a hidden two-minute kill.** On `origin/main`, an absent
   timeout resolved to 120 seconds. Real agent work was therefore killed by an
   undeclared default and led to multi-day diagnosis during an OpenCode
   workflow. Commit `04ccc8d` on the current
   `fix/unlimited-direct-runs` branch changes an absent direct-task timeout to no
   deadline and retains an explicit skill/task timeout when supplied. The fix is
   not merged, so it is evidence of a known repair, not a released guarantee.
3. **Terminal idle was mistaken for agent completion.** `terminal_wait` idle is
   derived from time since PTY input/output. A TUI repaint, spinner, cursor
   update, or quiet tool call changes that signal; none proves semantic success.
   Earlier dogfood also matched a completion sentinel in the input prompt and
   falsely declared completion.
4. **Terminal reads can flood the parent.** `terminal_read` currently returns the
   complete ANSI snapshot as both plain text and base64, with no caller-supplied
   response bound. The VTE may contain substantial scrollback, so one diagnostic
   read can duplicate a large payload into model context.
5. **Detached Codex/PTY completion and supervision are unclear.** A persistent
   PTY surviving detachment is not the same as a completed agent run.
   `agent_start` returns a terminal session, while `agent_run` supplies semantic
   completion but no same-conversation terminal takeover. Headless runs do not
   capture the provider session ID needed for reliable resume. The failed memo
   run above also showed that losing the supervising MCP process can leave a
   durable `running` row until another caller forces orphan detection. A
   repainting terminal cannot bridge those contracts.
6. **Preflight is fragmented.** Provider login, expired auth, TTY-only prompts,
   trusted-workspace checks, executable and version discovery, sandbox support,
   and repository toolchain availability surface only after launch or through
   separate diagnostics. The parent cannot ask one safe question and know that a
   named agent/model/workspace is ready.
7. **Nested Wingthing hit SQLite-WAL initialization.** A Wingthing-launched child
   attempted to invoke Wingthing again against the same state store. Store open
   sets `journal_mode=WAL` for each handle and retries writer contention, but
   there is no recursion contract; the nested path failed at SQLite/WAL instead
   of being rejected up front as unsupported recursion. This is both a reliability
   bug and a misleading error boundary.
8. **Sandbox diagnosis pointed at the wrong security layer.** A WSL2 failure was
   reported as `security profile: kernel`, sending the operator toward AppArmor
   instead of the failed operation and actual WSL environment. The same class of
   diagnostic loss appeared on Bryan: the actual cause was `/tmp` mode 0755,
   while the initial guidance blamed a kernel security profile. Restoring 1777
   made `wt doctor` report Linux sandbox availability. The field fix improved the
   diagnostic, but the rule remains: report the failed operation and OS error
   before suggesting a security layer.

## Operating rule: never ambient Wingthing

Wingthing is an explicit tool, never ambient policy.

- The parent delegates only when Bryan or the parent deliberately selects a
  named wing, an exact workspace, an agent, a model, and a bounded task.
- Merely mentioning Wingthing, opening this repository, installing its skill,
  or having its MCP server available must not cause delegation.
- The parent must be able to edit, inspect, build, test, and answer directly with
  its ordinary local tools. **Direct local work is the escape hatch**, not an
  error condition.
- If preflight is not green, the parent returns the typed reason and either uses
  direct local work or asks Bryan what boundary matters. It must not enter a
  terminal-repair loop just to satisfy a Wingthing policy.
- A delegated child does not receive Wingthing orchestration authority by
  default. Explicit nested orchestration must use a separate state directory,
  an explicit depth limit, and a separately authorized task. Re-entry against
  the parent's database fails immediately with a typed `recursion_denied` error.

This rule is part of acceptance, not just documentation. A parent that cannot
choose the direct route is not safe enough for daily use.

## North-star personal workflow

The first complete workflow should be deliberately narrow:

1. A parent agent decides that delegation is useful and selects a named local or
   remote wing plus an exact existing workspace or requested worktree.
2. It calls one preflight for that wing, workspace, agent, explicit model, and
   execution mode. The response says whether the provider is authenticated,
   whether TTY interaction or workspace trust is pending, whether the executable
   and project toolchain exist, and which sandbox boundary will actually apply.
3. It prepares or validates the workspace through an idempotent typed operation.
4. It launches a semantic Codex or Claude job with explicit agent, model,
   deadline policy, output bound, artifact policy, and recursion depth.
5. Launch returns a stable run ID and typed references for lifecycle state,
   result, error, artifacts, and, when supported, the associated terminal.
6. The parent waits on typed lifecycle events. It may read a bounded terminal
   tail for diagnosis, attach as an observer, or explicitly take control. The
   response states whether same-conversation takeover is supported; it never
   guesses.
7. Success comes only from provider/process completion plus the structured
   result contract. Failure, cancellation, auth-required, needs-input, orphaned,
   and stalled are distinct terminal or health states. Terminal quiet or repaint
   text is never success.

No single current operation provides this complete flow. The plan below is to
close that gap without waiting for federation or a new browser transport.

## Smallest ordered recovery plan

### Immediate P0 contracts

1. **Enforce opt-in use and a recursion boundary.** Add invocation metadata for
   parent run, state-store identity, and delegation depth. Do not expose the
   Wingthing orchestration tool to a child by default. Reject same-store re-entry
   before opening SQLite, with remediation that points to direct local work or an
   explicitly separate state directory. Add a regression for the observed WAL
   failure.
2. **Add a bounded terminal operation without breaking `terminal_read`.** Keep
   the existing operation and schema for compatibility. Add `terminal_tail` (or
   an equivalently named additive operation) with required server-enforced
   `max_bytes`, a small default and hard maximum, `truncated`, total/returned
   byte counts, and an optional cursor. Return one representation by default;
   base64 is opt-in only when bytes are not valid text. Parent-agent instructions
   use this operation and reserve full `terminal_read` for explicit legacy/debug
   use.
3. **Make preflight and health typed.** Add one `agent_preflight` contract for
   wing reachability, workspace/path authorization, executable and version,
   requested model support, provider auth state, TTY/trust prompts, sandbox
   enforcement, writable workspace, and declared project toolchain. It reports
   `ready`, individual checks, evidence source, and actionable remediation without
   returning credentials. Unknown is not ready. Separately report run health
   (`responsive`, `needs_input`, `stalled`, `disconnected`, `orphaned`, or
   `unknown`) without conflating it with lifecycle completion.
4. **Finish the semantic run object.** Keep the stable run ID and bounded result,
   but define one versioned lifecycle enum and typed error categories. Add bounded
   artifact references containing owner, wing, workspace-relative path, media
   type, size, and digest; do not inline arbitrary files. Associate a terminal
   reference only when one really exists, and advertise observer/takeover/resume
   capabilities per provider. Completion must come from child exit and structured
   provider events. Make supervision durable across client disconnect, or make a
   client-owned lifecycle transition immediately to a typed terminal failure; a
   task must never remain indefinitely `running` after its supervisor exits. A
   legacy PTY can remain inspectable, but its screen is never the result authority.
5. **Prepare workspaces semantically.** Start with an idempotent
   `workspace_prepare` that validates an existing directory or creates/reuses a
   Git worktree at an explicit revision, runs bounded argv-based setup steps, and
   returns a workspace ID, canonical path, revision, reuse decision, and bounded
   setup results. Enforce allowed roots and symlink checks. Do not add workspace
   copying or cross-wing synchronization to this slice.
6. **Adopt a deliberate long-run policy.** Merge `04ccc8d` or its reviewed
   equivalent so direct `wt run` has no hidden deadline when none is requested.
   Add an explicit direct-run timeout option rather than relying only on skill
   frontmatter. The north-star semantic workflow always sends an explicit job
   deadline and returns the effective value; an existing omitted `agent_run`
   timeout may retain its documented 900-second compatibility default. A wait
   timeout bounds only the wait request, never the underlying job. Test success,
   explicit timeout, cancellation, provider failure, and a run lasting well past
   two minutes.
7. **Prove the local loop before expanding it.** Drive the real stdio MCP process
   from a parent Codex in this repository, launch a child Codex with explicit
   `gpt-5.6-sol`, and complete a useful review through preflight, workspace,
   run/wait/result, bounded tail, and artifact references. No manual TTY repair,
   raw snapshot read, database surgery, or inferred completion is allowed.

P0 is complete only when this local loop is boring and the deterministic gates
below are green. Existing direct and browser behavior must remain compatible,
but new distributed product work should not be mixed into this recovery slice.

### Later work

After the local contract is stable:

1. Carry the same versioned preflight, workspace, run, health, artifact, and
   terminal-reference contract over direct remote MCP. Re-run physical Mac, WSL,
   and Bryan organization canaries, including offline/reconnect and mixed-version
   behavior. Finish fresh enrollment and protected-wing/passkey authorization.
2. Add the human/browser view of headless runs and, separately, browser-direct
   transport. Neither is required to prove Bryan's initial parent-agent workflow.
3. Design federation for independently administered roosts rather than treating
   a multi-wing gateway as federation.
4. Revisit schedules, service identities, delivery targets, shared memory/context,
   broader remote prompt/loop/swarm surfaces, and provider-managed environments
   only after the personal delegation loop is trustworthy.

## Test and dogfood ladder

Every higher rung requires every lower rung. A failure blocks merge, release, or
rollout at that rung; a green canary elsewhere does not waive it.

| Rung | Pass gate | Fail gate |
| --- | --- | --- |
| 1. Unit contracts | Exact schemas and bounds for preflight, lifecycle, health, artifacts, recursion, workspace preparation, terminal tail, and timeout precedence. Include malformed/oversized inputs, same-store nesting, symlink escape, missing auth, cancellation, orphaning, and provider failure. | Any state is inferred from terminal text; an oversized response is possible; same-store recursion reaches SQLite; zero/omitted timeout changes meaning without a test. |
| 2. Integration/MCP contracts | The actual `wt mcp stdio` process returns a stable run ID, typed states and errors, bounded result/tail/artifacts, and preserves ownership across reconnect. Disconnect during startup and execution yields continued durable supervision or an immediate typed terminal failure. Existing `terminal_read` clients still work. Direct CLI, local MCP, and shared registry schemas agree where claimed. | Only an in-process handler passes; a run remains pending/running after its supervisor is gone; a client must inspect SQLite or scrape ANSI; the compatibility operation breaks. |
| 3. Local Codex Sol review | From a normal parent session in this repository, preflight and prepare the chosen workspace, then run a child Codex with explicit `gpt-5.6-sol` on a real review task. It completes through typed wait/result, produces the declared evidence artifact, and permits bounded inspection or truthful takeover refusal. | Manual Enter/login/trust repair, raw snapshot use, nested-WAL error, implicit model, ambiguous completion, or parent context flooding. |
| 4. Long run and privacy | The same semantic route runs for at least 150 seconds and succeeds without the former 120-second kill. Explicit timeout and cancel controls also work. Canary secrets are absent from MCP payload records, logs, audit, errors, and artifacts; no terminal/result response exceeds its declared cap or duplicates a snapshot as base64. | Hidden kill, lost supervisor state, unbounded/duplicated response, secret match, or cleanup requiring manual process/database repair. |
| 5. Direct physical Mac | A built connector selects the named Mac and workspace, runs preflight and a real bounded semantic job with explicit model, reconnects, returns qualified references, and cannot act on another wing by guessed ID. | Session-only output marker, relay fallback, unqualified resource, or reconnect loses truthful state. |
| 6. Direct physical WSL | Repeat the actual parent-to-child direct semantic path on the authorized WSL rig, in addition to the native sandbox battery. Record host/kernel, binary, provider/version, model, isolation, duration, and exact result/artifact. | Treating the existing sandbox battery as a remote-workflow pass, misleading security remediation, missing fixture counted as skip, or TTY/auth repair after launch. |
| 7. Bryan organization host | Re-run the 17/17 browser/org compatibility canary, then add real owner and member semantic runs through the claimed native/HTTP MCP surfaces. Verify path bounds, per-owner provider homes, outsider denial, detach/reconnect, typed completion, cleanup, SQLite integrity, and no content in audit. | Reusing the service identity as proof of fresh enrollment, cross-owner leakage, browser regression, hosted policy drift, or an OAuth semantic path left untested while claimed. |
| 8. Three routine workdays | For three consecutive routine workdays Bryan voluntarily delegates at least one normal review, edit, or test task through the north-star flow. Across the three days include a local run, a remote run, and an inspect/takeover or recovery event. Each starts without a bespoke canary script and ends in a useful typed result or an honest actionable failure. There is no forced delegation, manual terminal repair, secret/snapshot flood, stale run, or database/process cleanup. Direct local work remains immediately available. | Any day requires orchestration because policy made it mandatory; completion must be guessed; recovery consumes more effort than doing the task directly; or a fixed defect recurs. After a fix, restart the three-day count. |

Rungs 1-4 are the P0 merge gate. Rungs 5-7 are the release-candidate gate.
Rung 8 is the minimum broad-rollout usability gate.

## Evidence ledger

Update this ledger after every behavior change. Record commit, binary digest,
host, provider/model/version, duration, exact assertion, logs/artifact digest,
and cleanup result; do not record secrets.

| Changed boundary | Minimum evidence to rerun |
| --- | --- |
| Direct-run timeout or cancellation | Timeout unit tests, explicit-timeout failure, cancel/process-tree test, and the local run lasting at least 150 seconds. |
| Terminal read/tail or PTY lifecycle | Schema compatibility, hard response cap, invalid-text/base64 case, repaint/idle negative control, detach/reattach, actual exit, and context-volume measurement. |
| Semantic state, health, result, supervision, or provider adapter | Mock success/failure/auth/needs-input/orphan cases, disconnect during startup/execution, reconnect/status/result, no stale `running` record, exact provider argv/events, local real Codex Sol review, and one real Claude control. |
| Preflight or sandbox diagnosis | Missing binary, expired/missing auth, TTY/trust prompt, missing toolchain, path denial, Mac Seatbelt, native WSL namespace/security checks, and exact failing-operation remediation. |
| Recursion or store handling | Same-store nested call rejected before DB open, separate-state explicit nesting bounded by depth, concurrent-store regression, WAL contention test, and database integrity. |
| Workspace preparation or artifacts | Existing directory, create/reuse worktree, exact revision, setup failure, symlink/path denial, output truncation, artifact size/digest/ownership, and cleanup. |
| Direct transport, identity, or authorization | Two-wing qualified routing, offline/revoked/locked denial, reconnect, no relay payloads, Mac and WSL physical canaries, and Bryan owner/member/outsider rows. |
| Browser or organization behavior | Existing 17/17 Bryan lifecycle plus current seeded organization-mode, compatibility, enrollment, per-owner credential, and audit-content gates. |

## Non-goals and deferrals

This recovery slice does not include:

- making Wingthing mandatory for Codex, Claude, or repository work;
- breaking or silently changing `terminal_read`;
- claiming terminal idle, a sentinel, a startup banner, or a PTY marker is
  semantic completion;
- synchronizing repositories, untracked files, credentials, or memory between
  wings;
- browser-direct transport or a redesigned browser session manager;
- peer-roost federation or a global cross-roost target registry;
- schedule MCP CRUD, unattended service accounts, Slack/email delivery, or
  general automation product work;
- generated application hosting;
- proving every cataloged provider/model before the Codex-and-Claude personal
  loop is reliable; or
- broad rollout based on unit, integration, security, browser, or one-off
  canary success without the three-routine-workday gate.

## Resume point

As of this memo, the working branch is `fix/unlimited-direct-runs` at `04ccc8d`,
one commit ahead of `origin/main` (`72c7be5`). That commit removes the hidden
120-second default for direct tasks but is not merged. The bounded terminal
operation, unified preflight, recursion guard, typed artifact/terminal references,
workspace preparation, and cohesive north-star workflow described here are a
plan, not current features.

The next implementation session should begin with failing contract tests for
same-store recursion, bounded terminal tail, and timeout semantics, then build
the local Codex Sol review path. Do not begin with browser, federation, or new
rollout copy.
