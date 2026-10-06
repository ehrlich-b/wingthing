# Personal coordinator and home roost

The product is one persistent conversational dot that holds a person's intent
and coordinates inspectable native tasks. Its home roost owns the coordinator's
durable state; a job executor owns a particular workspace and agent execution.
Those can be on the same machine initially. They are different responsibilities
and must not become one interchangeable ID when more machines are added.

This change supplies an original public coordinator role, injected by the
existing Claude parent launch/resume path, and observational response context.
It does not implement a new home-roost identity, cross-host task adoption,
ownership epoch, general phone inbox, pairing, push service, or network-default
change. The unexposed mailbox draft remains separate. The full configured
parent-to-two-child journey still has the recorded nested proxy bind blocker.

## Public role actually supplied to a parent

[The public prompt](../../internal/localmcp/prompts/personal_coordinator.md) is embedded by
`cmd/wt/coordinator_role.go`. `prepareBoundParentMCP` passes it through Claude's
existing `--append-system-prompt` argument alongside the unchanged per-root
`--mcp-config`. It is authored for this product; it does not reproduce a Codex
system prompt or private session instructions. Existing selected model arguments
are preserved. A resume describes the newly reserved execution while retaining
the same logical root and immutable MCP configuration.

The role explains launch intentions and stable request IDs, exact task/executor
references, native readiness and text receipts, inspection of actual results,
revisioned checkpoints, explicit opt-in wake, and human approval boundaries.
It distinguishes native foreground completion from task success, unconfirmed
input from definite no-input proof, and offline availability from completion.
`conversation_bootstrap` returns the same public prompt for inspection; that
operation remains output-only and does not install or authenticate a provider.

Sol remains the engineering driver. Direct personal included-subscription
Opus 5.5 output was verified on the original WSL home. The separate preview
profile failed a real Wingthing-managed launch with `authentication_failed`;
its human login is now authorized and remains owned by the dedicated dogfood
executor. A completed preview OAuth browser flow then failed credential saving:
relocated `HOME` hid macOS's default Keychain, and lexical `/tmp` versus
`/private/tmp` config paths selected different vendor credential services.
The coherent Mac preview now separates the OS account home from canonical
provider data identity for login, status and runtime. The private terminal login
route has source and fake-provider proof; real save/read and model output still
require the dedicated executor's independent review and human retry.
Direct WSL success is not Wingthing dogfood proof. The role itself
does not select a provider, switch a
model, import credentials or add paid fallback. The current linked interactive
implementation supports Claude; Codex's parser/contract work is not a completed
interactive coordinator adapter.

## Identities: current facts and next seam

| Identity | Intended meaning | What is available now |
| --- | --- | --- |
| DotID | Persistent logical root, eventually independent of coordinator placement. | `Conversation.RootID`; still qualified by its local wing namespace. |
| TaskID | A durable unit of delegated work with parent lineage. | `Conversation.ID` and `ParentID`, reserved before launch. |
| HomeRoostID | Stable identity of the host responsible for this dot's coordinator and durable inbox/outbox. | Not persisted as a distinct identity. A relay URL, label, local roost process or WingID is not this ID. |
| ExecutorID | Stable identity of the wing that executes a job and holds its workspace/provider state. | `Conversation.WingID`, from the existing wing configuration; empty means unknown. |
| Execution reference | One job attempt on one executor and one exact provider conversation. | Local session ID plus native provider identity in lifecycle metadata. Prior local executions are retained. |
| Coordinator owner epoch | Monotonic authority generation for one active home coordinator. | Unsupported; no executor/effect fencing is implemented. |

`conversation_read`, each returned task, linked launch results, checkpoints and
bootstrap now include additive `coordinator_context`. For a known local root,
the shape is:

```json
{
  "version": "wingthing.personal-coordinator.v1",
  "dot_id": "root-task-id",
  "task_id": "root-task-id",
  "executor_id": "stored-wing-id",
  "executor_identity_state": "known",
  "session_id": "latest-local-execution",
  "workspace": "/selected/local/workspace",
  "identity_scope": "local_wing",
  "home_roost_id": null,
  "home_roost_identity_state": "not_persisted",
  "owner_epoch": null,
  "owner_epoch_state": "unsupported",
  "cross_host_adoption_supported": false
}
```

