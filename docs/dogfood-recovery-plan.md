# Wingthing dogfood recovery plan

Status: internal operating plan, 2026-09-01. This is deliberately not a launch plan. Wingthing has enough real successes to justify finishing the work; it is not yet reliable enough to make Bryan choose it for ordinary work without hesitation.

## The verdict

We have demonstrated parts of the system under real conditions. We have not demonstrated a routine, low-friction personal workflow. Those are different bars.

The current experience is still too likely to interrupt work that matters, blur whether a run is complete, or ask an operator to debug transport, terminal rendering, or agent setup. The product must earn the right to sit between a person and an agent. Until then, direct local Codex or Claude remains the default escape hatch.

The goal is not “make every agent invocation go through Wingthing.” The goal is: when a person explicitly chooses Wingthing to reach a machine or manage a durable run, it makes that job more dependable and inspectable than doing it ad hoc.

## What the evidence actually says

| State | Evidence | Meaning |
| --- | --- | --- |
| Field-proven | Direct agent-manager control has driven a real inner Sonnet run from a real outer Claude session. Direct Mac and Bryan physical-wing checks passed. The organization browser canary passed 17/17. The WSL security canary exercised six installed agent CLIs and the proxy-bypass check. | The architecture, direct control plane, shared-host identity boundary, and network-sandbox direction are viable. |
| Integration-proven | Earlier deployment work found and fixed Claude MCP `_meta` rejection, headless binary/auth resolution, a misleading sandbox diagnostic, Linux architecture selection, single-frame text+Enter submission, kill reporting, and shared-host Claude configuration persistence. | These are valuable regressions caught in test and deployment, not proof that an everyday user flow is calm. They must stay covered. |
| Branch-only | `04ccc8d fix: remove implicit direct task timeout` changes an unspoken 120-second deadline on direct `wt run` to no deadline unless configuration or a task specifies one. The full Go test suite passed when the change was made. | A three-day long-run failure is addressed in `fix/unlimited-direct-runs`, but it is not proof until the intended long run completes through a deployed/current binary. |
| Not dogfood-ready | A persistent PTY has no trustworthy semantic completion signal; terminal idle means “terminal emitted no bytes,” not “the agent is done.” `terminal_read` and `wt session read --json` can return an enormous ANSI/base64 snapshot. There is no clean detached Codex/PTy task-result contract. | An outer agent must not scrape a terminal to decide what happened. This is the central usability gap. |
| Failed while writing this plan | A bounded local `agent_run` through the current branch created run `t-20260901-231129-b8d52d4752054832`, entered the macOS sandbox, then lost its supervising local MCP process before a typed result was returned. The task remained `running`, no memo was written, and the caller had to inspect SQLite to understand it. | Durable agent work cannot depend on the lifetime of an MCP stdio client. Orphan detection, durable supervision, and a typed terminal result are release-blocking contracts. |

The last row is the important correction: using a real run to write this document did not work. The result is product evidence, not a reason to reinterpret a terminal log as success.

## The failure inventory

These are observed problems, not hypothetical polish items.

1. Ambient or mandatory delegation got in the way of unrelated work. An instruction that every meaningful task must use Wingthing turned research and maintenance work into a Wingthing troubleshooting exercise. That policy was removed. The product may be chosen; it must never be a hidden prerequisite.
2. Direct `wt run` silently inherited a 120-second deadline even when the task stored `timeout_seconds=0` and exposed no way to opt out. This was the source of multi-day confusion in an OpenCode workflow. Commit `04ccc8d` removes the implicit deadline; explicit task and skill deadlines still win.
3. `wt session wait --idle` cannot be a Codex completion mechanism. A TUI repaint or spinner continually refreshes activity, so it may never become idle after producing a useful answer.
4. Terminal snapshot reads can put raw ANSI state and base64 payloads into an outer agent’s context. That is expensive, hard to inspect, and can swamp the very agent meant to supervise the work.
5. A persistent Codex session has no typed outcome, health, error, or artifact contract. It may be useful for human takeover, but its terminal should not be mistaken for a semantic run API.
6. Codex startup has required situational fixes for terminal capability, trusted-workspace prompts, authentication/config persistence, MCP startup visibility, and host toolchain availability. The environment needs a preflight that names which prerequisite failed.
7. A nested Wingthing attempt failed during SQLite WAL initialization. Recursive orchestration is currently neither a supported product capability nor a typed refusal.
8. A WSL2 failure message blamed a “security profile: kernel,” sending an operator toward AppArmor even though the failed operation and WSL environment were the relevant facts. Diagnostics must identify the operation and recognize WSL2.

