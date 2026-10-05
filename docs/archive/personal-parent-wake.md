# Durable personal parent wake delivery

Wake delivery is an explicit opt-in on an existing personal root conversation.
It runs in the host wing daemon, using that parent's retained owner/principal,
exact native provider identity, existing prompt transport and single writer
lease. No provider, listener, credential, client grant, sandbox exception or
permission response is created by this controller. Organization and shared
wings cannot opt in; a locked wing pauses its controller.

```sh
wt conversation wake ROOT enable --client EXISTING_OWNER
wt conversation wake ROOT status --client EXISTING_OWNER
wt conversation wake ROOT retry --client EXISTING_OWNER
wt conversation wake ROOT disable --client EXISTING_OWNER
```

The corresponding typed MCP/browser operation is `conversation_wake` with
`conversation_id` and optional boolean `enabled`. Omit `enabled` to inspect the
policy and pending delivery. Enabling also drains eligible retained native
events that have not yet been scanned by this root's wake controller. Disable
pauses delivery and retains its outbox; it does not cancel an in-flight send.

Native Claude child `needs_input`, `completed`, `stopped` and `failed`
observations are eligible, along with exact `egg_process` runtime
`session_exit`/`session_failed` records in `stopped`/`failed` state. A plain PTY
row, unsupported source, or successful process exit does not become native
foreground completion. Reconciliation imports all linked execution tails,
including executions resumed before the controller observed their final state.
The message contains bounded conversation, execution, provider, wing and event
identities, a native state/source, a host-written reason and an inspection link.
It excludes child prose, titles, tool output and approval question text. Startup
failure before a provider identity is available explicitly marks that child
identity unknown rather than inventing one.
`completed` means foreground completion was observed; it does not declare the
child task done. `needs_input` requests inspection and leaves permissions to a
human.

The parent receives input only with living exact-provider native foreground
idle/completion readiness. While it is working, stopped, or awaiting input or
approval, the event stays queued. This controller does not answer permissions
or turn an approval wait into readiness. One ordered event is outstanding per
root. The host loop has concurrency one, processes four roots per page (the
store bounds pages to fifteen), scans at most fifty tree events per root step,
and uses a cancellable three-second step and a fixed 500ms prompt receipt wait.
Existing conversation/execution inventory bounds and lifecycle import bounds
also apply. This is polling, not an immediate event subscription.
The private host helper retains owner checks and current wing path bounds; it
exposes no general MCP server or spawn/admission surface. `paused` in the status
response indicates a locked wing, including when opt-in is configured while
locked.

Before any prompt call, the SQLite outbox saves an immutable request ID,
execution, provider identity and message. The native prompt reservation adds its
existing persist-before-send protection. Restart retries the same request ID on
the same execution and provider to reconcile native history. It never redirects
an uncertain send to a resumed parent, and missing retained evidence or a
changed provider blocks reconciliation instead of resending.

Only the prompt transport's explicit `not_sent` plus `definitely_not_sent=true`
allows a new attempt ID. A zero byte count or generic error does not. Known
no-input failures wait at least five seconds and initially permit three attempts
for that event; exhaustion exposes `blocked` in the pending status. The explicit
user action `retry_not_sent:true` (CLI `retry`) authorizes an additional three
attempt cycle only while the retained result is `not_sent` or `blocked`. It
preserves previous request evidence and the monotonically increasing attempt
number, so a new ID cannot collide with an old reservation. Each additional
cycle requires explicit authorization, with a total limit of twelve attempts
per event. Pending status exposes `attempt_limit`, and the response exposes
`total_attempt_cap`. A five-second cooldown still applies after authorization.
Queued, pending or uncertain deliveries cannot use this action; combining retry
and enable/disable in one call is rejected. Unknown
delivery remains `unconfirmed` and holds subsequent events. Inspect the original
session and request evidence; disabling wake retains that evidence. Automatic
abandonment, manual outbox resets and rerouting unknown requests are unsupported.

An exact native user text receipt advances only `wake_delivery_cursor`.
`conversation.delivered_cursor`, `checkpoint` and `revision` remain unchanged
until the parent explicitly calls `conversation_checkpoint`. Receipt causality
remains unverified without a native provider request acknowledgement. Observed
outbox rows retain the latest 128 deliveries; persistent scan/delivery cursors
prevent their deletion from generating duplicate wakes.

## Evidence and limits

Store tests reopen a real SQLite database after an unknown delivery, preserve
the request/target, deliver an attention event before its subsequent completion,
keep checkpoint acknowledgement unchanged, and enforce cooldown/attempt bounds.
Controller tests use the actual typed `SubmitSessionPrompt` reservation and
receipt parser with a synthetic sender: a parent permission wait queues input,
one send becomes unknown, then a restarted controller sees native user text
while the parent is working without sending again. Additional tests cover
unknown delivery across parent resume and provider replacement, owner/personal
scope and retained child execution tails. An additional typed prompt fixture rejects three sends with explicit no-input
proof, releases its synthetic competing writer, requires a deliberate user
retry, and observes one fresh-ID native receipt without changing checkpoint ACK.
Store tests exhaust all twelve unique IDs and reject unknown delivery recovery.
These tests do not invoke a model.

The host wake loop is wired into the wing daemon, but no authenticated live
provider acceptance is claimed. The separate parent MCP-to-child spawn path on
macOS remains blocked by its nested network-proxy listener unless an existing
authorized host transport becomes available. Wake delivery neither depends on
nor repairs that bridge. A complete acceptance journey still requires a
deliberately personal provider, two real children, foreground completion wake,
human approval handoff, restart and native receipt recovery under the unchanged
runtime policy.