Children also have `parent_task_id`. This is factual context, not an admission
claim or a new routing operation. Clients must not manufacture the missing
values or send new fields to existing closed tool schemas. A missing epoch does
not mean epoch zero, and a matching dot string on another wing does not establish
the same conversation. UI selection remains qualified by account/realm, wing
and logical conversation until an explicit migration contract exists.

The exact current constraint is visible in
`internal/store/migrations/014_conversations.sql`: a conversation has one
`wing_id`; `conversation_executions` is keyed by local `session_id` and has no
executor-qualified identity or owner claim. `ResumeConversationExecution` links
local executions and does not adopt a remote task. Remote terminal/MCP access
already addresses a selected wing, but it does not synchronize workspace,
provider credentials, conversation DB or memory to another wing.

## One active owner and explicit migration

The next state model needs a separately persisted realm-qualified DotID,
HomeRoostID and claim `{dot_id, home_roost_id, owner_epoch}`. A single writer
controls each claim. The home keeps coordinator state and event delivery;
executors keep native journals and exact attempts. A friendly endpoint, fresh
process, heartbeat or newer timestamp cannot assert ownership.

An ownership epoch is useful only if executors and the broker for consequential
effects enforce the current claim before admitting each effect. Including it
in an LLM prompt, phone request or database row is not fencing. Existing eggs
and arbitrary provider shell actions do not enforce such a claim today. A future
effect broker must also journal stable operation IDs and outcomes; uncertainty
after a crash is reconciled against that journal and the target rather than
claimed to be exactly-once execution.

Migration is an explicit human operation, separate from reconnect. Quiesce the
old coordinator, reconcile its durable inbox/outbox and uncertain operations,
retain native execution tails, revoke the old claim at affected executors/effect
brokers, and obtain acknowledgements before granting a new home a higher epoch.
Transfer and verify the checkpoint and delivery cursors before starting the new
coordinator. An unreachable executor cannot attest that an old claimant stopped;
that target remains blocked until its claim/effects are reconciled. A phone
cannot silently fail over because its last endpoint timed out. No step in this
sequence is implemented by the new observational context.

## Durable messages and honest delivery stages

A future home inbox receives stable message IDs with a payload digest, realm,
DotID, requested home/epoch when known, parent intent, and bounded timestamps.
The phone retains its own outbox before transmission. The home commits an inbox
reservation before reporting receipt, and creates a durable TaskID before
starting a job. Its outbox retains qualified execution targets, operation IDs,
native receipt/event cursors and unresolved outcomes. Duplicate delivery with
the same ID/digest reconciles the original; changed content is rejected.

| Display stage | Required evidence |
| --- | --- |
| Saved locally | The client's durable outbox committed the draft. The home may be unreachable. |
| Received by home | The selected home committed that message ID/digest and returned a verifiable receipt. This does not mean the model read it. |
| Admitted | The home persisted the task after checking current claim, path, grant and budget. This does not mean provider input arrived. |
| Native text receipt observed | Exact provider user-text evidence followed a reserved prompt cursor. Current causality remains unverified without a provider request ID. |
| Processing | Fresh native working evidence for the exact admitted execution. A spinner or open connection is insufficient. |
| Needs human input | Native provider attention on the exact child; inspect its source and leave the decision to the person. |
| Offline/unavailable | Current reachability or evidence is missing. Retain the last report and its age; do not infer task completion or retarget uncertain work. |

These are separate facts, not a linear promise of success. A message can be
saved locally while offline, received while waiting for admission, or have a
native receipt without proven processing. The current store already provides
launch reservations, checkpoints, replayable events and an explicit opt-in wake
outbox (`015_conversation_wake.sql`/`016_conversation_wake_retry.sql`). It does
not provide the general home inbox or these new stage acknowledgements. Existing
`session_prompt` retains its own operation ID; a generic timeout or zero byte
count never licenses a fresh send. A typed `not_sent` with
`definitely_not_sent=true` is the narrower recoverable case.

## Network direction: no vendor infrastructure by default

