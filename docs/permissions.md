# Answer one Claude Bash request

On a personal local wing, `wt` can review and answer a live Claude Bash
PermissionRequest. The selected request shows its complete command, machine,
OS user and directory. `a` opens a scrollable review; after reaching its last
line, Enter sends **Allow once**. `d` sends deny. `A` always opens agent setup;
`D` always opens diagnostics. Requests without a live supported response handle
say **Answer in terminal**. The browser uses that terminal fallback too.

Enable the human adapters explicitly in the personal wing's `clients.yaml`:

```yaml
clients:
  tui:
    owner: default
    grants: [permission.read, permission.respond]
  browser:
    owner: default
    grants: [permission.read, permission.respond]
```

The socket still checks the wing owner, client, logical session owner, paths,
lock and passkey requirements. Neither reading nor terminal input grants the
right to answer a permission. This first slice exposes no permission MCP tools
and performs no remote permission routing. Clients that already exist must
retain their other grants when adding these two.

The same contract is available to human scripting with `wt permission list
--wing-id W --session-id S --json` and `wt permission respond --wing-id W
--session-id S --request-id R --digest D --attempt-id A --decision allow|deny
--json`. IDs and the digest come from the displayed request; an attempt ID must
be new for each decision. Never automatically retry a delivery.

Each interactive Claude egg adds invocation-only `permissions.ask: ["Bash"]`
and a synchronous hook alongside existing hooks. A per-process secret
authenticates its private IPC; it never enters settings or argv. The 90-second
application deadline precedes the 95-second provider hook timeout. Expiry,
cancellation or unsupported input returns no decision, retaining native
permission handling. `disableAllHooks` stays effective; deny rules and the
sandbox continue to apply. There are no saved rules or blanket approvals.

The wing's private `permissions/` ledger synchronizes competing surfaces,
fsyncs a reservation and audit record before delivery, and rechecks authority.
An audit failure blocks delivery. An identical attempt returns its stored
outcome; changed decisions and competing attempts conflict. A crash or lost
reply leaves **unconfirmed**, which is never resent. **Delivered** means the
hook accepted the answer, not that the command executed or succeeded. Inspect
the session for that result.