## The operating rule from now on

Wingthing is opt-in at the point of execution. A parent agent or person chooses it only when they want a named workspace, a different machine, a sandbox, a durable run, terminal takeover, or auditable coordination.

It must never be injected into global agent instructions as the default way to perform ordinary work. If the Wingthing path is unavailable, unclear, or taking longer to diagnose than the bounded job is worth, the caller explicitly falls back to direct local work and records the friction as a dogfood issue. No time-sensitive unrelated task waits for a Wingthing repair.

Every supported use must have all of the following before we call it routine:

- an explicit wing and workspace;
- an explicit agent and model;
- a documented timeout policy;
- a stable run ID;
- typed `pending`, `running`, `done`, and `failed` state;
- bounded result/error/artifact retrieval; and
- a human terminal attach/takeover path that is supplemental, not the source of truth.

## The personal workflow we are trying to make boring

An outer Codex or Claude session on a laptop should be able to ask for a bounded review or implementation job like this:

1. Choose `local-mac`, `wsl-rig`, `office`, or another named wing, and choose an allowed repository/worktree.
2. Run preflight. It reports agent binary/version, selected model support, authentication/config availability, workspace trust, TTY needs, sandbox mode, network policy, MCP health, and required toolchain. A failure says which check failed and how to proceed.
3. Prepare the workspace only through an explicit operation: verify or create the requested directory/worktree and return its resolved path. No surprise directory creation or implicit current-directory use.
4. Start a semantic Codex or Claude job with its prompt, model, isolation, and timeout recorded. The call immediately returns a stable run ID.
5. Read a small typed status object and, when terminal, a bounded semantic result, typed error, token/use information, and artifact references. Completion never depends on reading or waiting for terminal repaint traffic.
6. When useful, attach to the persistent terminal to inspect, steer, or take over. Terminal output is a human diagnostic surface. It is not completion signaling and it is not shoved unbounded into the parent agent’s context.
7. Stop, retry, or hand off by run ID. If the caller disconnects, supervision and status remain meaningful; otherwise the system reports a typed “client-owned run ended” state rather than a stale `running` row.

This is the local-first product. Peer roosts, browser access, federation, hosted relay economics, and broader team workflows build on it; they do not substitute for it.

## Smallest recovery sequence

### P0: make one selected run dependable

1. **Merge and prove the long-run policy.** Land `04ccc8d`. Preserve explicit configured/task deadlines, but make an absent direct-run timeout intentionally unlimited. Add or retain tests that distinguish absent, skill, and task timeout values. Run a real job longer than two minutes on the installed artifact.
2. **Bound terminal inspection without breaking readers.** Add a new `terminal_tail` operation and a `wt session tail` command. Keep `terminal_read` and `wt session read --json` byte-for-byte compatible for existing callers. The new operation has a modest default, a capped maximum, `total_byte_length`, and `truncated`; it is clearly described as terminal state, not an agent result. Server-side cursor/tail transport can follow after the compatible contract exists.
3. **Make semantic runs survive and explain caller loss.** The `agent_run` lifecycle needs durable ownership/supervision or a deliberate client-owned lifecycle with immediate typed terminal failure. A stdio disconnect must never leave a forever-`running` task. `agent_status`, `agent_wait`, and `agent_result` must agree on terminal state and expose the final error without database inspection.
4. **Publish typed worker health and completion.** A Codex or Claude semantic run reports startup/preflight status, completion, error, result, and artifact references. A persistent PTY remains separate. Do not infer any of these from `IdleSeconds`.
5. **Add preflight and honest diagnostics.** Check binary, auth/config source, workspace/trust, TTY, MCP startup, toolchain, sandbox capability, and WSL detection before the expensive run. An error names the failed operation and environment rather than a speculative security subsystem.
6. **Define the recursion boundary.** Either support nested Wingthing with isolated state deliberately, or reject it before SQLite with a typed unsupported-recursion result. Do not let it fail as a WAL surprise.
7. **Add explicit workspace preparation.** A prepared-workspace result must make worktree/directory policy visible and auditable. It is not permission to create arbitrary directories or mutate a repository behind a caller’s back.