The personal default should require no Wingthing/vendor account, discovery
service, rendezvous, relay, mailbox, telemetry or automatic network check.
Start with direct LAN reachability, local discovery and a QR identity bootstrap.
Discovery carries non-secret endpoint/identity hints; it is not authority. A QR
can pin an identity and selected realm but cannot open a NAT or make an offline
home reachable.

Away from LAN, the person must select a reachable user-owned endpoint or an
already configured VPN/Tailscale/self-hosted relay. Reuse existing direct,
encrypted tunnel or WebRTC/relay primitives for that selected endpoint; never
fall back to a vendor directory/relay when direct connection fails. A paid hosted
roost is an independently selected peer on the same protocol, with its own
identity and authority, not an implicit owner or automatic fallback.

This is a new default direction, not a description of every shipped code path.
`WingConfig.HostedRelay`, existing account registration and coordination-only
hosted paths still exist. This patch does not rewrite them. A release claiming
the new default needs a separate ordinary no-network/default-source test that
proves no vendor contact before explicit selection. An endpoint stored by the
person is not permission to discover a different one.

The existing no-login local portal is loopback-only; `docs/security.md` records
that explicit LAN/wildcard no-login listeners are rejected. Do not expose that
mode to a phone by weakening the listener/auth guard. A direct LAN bootstrap
needs a reviewed per-device consent and authentication path first. The current
native client scaffolds can reuse selected authenticated endpoints and existing
encrypted `session.control` without claiming that QR enrollment already exists.

## Phone pairing, realms, revocation and push seams

Proposed pairing begins at the person's existing trusted desktop/home view. A
short-lived QR describes the selected endpoint, realm/home identity or explicit
unknown state, public-key fingerprint, nonce and requested scope; it contains
no model credential or durable bearer secret. The phone creates its device
key and verifies the pinned host. The trusted home displays the phone identity
and requested access, and the person explicitly approves it. Confirmation
binds that device to that realm only; another home requires another decision.
Current passkey challenges and owner/wing policy checks are reusable primitives,
not a completed QR/device enrollment protocol.

Store phone state, keys, outbox and selections per realm. Revocation must stop
future authenticated dispatch, invalidate that device's tokens/active input
leases where appropriate, and prevent pending drafts from being automatically
sent through another realm. Existing wing allow/revoke and restart token
revocation provide seams, but they are not yet a per-phone home claim API. No
pairing, enrollment or grant is changed by this role/context patch.

Generic native push can later be an explicit opt-in wake hint carrying only an
opaque event nonce and realm reference. Fetch actual state from the selected
authenticated home, then inspect the exact task. Push delivery is not receipt,
processing, permission approval or ownership transfer. Existing ntfy attention
delivery is a separately configured feature and can carry agent/cwd data; it
must not be described as the new content-minimal generic push protocol. This
slice registers no APNs/FCM service, hosted relay or push entitlement.

## Coherent next acceptance

1. Start the personal candidate with no vendor contact before endpoint choice;
   distinguish that new default proof from the existing runtime configuration.
2. On a deliberately selected home, inspect the exact public role and known
   context, and verify that unknown home/epoch fields stay unknown.
3. Complete the unchanged-sandbox configured parent-to-two-child transport
   fixture, then a personally authenticated provider run. The current nested
   proxy bind blocker is still a prerequisite, not fixed by this prompt.
4. Pair a phone through reviewed explicit consent; save a draft offline, reconnect
   to the same realm/home, and show durable home receipt separately from native
   processing. Repeat the message ID without duplicating a task or input.
5. Inspect child results/tools, manual attention and opt-in wake. Restart the same
   home and recover inbox/outbox/checkpoint cursors without automatic resend.
6. Revoke the phone and prove future dispatch stops. Attempt explicit home
   migration only after executor/effect claim enforcement and uncertain-outcome
   reconciliation have passed independent review and ordinary fixtures.

This patch's tests cover public-role argv/bootstrap parity, preserved provider
selection, truthful local context, task linkage and resumed invocation facts.
Those tests establish the role/context seam; they do not prove pairing,
authenticated synthesis, network defaults, fencing or the full journey.
