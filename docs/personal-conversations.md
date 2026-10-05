# Personal conversations: bounded first slice

A logical conversation is a durable personal task on one wing. Its latest egg
session is an execution of that task, with an exact provider conversation ID.
Earlier executions remain associated with the same logical task after authorized
browser resume. Parent and root IDs are persisted independently of labels.

This slice supports Claude native transcripts and per-launch native lifecycle
hooks. Other providers remain inspectable terminals and report unsupported
conversation input. A native Stop is a foreground completion observation; it is
not a guarantee that every provider background task or hook has ended.

## Entry and inspection

On an online personal wing, the Conversations inventory creates a root parent
and shows its linked children. Every child is a real egg session. Selecting a task
opens the same native transcript and tool detail reader used for its parent;
Open terminal attaches to its exact execution wing. The conversation URL carries
both logical conversation and wing IDs. Offline cached tasks remain labeled
unavailable. Organization and shared wing parent creation is excluded.

The typed `session.control` browser adapter shares the existing CLI/MCP handlers
for session status, transcript, wait, prompt receipt and conversation operations.
A browser owner uses its existing session principal. Bound local MCP propagates
that browser owner to children only after matching the persisted owner and
principal. A bound connection can read its own root tree and launches children
under the selected bound task, including grandchildren when explicitly bound to
a child. It cannot switch to another root tree.

## Parent MCP connection

A root launch generates a private workspace MCP configuration for the current
executable, current Wingthing directory, existing principal and logical task.
The root receives it through Claude's `--mcp-config` argument. It does not edit
clients.yaml or host provider settings. Existing caller MCP-config flags are
rejected with an explicit conflict. Authorized root resume reuses identical
configuration and refuses changed contents.

When the Wingthing state directory is writable under the selected workspace's
existing egg policy, the parent runs `wt mcp stdio` directly inside its sandbox,
unchanged. No mounts, socket exceptions, network permissions or client grants
are added. `wt conversation bootstrap` also prints a reproducible configuration
for an already authorized client; printing it is not proof that a provider can
reach every operation from its sandbox.

