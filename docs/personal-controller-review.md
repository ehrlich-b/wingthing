# Personal controller: architecture and review map

Wingthing already has most of the execution control plane. The missing product
contract is a persistent conversational parent over those resources, with
durable lineage, native child inspection, delivery recovery and human decisions.
Herdr already offers agent-to-agent launch, prompt, read and wait. Its terminal
supervision is the parity baseline, not a claimed Wingthing invention.

Primary competitor sources: [agent automation](https://herdr.dev/docs/agent-automation/)
and [agents](https://herdr.dev/docs/agents/). The isolated hands-on report is
`herdr-evaluation/EVALUATION.md` in the task workspace. It proves real personal
subscribed Codex prompting, completion, observers, takeover and explicitly
registered native cold resume, with terminal captures and exact receipts.

## Existing substrate versus this draft

The comparison is against reviewed snapshot `8782b76`, not the older installed
Mac binary and not an assumed deployment of mainline. Mainline is `26f2400`.

| Area | Already present in the reviewed snapshot | New local draft / remaining boundary |
| --- | --- | --- |
| Session runtime | Per-session egg process, PTY, sandbox and Unix control socket; detach and attach; native conversation continuation metadata | Durable native Claude journal, exact lifecycle/read/wait, receipt reservation, single-writer preview lease, typed unavailable states |
| MCP | Local stdio, self-hosted HTTP and explicit wing-targeted native direct tools; terminal start/read/send/wait/stop; supervised runs, messages, loops and swarms | Logical conversation operations, linked launch reservations, bound root/parent identity and configured parent MCP |
| Persistence | Egg state, native provider identity and headless task/event store | Separate logical conversation, retained executions, transactional event imports, delivery/checkpoint cursors and pending input IDs |
| Browser | Wing/session inventory, terminal attachment, chat view, continuation and session files | Search/filter/accessibility, wing-qualified actions/caches, native linked task tree and transcript/tool reader |
| Remote | SSH receiver and remote CLI path in the reviewed unpublished snapshot | Preview channel preflight, truthful EOF/disconnect, writer arbitration and real transport/host capability receipts |
| Orchestration | Headless run lifecycle plus bounded loop/DAG and owner-scoped messaging | Conversational parent substrate, explicit opt-in durable child wake and persistent dot; full configured child transport, live parent synthesis and exact human decision routing remain acceptance gates |

Evidence in the composed source:

- `internal/control/registry.go`: closed schemas and supported control surfaces.
- `cmd/wt/mcp_local.go`: shared typed handlers, owner resolution, spawn bounds and
  audit attribution; headless `agent_run` is distinct from interactive `agent_start`.
- `internal/egg/server.go`, `proto/egg.proto`: session runtime, attach/control
  protocol and preview writer arbitration.
- `internal/egg/lifecycle.go`, `internal/egg/session_prompt.go`: exact native
  journal, cursors, before-send reservation and conservative retry semantics.
- `internal/store/conversations.go`, migration `014_conversations.sql`:
  logical task/execution/lineage and durable delivery contract.
- `cmd/wt/conversations.go`, `conversation_browser.go`: owner-bound operations,
  transactional native state reconciliation and browser adapter.
- `web/src/conversation-view.js`, `chat-view.js`, `conversation-state.js`:
  task tree, native tool inspection, pending input and restored selection.
- `web/src/session-reference.js`, `session-route.js`: qualified resource keys,
  cached content and exact navigation.
- `cmd/wt/conversation_wake.go`, `internal/store/conversation_wake.go`:
  opt-in host wake delivery, retained immutable targets/request IDs and bounded
  retry only for proven unsent input. Synthetic typed-provider tests cover
  restart, approval waits and duplicate prevention; no live synthesis is claimed.
- `web/src/parent-dot.js`, `parent-dot-state.js`: persistent root selection and current native
  availability, with explicit exit, stale evidence and read-error behavior.
- `cmd/wt/remote.go`: explicit SSH target/binary selection and receiver guard.
- `internal/egg/codex_native.go`: second-provider native protocol draft. The
  active Codex lifecycle remains unsupported until the real supervisor exists.

The original Mac checkout is `~/repos/wingthing`, clean at `a763434` on
`feature/session-file-upload`. Saved September review/checkpoint receipts are
under `~/repos/claude/state/wingthing-org-review-20260929`. Development is an
independent local clone with no publication remote; source promotion remains
selective review rather than promotion of an entire historical snapshot.

## Minimum coherent slice and acceptance

The first slice is one personal wing, one supported native provider, one
persistent logical parent and two named children. Every task has a durable
wing-qualified reference. Native messages, tools, lifecycle source and pending
decisions are inspectable through the same owner-scoped browser/CLI/MCP
contract. A terminal remains available for deliberate takeover.

1. Start the separate preview and verify stable binary, sessions, credentials,
   daemon/PID, cookies and update selection are untouched.
2. Open the parent and use its actual configured MCP to start two children.
   Record readiness and prompt receipt independently from process creation.
3. Inspect one child's exact native messages/tools and terminal. Readers do not
   acknowledge parent delivery or acquire input ownership.
4. Observe one child's foreground completion and another's needs-input event.
   Wake a ready parent once from durable events; keep a busy or blocked parent
   queued. A native foreground completion is evidence for the parent to assess,
   not an unconditional delegated-task success.
5. Route the exact human decision to the exact child and expose its result.
   Never infer approval, auto-answer a permission request, or send generic chat
   text as consent.
6. Close/reopen the browser and restart the controller. Restore the parent,
   children, prior executions, pending requests and event cursors without
   duplicate launches or prompts. Preserve uncertain delivery explicitly.
7. Stop only the selected child. Separately prove exact native cold continuation,
   then repeat through a physically admitted remote route.

The complete journey has not yet passed. The Mac fixture reaches automatic
parent MCP initialization and a bound child reservation, but the nested child
cannot bind its network proxy under the parent's inherited sandbox. Existing
private SSH reaches owned Linux guests and verifies the preview package, but
temporary preview launch fails the host's unprivileged mount probe. Full model
authentication, supported interactive browser QA and live native Codex
supervision are also distinct gates. See `personal-conversations.md`,
`personal-remote-preflight.md` and `codex-native-interactive.md` for exact evidence.

## Context work is separate

Overnight cloud commits `267c343`, `bf254601` and `0077675` covered immutable
caller binding, operation-token exchange, sealed Linux provider isolation and
defensive preflight. They were not necessarily in the Mac source; their live
integration was unverified. None was imported as completion of this
conversational controller. The controller needs its own end-to-end acceptance,
even when security groundwork is relevant to a later transport design.
