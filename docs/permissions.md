# Answer one Claude Bash request

On a personal local Linux wing, `wt` can review and answer a live Claude Bash
PermissionRequest. The selected request shows its complete command, machine,
OS user and directory with `permission.read` alone. With `permission.respond`,
`a` opens a scrollable review; after reaching its last line, Enter sends **Allow once**. `d` sends deny. `A` always opens agent setup;
`D` always opens diagnostics. Requests without a live supported response handle
say **Answer in terminal**. The browser uses that terminal fallback too.
The session shows its pending count; `[` and `]` step through parallel requests.
Refreshing the list preserves the selected request ID, and an open review
remains bound to its own request even if another request is answered or removed.
After an answer, selection stays on that request through refreshes; repeated
`d` cannot decline another request. Use `[` or `]`, or move to another session,
to select another request. The dashboard queries only blocked Claude sessions
on supported platforms.

Enable the local TUI explicitly in the personal wing's `clients.yaml`:

```yaml
clients:
  tui:
    owner: default
    grants: [permission.read, permission.respond]
```

The socket still checks the wing owner, client, logical session owner, paths,
lock and passkey requirements. Neither reading nor terminal input grants the
right to answer a permission. This first slice exposes no permission MCP tools
and refuses tunnel permission operations. The browser shows Answer in terminal;
do not grant it permission.respond. Clients that already exist must
retain their other grants when adding these two.

The same contract is available to human scripting with `wt permission list
--wing-id W --session-id S --json` and `wt permission respond --wing-id W
--session-id S --request-id R --digest D --attempt-id A --decision allow|deny
--json`. IDs and the digest come from the displayed request; an attempt ID must
be new for each decision. Never automatically retry a delivery.

Each supported interactive Claude egg adds invocation-only `permissions.ask: ["Bash"]`
and a synchronous exec-form hook alongside existing hooks. The native hook
passes its provider stdin descriptor over private IPC. Linux checks the hook
executable, original provider ancestry and the channel creator, then reads the
request JSON from that channel. The hook checks the broker process identity;
its socket directory is read-only inside the sandbox. No broker token
enters Claude, settings, argv or tool environments. Parallel invocations retain
separate, immutable request IDs. Where this channel cannot be authenticated
(including macOS in this slice), neither the hook nor the Bash ask rule is added;
existing settings and native permission handling are preserved.
The 90-second application deadline precedes the 95-second provider hook timeout. Expiry,
cancellation or unsupported input returns no decision, retaining native
permission handling. `disableAllHooks` stays effective; deny rules and the
sandbox continue to apply. There are no saved rules or blanket approvals.

The wing's private `permissions/` ledger synchronizes competing surfaces,
checks request liveness, and fsyncs an audit intent before preparing the request
and attempt bindings. Preparation starts as **failed**; only persisted bindings
get a reservation audit line. An audit failure blocks delivery. Failure of the
initial intent leaves no reservation; later preparation failures retain a failed
outcome. The wing rechecks authority and persists the delivery handoff before
sending. A storage error before sending records **failed**. Delivery appends a
separate delivered, unconfirmed or failed audit line and stores both receipts;
either receipt can resolve a failed write to the other. If both receipt writes
fail, the delivery audit still resolves a known delivery or non-delivery.
An identical reserved attempt returns its stored outcome; changed decisions and
competing attempts conflict. A crash during preparation stays **failed**; a crash
during delivery or a lost reply leaves **unconfirmed**, which is never resent.
**Delivered** means the hook accepted the answer, not that the command executed or succeeded. Inspect
the session for that result.