On macOS, that direct server cannot start children: the child runner inherits
the parent's network sandbox and its proxy bind fails with `proxy listen: listen
tcp4 127.0.0.1:0: bind: operation not permitted`. The
`--expect-nested-proxy-block` fixture mode records that failure.

### Host mailbox (macOS personal wings)

Where the direct server was previously refused because the provider cannot
write the state directory, a root launch instead registers a host mailbox. This
is the only activation path; layouts that already worked keep the direct server.

- The already-authorized launcher (browser owner or local MCP client) captures
  its principal, actor, surface, conversation, root and exact parent execution,
  a fixed tool subset it already holds, finite session and spawn-rate bounds,
  path bounds and the parent's resolved egg policy. Without a clients.yaml
  entry the ceiling is the bridge subset and the direct-MCP default bounds,
  never an unrestricted server.
- Registration lives in `<state>/conversation-brokers/<execution>/`. Before
  registering, on broker start and before every child spawn, a model of the
  existing Seatbelt write rules must show that the provider cannot write the
  database, clients.yaml, wing/egg configuration, eggs, the registration, its
  journal or the broker executable, and that no ancestor of the sandbox HOME is
  renamable. State under `/tmp`, TMPDIR, the workspace or an agent profile
  path is refused rather than protected with a new rule. Organization and
  shared wings are excluded.
- A broker (`wt conversation broker`, started by the launcher outside the
  parent sandbox, the same built binary) serves only while the registered
  parent execution is alive, with the registered owner, provider and workspace.
- The parent's injected `wt mcp stdio --host-mailbox` opens no Wingthing state.
  It exchanges bounded, no-follow, atomically published JSON files in
  `.wingthing-conversations/<task>/<execution>/mailbox` in the workspace.
  Envelopes carry only an MCP method and params; unknown fields such as a
  principal or grants are rejected.
- Each call is dispatched through the existing typed tool handlers, strict
  argument decoding, audit (`actor: conversation:<task>`) and shared admission,
  re-intersected with current clients.yaml and wing lock/org state. Only
  `wingthing_capabilities`, `agent_start`, `session_status`, `session_read`,
  `session_wait`, `session_prompt`, `conversation_list`, `conversation_read`
  and `conversation_checkpoint` exist; raw terminal input, commands, prompt
  runs, wake policy and stop are unavailable. Every session target must be an
  exact execution linked to the captured root, including archived and resumed
  executions.
- Children always link to the captured task, use the captured parent policy
  (workspace egg.yaml files are ignored), must run inside the parent workspace
  and count against a durable per-owner hourly launch bound.
- The broker journals each accepted request durably before dispatch. A replayed
  request ID returns the journaled response. After a broker crash, reads and
  tools with durable tool-level IDs (launch and prompt) are reconciled; a
  checkpoint reports an unconfirmed outcome and is never replayed. The client
  never republishes a request whose response was lost.

`wt conversation transport <execution> --json` reports the registration,
readiness and trust statement. The mailbox is **same-owner workspace trust**:
any process able to write the workspace, including the parent provider, can use
the captured parent's already-authorized tree scope. It does not authenticate a
caller process and is not Context's sealed caller binding.

## Reconnect and input

Launch intent is reserved before spawning. Reusing the same request ID and
arguments returns the same task and execution; changed arguments fail. A
reservation left starting after a crash stays explicitly unresolved and will not
automatically launch again. Inventory is capped at 256 logical tasks per owner
and 128 executions per task before reservation, so bounded reads cannot silently
hide newer tasks.

Native journal events are imported transactionally with a per-execution cursor.
All executions are drained, preserving intermediate needs-input and completion
states even if another turn or resume occurred before the parent read. Root
state delivery is paginated and replayable. Checkpoint uses a revision compare
and swap, with a monotonic delivered cursor and a bounded intent string. Reading
does not acknowledge delivery. The separately implemented, explicit opt-in
host wake controller uses a retained outbox and exact prompt reservations to
notify a ready parent of eligible native child events. A working or blocked
parent keeps events queued; unknown delivery retains the same request and
target across restart. See `personal-parent-wake.md` for the tested contract.

Prompt input reserves an identity before transport. Retry inspects the same
request without resending. The UI clears pending input only when exact native
provider user text is observed after the reserved cursor. This is a native text
receipt with unverified causality, not provider request acknowledgement. Pending
request identity survives page reload in sessionStorage, qualified by user,
execution wing and session. Historical scrolling and expanded tool details stay
stable while new messages arrive.

Native journals, prompt reservations and bounded startup failure diagnostics
retain owner, principal and provider metadata after cleanup. Startup diagnostic
tails are private and bounded; they do not dump environment or credential files.

## Preview provider data home binding

By default preview derives the provider data home from its state:
`<state>/provider-home`, with Claude using `<data home>/.claude` as
`CLAUDE_CONFIG_DIR`. On macOS that canonical directory is also Claude's
secure-storage identity, so moving control state would otherwise select a
different credential namespace. An optional host-created file,
`<state>/.provider-home`, keeps an existing data home while the control state
moves. It is not a grant, credential, or auth transport, and Wingthing never
creates, edits, or removes it. Stable ignores it.

When the file is absent, behavior is exactly the default above. When present it
must be a regular, single-link file owned by this account, not readable or
writable by group or others, and at most 4096 bytes. It is opened without
following links or blocking on special files. Its content is one absolute,
clean path with an optional trailing newline. That path must already be its own
canonical real directory (no symlink component). It must be owned by this
account and not writable by group or others. It must not overlap the selected
state, the OS account's stable `~/.wingthing`, or its `~/.claude` or
`~/.claude.json`.

A present binding that fails any check stops `wt-preview` before any vendor
invocation; it never falls back to `<state>/provider-home`. Config loading,
native and direct spawns, lifecycle/history, resume, provider
status/setup-guide/login (re-checked right before login) and parent re-entry all
use the same resolved home. Spawned providers keep the existing contract. On
macOS, `HOME` is the OS account home, `CLAUDE_CONFIG_DIR` is the canonical
`<data home>/.claude`, and `USER` is omitted. Status and login run from the data
home. No host model settings are imported.

Example for host review. Only the human creates the file; replace `STATE` with
the selected, already-marked preview state:

```sh
DATA_HOME=/private/tmp/wtd5/provider-home
STATE=/absolute/new/preview-state
test "$(cd "$DATA_HOME" && pwd -P)" = "$DATA_HOME"   # canonical, no alias
stat -f '%Su %Sp' "$DATA_HOME"                       # this account; no g/o write
test "$(cat "$STATE/.release-channel")" = preview
test ! -e "$STATE/.provider-home"
(umask 077; set -C; printf '%s\n' "$DATA_HOME" > "$STATE/.provider-home")
chmod 0400 "$STATE/.provider-home"
env -i HOME="$HOME" PATH="$PATH" WINGTHING_DIR="$STATE" WINGTHING_PREVIEW_DIR="$STATE" \
  wt-preview provider setup-guide claude --json
