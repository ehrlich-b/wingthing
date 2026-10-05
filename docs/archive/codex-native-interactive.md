# Native interactive Codex: protocol draft and remaining integration

This change is a reusable parser and command contract, not live Codex lifecycle
support. The active `session_status/read/wait` adapter continues to report Codex
as unsupported with `ready:false`. No provider process, login, model call, global
hook/config change, or sandbox permission change occurs in this draft.

## Verified sources and feasibility

Installed macOS Codex CLI `0.159.3` exposes `--remote unix://PATH` on both the
interactive entry point and `resume`. Its app-server help accepts that endpoint
with `--listen`. Generated native JSON Schemas pin the thread/status, turn/item,
and approval field shapes used in the fixtures. Source receipts live in the
personal evaluation workspace's `herdr-evaluation/codex-native-schema/`.

Official [app-server documentation](https://learn.chatgpt.com/docs/app-server)
describes a remote interactive TUI, WebSockets over Unix sockets, exact thread
creation/resume responses, turn completion, authoritative completed items, and
server approval requests. [Hook documentation](https://learn.chatgpt.com/docs/hooks)
provides exact session and transcript identity but requires trust for non-managed
hook definitions. This draft neither installs nor bypasses that trust.

The previously created personal Herdr session was read by its exact known native
ID, `01a101ec-9ea9-7bc1-861e-d5fb2243c32d`. Its local history had explicit session
metadata, task-start/task-complete events and message rows. This is schema
evidence from one owned session, not permission to search other histories, a
stable rollout-format guarantee, or a new-file discovery strategy.

## Implemented building blocks

`CodexNativeThreadResult` extracts the exact thread from the response to a known
pending `thread/start` or `thread/resume` request. The integration must associate
the response with its RPC request ID; resume checks the expected thread ID.
Inventory responses cannot supply the identity.

`CodexNativeCommandPlan` returns separate app-server and interactive TUI argv.
It requires absolute paths, a socket below the supplied provider home and an
exact native thread ID. It starts neither command. The server arguments force
ChatGPT authentication and disable plugins; the TUI resumes the known thread
through that private endpoint. Authentication availability is still a setup gate.
This is an interactive remote TUI plan, not `codex exec` presented as a terminal.

`ParseCodexNativeEvent` accepts bounded v2 native notifications/requests for only
the bound thread. It preserves turn, item and RPC request identities, full tool
evidence, and user-input receipts. Assistant-item completion has no lifecycle
completion state; only the terminal native turn event supplies that state.
Approval, permission and blocking user-input requests become needs-input evidence.
Request resolution means answered or cleared; it does not infer consent. The
parser generates no replies, timeout defaults, grants or approval decisions.

Engine-idle events do not emit `session_ready`. A future integration must verify
auth, loaded-thread ownership and interactive access before publishing readiness.
Nonblocking user-input requests do not pause lifecycle state.

`RecordCodexNativeEvent` persists these records through Wingthing's existing
durable journal. Stable completed-item/turn keys deduplicate authoritative replay.
Native stream deltas are deliberately ignored because they lack durable replay
positions. A future transcript reconciliation must use stable native item/turn
IDs, rather than claiming a socket stream itself supplies persistence.

## Required coherent runtime slice

1. A supervisor inside the existing egg sandbox must own both app-server and
   interactive TUI. Launching app-server outside it would move provider work
   outside the existing boundary. Validate socket ownership/symlinks/length,
   lifecycle cleanup, sibling-process termination and approval routing there.
   Do not add an outside-sandbox convenience fallback or expand access.
2. Supply the clean preview provider home explicitly to both child processes.
   Reuse only an already authorized personal login in that selected home. Missing
   login is actionable setup evidence; neither ambient API billing nor credential
   copying is a fallback. No new personal login is proved by this draft.
3. Perform native initialize/initialized, exact thread start/resume, persist the
   response binding under the Wingthing execution, then attach the real TUI to
   the same server/thread. RPC response IDs are connection-scoped; durable task,
   execution and provider IDs are distinct.
4. Submit protocol input with caller request identity and reconcile the native
   returned turn ID on uncertain transport outcomes. A matching message in
   history alone does not prove causal acknowledgement of that caller's request.
5. Own server requests without auto-approval. Native flags/requests can show
   needs-input, but interactive controls must also account for login, trust and
   hooks-review setup. Engine idle alone cannot certify the TUI composer.
6. Reconnect using native history for the exact thread, stable item/turn IDs and
   Wingthing delivery cursors. Drain the previous execution before replacement;
   retain inspectable history and do not duplicate parent launches or prompts.
7. Test an actual personal subscribed interactive session in that sandbox:
   two children, prompt/completion, needs-input, exclusive writer plus observers,
   detach, reconnect and cold native resume. Label parser fixtures separately.

## Validation boundary

Unit tests pin exact native identity/argv, assistant-vs-turn completion, user
receipts, inspectable tool evidence, approval correlation, blocked flags, invalid
frames and ignored deltas. The integration fixture reopens the durable journal,
replays final records, paginates and reconnects without duplicate completion. It
also proves this draft keeps the active Codex adapter unsupported and unready.
Neither tier proves provider startup or sandbox enforcement.

Run both focused fixture tiers with `make gate GATE=integration`; use `make check`
for the normal repository verification.