### P1: prove the selected-machine workflow

After P0 works locally, repeat it through a direct connection to the WSL machine and a shared organization host. This is where login/config persistence, user-home isolation, allowed paths, agent availability, and terminal takeover become acceptance criteria rather than incidental deployment facts.

### Deliberately deferred

- Making wingthing.ai or the browser the default control path.
- Broad peer-roost federation, hosted relay monetization, and public “patterns” positioning.
- Swarms, ambient delegation, or recursive orchestration.
- Replacing direct agent CLIs for work that does not need another machine, isolation, or durable coordination.

None of these are an acceptable substitute for a calm local parent-to-child run.

## Test and dogfood ladder

Each step below is a gate. A later pass never waives an earlier failure.

| Gate | Required proof | Failure condition |
| --- | --- | --- |
| Unit contracts | Timeout resolution: absent direct timeout has no deadline; skill/task override it. Terminal-tail truncation, byte limits, and compatibility behavior are pinned. Orphan/lifecycle transitions and recursion errors are pinned. | Any implicit deadline, unlimited raw payload, stale run state, or changed legacy `terminal_read` behavior. |
| MCP/control integration | `agent_run` returns a run ID; status/wait/result agree; disconnect/reconnect is deterministic; preflight failures are typed; direct and HTTP/organization control surfaces expose the same contract. | A caller needs a terminal scrape, SQLite, or an unstructured log to answer “what happened?” |
| Local real run | A parent launches Codex Sol in this repository for a bounded review, waits on a typed result, and gets one artifact/result. It runs past two minutes when appropriate. | The local agent re-onboards unexpectedly, stalls on trust/auth, loses its result after client exit, or floods the parent with raw terminal bytes. |
| Cross-machine direct run | Repeat from Mac to the WSL rig using an allowed repository and the remote machine’s existing agent login/config. Verify that a person can attach without exposing an inbound port. | Login state is confused, workspace is wrong, security is explained vaguely, or results differ from local semantics. |
| Organization-host canary | Repeat the same semantic workflow with real org identity/home/path constraints, then terminal takeover. Preserve the existing browser and shared-host canaries. | Another user’s home/config leaks, a run loses identity, or upstream HTTPS/organization behavior regresses. |
| Habit gate | Bryan deliberately uses the supported workflow for three routine workdays: at least one local, one remote, and one recovery/takeover event. Each run has a run ID and a comprehensible result without product debugging. | The direct local escape hatch is chosen because Wingthing adds uncertainty or delay. |

The release bar is the habit gate, not a count of tests. Tests keep the working path working; the habit gate says whether the path deserves to exist in someone’s day.

## Retest ledger

| Change | Re-test immediately |
| --- | --- |
| Timeout policy | Direct `wt run` exceeds two minutes; configured and per-task deadlines still cancel; existing scheduled/skill behavior stays unchanged. |
| Terminal tail | Legacy full read remains compatible; new tail is bounded and clearly non-semantic across local, direct, and organization surfaces. |
| Run lifecycle | Stdio client disconnect during startup and during execution; reconnect/status/result; stop/retry; orphan cleanup; no stale `running`. |
| Preflight | Missing binary, expired/missing login, untrusted workspace, unavailable toolchain, MCP startup failure, and WSL2 each produce the right typed diagnosis. |
| Workspace preparation | Existing directory, new permitted worktree, denied path, shared-host identity/home/path boundary, and audit record. |
| Any sandbox change | Existing egress canaries, privileged-mode disclosure/refusal, macOS and Linux/WSL behavior, and upstream organization/HTTPS deployment canaries. |

## Decision point

Do not broaden distribution until the local real-run gate passes repeatedly. The first implementation slice is not a redesign of federation or the website: it is a small reliable semantic-run contract with bounded inspection, explicit state, and an unembarrassing escape hatch. Once that works, Wingthing can become an agent manager for people who occasionally want terminal control—not a source of friction that people learn to disable.
