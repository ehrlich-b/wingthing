# Conversation reader recovery (browser)

Scope: `conversation-recovery.js` (controller), `conversation-recovery-store.js`
(browser evidence), `conversation-state.js` (reducer and presentation),
`conversation-response.js` (receipts and task availability), `chat-view.js` and
`conversation-view.js` (visible UI). Tests: `conversation-recovery.test.js`, run by
`make test-conversation-ui` and by `make web`.

## Reader contract

- Opening or reopening a conversation **only reads** (`session.control`
  `session_read` on the exact wing and session). The browser does not prompt,
  check a receipt, checkpoint, wake, kill or attach a terminal writer. The
  earlier reader automatically re-submitted a restored pending prompt when it
  reopened. That behavior is removed.
- **Send** reserves an immutable pending input before any transport:
  `request_id`, text, user, wing, session, provider conversation and logical
  conversation. Only receipt evidence (status, check count, last reason/time)
  changes afterwards.
- **Check receipt** is explicit. It re-submits the same `request_id` and text to
  the same exact execution, and the wing replays it without resending input.
  Only `native_receipt_observed` clears it. Only `not_sent` with
  `definitely_not_sent` releases the text, which returns to the empty composer
  whether the answer came from Send or Check receipt. Every other outcome stays
  "delivery unconfirmed". A request still in flight when the reader closes
  records its outcome under its own execution without updating the screen.
- When a logical conversation moves to a new execution (for example after
  Resume in terminal), earlier unresolved input stays bound to the earlier
  execution. It is shown as "Unresolved input on an earlier execution" and
  checked only against that execution. It is never moved to, or resent on, the
  current one. Released text from an earlier execution is shown in the notice;
  it is not placed into the current composer.
- **Stop execution** asks for an inline confirmation, then sends `pty.kill
  {session_id}` to the exact wing. If the request fails or is not acknowledged,
  the card and reader stay, with the error kept. An acknowledgement is reported
  only as an acknowledgement: reads continue until native exit evidence
  arrives. Stopping does not prove task completion or cancel external effects.

## Identity and stale responses

- A `session_read` for another session, another agent, or a changed provider
  conversation is discarded. The reader halts automatic reads and asks the user
  to check the current execution. Nothing is re-targeted.
- A delayed older page, identified by request sequence or a lower
  `head_cursor`/`state_cursor`, can add unseen events but never replaces a
  fresher lifecycle.
- Each card or parent-dot activation resolves the logical task with a fresh
  `conversation_read`, then opens the exact current wing, session and provider.
  Only the newest activation may switch the reader. Tasks reported for another
  wing are rejected. Identical session or conversation IDs on different wings
  never share state.

## Truthful state

`conversationExecutionPresentation` is the single reading used by the reader,
the cards and task availability:

| Evidence | Shown as |
|---|---|
| process exited | `archived` (a failed turn stays `failed`). Exit is not task success. |
| fresh live native `completed` from `claude_hook` | `turn finished`. One foreground turn, not delegated-task success. |
| fresh live `idle` from `claude_hook` | `ready` |
| fresh `needs_input` | `needs input`, with the native reason. There is no provider approval ID, so there is no approve button and no automatic approval. |
| cached, stale (≥60 s), interrupted or not-yet-read | `unknown` / `cached · unknown`, never live |
| `unsupported` source | `no reader`. Inspect the terminal. |
| no recorded execution (`conversation_read` sends an empty lifecycle) | the launch state (`launch failed`, `launch not confirmed`) or `unknown`, never `archived` |

Missing earlier execution history (`history_unavailable`) is a separate
"History issue" on the card. It no longer turns the current state into
`unavailable`. A current `lifecycle_error` still does.

Native lifecycle records (`input_requested`, needs-input `notification`,
`turn_completed`, `turn_failed`, `provider_warning`, `provider_session_end`)
appear inline as evidence markers. Native items that are not Claude and have no
chat shape appear as inspectable "provider record" details.

## Browser storage (all `wt_`-prefixed, so they are cleared on account switch)

| Key | Qualified by | Content and bound |
|---|---|---|
| `wt_conversation_execution_v1:` | user, wing, session | Last 200 transcript records (16 KiB per field, 256 KiB per record), last lifecycle, cursor, pending input |
| `wt_conversation_execution_index_v1:` | user | Up to 32 executions. Oldest are evicted first; an execution holding unresolved input is never evicted. |
| `wt_conversation_selection_v1:` | user | Last opened logical conversation (wing, conversation, root). Shown as "Last opened · Reopen". |
| `wt_conversation_tree_v1:` | user, wing | Up to 256 linked tasks without events, and up to 20 root deliveries per root |

If the browser quota is full, older executions without unresolved input are
evicted first. As a last resort an execution's identity and pending input are
saved without its transcript, and the reader says so. A failed save is shown
as a storage error. It does not block an explicit Send.

A record whose stored identity differs from its key is ignored. There is no
bare-ID fallback. A pending prompt from the previous release
(`sessionStorage wt_conversation_prompt:[user,wing,session]`) is migrated once
as "unconfirmed" with "provider identity not recorded". It is never submitted.
Restored evidence is labeled cached until a fresh read replaces it. A cached
archived state can be shown, but Resume is offered only after a fresh read.

## Linked cards, deliveries and recovery

- The reader panel shows the root and its children, with state badge, role,
  wing, current execution, provider conversation, process, launch and native
  reason (under Inspect). Missing history and current-state errors are separate
  disclosures. The tree refreshes every 5 s with `conversation_read` from the
  root's `delivered_cursor`; reading never acknowledges. Each subtree carries
  the time its read was issued. A delayed panel or inventory response never
  replaces a subtree from a later-issued read.
- Child state deliveries after the parent checkpoint are listed as
  "parent receipt unknown". This reader does not checkpoint, wake or prompt the
  parent.
- If a newer execution appears for the open logical conversation, the reader
  says so and stays on its execution until **Check current execution**.
- If the current execution cannot be read, the last cached execution opens and
  is labeled "Showing the last execution this browser knew about". It is
  read-only and labeled cached. If the failed check was for the conversation
  already open on that same execution, the open reader stays as it is and
  only the failure is reported.
- **Resume in terminal** uses the existing explicit `PTYStart{resume_session_id}`
  path (`connectPTY`), which attaches an interactive writer. It is offered only
  when all of these hold:
  - a fresh read shows the exact current execution archived;
  - no newer execution exists;
  - a fresh `sessions.history` entry matches the session, logical conversation,
    owner (`user_id`) and provider (`agent`, which must be reported; there is
    no default provider), and is `resumable`;
  - the wing has `session.provider_resume.v1`.

  It is re-verified on click. It is not a reader-only or logical resume.

## Backend contract requested (not consumed yet)

There is no typed logical resume. When the backend owner announces one, the UI
expects:

```
conversation_resume { conversation_id, request_id }
  -> { conversation_id, root_conversation_id, parent_conversation_id,
       previous_session, session, launch_state: started|starting|failed,
       launch_error?, reused }
```

The backend should do the owner, path and provider-archive checks. Requests
with the same `request_id` should be idempotent, and launches should go through
the browser `session.control` allowlist. Pending input stays on
`previous_session`. Until this exists, only Resume in terminal is offered.

## Remaining gates

- No supported browser runtime was available. Visual, layout and accessibility
  QA of the panel, cards and pending cards has **not** been performed and must
  be done in a supported runtime.
- The full authenticated parent → two children provider journey remains as
  described in `docs/agent-manager-product-brief.md`.