```

The setup guide is output-only and runs no vendor command. Expect `data_home`
`/private/tmp/wtd5/provider-home`, `config_directory`
`/private/tmp/wtd5/provider-home/.claude` and `data_home_binding`
`$STATE/.provider-home`. Do not run the original default-derived state against
the same data home concurrently.

## Evidence and remaining acceptance

`make check` runs the existing pinned web build, Node tests, Go tests and binary
build. Store tests cover concurrent duplicate reservation, owner/tree binding,
cursor replay, intermediate transitions, old execution drain, checkpoint races
and inventory limits. Native protocol fixtures use a disposable provider home
and no model authentication.

`make test-conversation-transport` runs the focused unit tier: envelopes, the
provider write model, policy intersection, journal replay, tree-bound targets,
activation and the refusal to start a broker from a Go test binary (unit tests
replace the launcher with a fake; nothing spawns a process).

```sh
make test-preview-reentry   # direct server: records the nested proxy blocker
make test-conversation-e2e  # host mailbox: parent plus two real child eggs
```

`test-conversation-e2e` builds `wt-preview` and runs the fixture with state in
a fresh `wtc-state-*` directory under `CONVERSATION_FIXTURE_ROOT` (default: the
checkout's parent), which must be inside the OS-account HOME and outside `/tmp`
and the workspace. The parent provider, through its injected client, lists the
bridge tools, is refused raw input and an out-of-tree target, launches two
children with same-ID replay, waits for native readiness, submits each prompt
with a same-ID replay, reads results, reconnects with a new client process and
replays both IDs, then checkpoints. The proof asserts exactly one native
delivery per child, owner/root linkage in browser inventory, the transport
registration and broker shutdown with the parent. Neither mode invokes an
Anthropic model; a fake protocol pass is not authenticated-model evidence.

The completed product acceptance still requires a deliberately personal
authenticated Claude home, a passing `test-conversation-e2e` run of the host
mailbox (implemented and unit tested; its compiled fixture has not yet passed),
two real child results, inspected tool details, restart
with the same provider conversation, and attention/completion delivery resumed
from a checkpoint without duplicate launches or sends. This slice supplies
linked native tasks, durable cursors and inspectable failure evidence. It does
include an opt-in durable wake controller and a persistent browser parent entry,
covered by store, synthetic typed-provider and pure frontend fixtures. It does
not claim the full configured parent-to-two-child journey, authenticated parent
continuation/synthesis, exact human approval handoff, cross-host child launch,
interactive browser acceptance or a live subscribed model run.
