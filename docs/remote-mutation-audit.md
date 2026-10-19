# Decision: outer-boundary authority and remote mutation audit

Status: design accepted for implementation; runtime behaviour is unchanged by
this document. The audit admission and receipt contract below is a requirement
for the next implementation, not a guarantee of the current release.

## Scope and current behaviour

`outer-boundary` selects an execution profile for a trusted, dedicated VM. It
removes nested process restrictions; it does not confer control-plane authority.
The VM administrator and every process with the wing's OS-user authority remain
trusted. A compromised same-user process can change state or bypass Wingthing;
this design is not a tamper-proof log or a substitute for an OS boundary.

Current permission checks already enforce this distinction:

- `resolveLocalWingClient` in `internal/localmcp/mcp_socket_wing.go` requires a
  personal wing and `allow_unsandboxed: true`, resolves current client grants,
  ownership and paths, and refuses unsupported lock/passkey admission. The socket
  resolves the client again on every call, including existing connections.
- `TestOuterBoundaryCannotOverridePolicy` and
  `TestLocalWingPolicyRefreshAndEmptyGrants` in
  `internal/localmcp/mcp_socket_test.go` exercise launch opt-in and revocation.
  `TestDirectMCPExistingChannelRechecksLiveWingPolicy` in
  `internal/localmcp/mcp_direct_wing_test.go` covers the direct remote channel.
- `Server.callTool` in `internal/localmcp/mcp_local.go` currently defers its
  audit until after dispatch. `auditToolCall` appends a redacted JSON record to
  `mcp-audit.log` and closes the file, without fsync. Audit errors are diagnostic
  log messages and do not change the tool result. This is best-effort auditing,
  including for mutations; the general fail-closed contract is not implemented.
- Human permission responses are a separate, narrower protocol. The ledger in
  `internal/permissions` already gates delivery on durable audit intent and
  retains ambiguous delivery outcomes. See [permissions](permissions.md).
  MCP and tunnel permission responses remain refused; this decision adds no
  remote permission-answer capability.

## Authority decision

Keep the current explicit startup choice and independent wing opt-in. Neither a
tool argument, an authenticated transport, a free or paid account, nor a previous
successful request can widen authority. Evaluate current authority on the wing
that executes the operation, using authenticated identity rather than identity
fields supplied by the caller. Intersect upstream scope with receiving-wing
scope; do not substitute either scope for the other.

Every mutation must still satisfy operation exposure, client grants, wing and
logical session ownership, exact target identity, canonical path bounds,
conversation/role restrictions, spawn/session limits, and lock/passkey policy.
Require only the checks applicable to that operation. Recheck before dispatch
after any blocking admission work, with synchronization that prevents a policy
revocation committed before dispatch from being ignored. Missing policy or an
unsupported authentication ceremony denies the call. Outer-boundary permission
revocation blocks new launches and calls; it does not retroactively sandbox a
running OS-user process or imply that such a process has stopped.

## Audit decision: fail closed before dispatch

All remote control mutations require a durable audit intent on the executing
wing before the first side effect. Apply the same rule to local typed control
mutations so SSH/stdio, direct MCP, HTTP MCP, browser/tunnel and phone adapters
cannot select different failure policies. No fail-open flag is introduced.
The rule is independent of execution isolation and optional terminal recording.

Classify operations explicitly in the shared control registry. Launch, input,
stop/kill, rename, resume/fork, task cancellation, configuration/ACL changes,
checkpoint/wake settings and permission responses are mutations. Read operations
must not acquire mutation side effects silently. A newly exposed operation with
no classification is unavailable until classified. Include legacy PTY/tunnel
handlers, attachment paths that create or resume a session, background wake
delivery and negotiated task methods in the inventory; annotations alone do
not constitute enforcement. Transport framing, terminal recording and ordinary
read diagnostics need not be journaled as control mutations.

| Failure point | Required result | May dispatch? |
| --- | --- | --- |
| Authentication or policy denial | Denied; attempt redacted denial audit | No, even if denial audit also fails |
| Intent open, mode check, encode, short write, sync, close or directory persistence fails | Audit unavailable, known not dispatched | No |
| Authority revoked or request cancelled after intent, before dispatch | Not dispatched; retain intent and attempt terminal receipt | No |
| Intent durable and current authority valid | Persist dispatch handoff, then execute once | Yes |
| Handoff persistence fails | Audit unavailable, known not dispatched | No |
| Execution fails with proven absence of side effects | Failed, known not applied | No automatic retry |
| Execution returns a result; terminal receipt cannot be persisted | Retain known result if available; audit incomplete, never ordinary success | Already dispatched; no replay |
| Lost reply, crash at handoff, or unknown execution outcome | Unconfirmed; reconcile by original operation ID | No automatic retry |

The durable journal must use private state (directory `0700`, file `0600`),
reject unsafe file types/symlink substitution, serialize concurrent appends and
propagate all persistence failures. Sync the file and, when creating it, its
parent directory before declaring intent durable. Rotation must preserve
unresolved intents and their identifiers. Do not treat writing stderr as durable
audit. Do not dispatch when the audit state directory is missing or invalid.

Use a wing-generated operation ID plus authenticated caller attempt identity,
wing ID, principal/client, operation, approved bounded target IDs, timestamp,
effective isolation and a digest of arguments. Include phase and outcome so an
intent is never mistaken for execution. Never log prompts, commands, input bytes,
permission arguments, credentials, paths or arbitrary error/result strings in
the general journal. Store bounded error codes instead. The existing permission
ledger's private request data remains governed by its narrower protocol.

Bind duplicate attempts to principal, execution wing, operation, target and
argument digest. An identical attempt returns the durable outcome without
dispatch; a changed binding conflicts. Persist a handoff state before calling
the mutator. A crash after that state cannot prove non-delivery and must remain
unconfirmed unless the underlying service supplies an authoritative receipt.
Intent-only recovery is known not dispatched. This requires operation identity
to survive all adapters and restarts; a JSON-RPC request ID alone is insufficient.
It does not promise exactly-once execution across arbitrary external systems.

Receipt failure after execution cannot undo input, kill, launch or configuration
changes. Return the original operation ID and distinguish execution outcome from
audit durability. Clients must not translate this into a generic retryable
failure. For streams, report the incomplete audit state through the control
receipt/status path even if the input/output connection has ended. Reconcile
against retained run/session/service receipts; where no proof exists, keep
unconfirmed and require human inspection. Never infer non-execution from a
missing audit receipt.

Read-only calls may continue with best-effort audit during journal failure if
their normal authority checks pass. Policy denials always remain denials.
Existing-session transport disconnect and process cleanup required to release
resources must remain possible; they cannot create work or deliver agent input.
An operator may repair storage or stop processes locally using OS authority.
Remote stop/kill requests themselves remain audited mutations, so audit failure
can prevent them. This availability cost is accepted rather than providing an
unaudited remote emergency bypass.

## Implementation acceptance

1. Inventory every mutation adapter and registry entry, including legacy and
   background paths. Use one receiving-wing admission/journal primitive; adapters
   cannot bypass it or downgrade failure behaviour. Separate the general control
   journal from optional PTY recordings and the existing permission ledger.
2. Exercise real serialized socket/direct and browser/tunnel requests with
   outer-boundary enabled: missing opt-in, grant/path/owner/bound/role violations,
   lock/passkey requirements and live revocation must cause zero side effects.
   Also run the matrix with nested isolation so audit policy cannot depend on it.
3. Inject every intent/handoff persistence failure above and assert zero mutator
   invocations. Exercise full disk/quota, unwritable state, invalid file types,
   concurrent writers and rotation without changing the user's original files.
   Confirm that denied calls stay denied when audit fails and reads stay usable.
4. Crash before intent, after intent, after durable handoff, after side effect and
   before receipt/reply. Restart and query by operation ID. Identical concurrent
   attempts dispatch at most once; changed bindings conflict; uncertain outcomes
   never resend. Inject receipt failure after terminal input and after launch to
   prove the error does not claim rollback or trigger a second side effect.
5. Verify private modes, redaction with secret canaries, effective isolation and
   principal/wing attribution. Test receipt/status behaviour through each public
   adapter, including lost reply and stream termination. Preserve the current
   human permission no-resend contract and remote refusal tests.

This change deliberately alters the current best-effort mutation audit contract.
Before enabling it in a release, implement the journal and receipt protocol,
teach supported clients to reconcile unconfirmed operations, document the
availability change and test older-client failure handling. Do not advertise
durable mutation auditing until those implementation checks pass. No migration
of historical best-effort records can establish that an old mutation was gated
by a durable intent.
