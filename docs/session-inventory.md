# Qualified personal session inventory

`wt session inventory --remotes --query 'DNS helper' --json` searches live and
archived sessions through the running personal wing. `session_inventory` exposes
the same contract through MCP. `query` matches all words across session name,
repository path, machine label, provider and recorded transcript text. The wing
checks session ownership and path scope before reading content. The response
contains match field names, never transcript snippets or raw provider records.

Inventories use version `wingthing.session-inventory.v1`. The top-level `wing_id`
is the connected endpoint. Every row has `origin_wing_id`, full `session_id`,
`machine_label`, `live`, `status`, `pinned`, `role`, `needs_attention` and `routes`.
Routes identify the paired endpoint (`wing_id`), kind (`direct` or `via`), gateway
label, saved remote name, health and RFC3339 `observed_at`. Source health/errors,
`complete` and `generated_at` remain visible even when a remote is unavailable.
A successful empty source is distinct from an unreachable source.

Pair each endpoint independently. Merge only equal nonempty
`(origin_wing_id, session_id)` tuples. Short IDs, names and labels do not establish
equality. Retain unproven origins separately and mark `possible_duplicate`.
`wt session merge <inventory.json>... --json` and `internal/fleet.Merge` implement
this reconciliation. Files must come from authenticated paired endpoints; merge
is not authentication. Same-origin sightings accumulate routes, while sessions
with equal display IDs on different origins remain separate.

Before dispatch, select a healthy route observed within 30 seconds: direct first,
then via a gateway. `internal/fleet.SelectRoute` implements this selection and
rejects unknown origins, stale/future observations and a direct route to another
origin. A phone must refresh each endpoint independently and mark unreachable
routes unhealthy. After a mutation is sent, a disconnect has unknown outcome:
never send it again through another route. Read the original receipt; native
`session_prompt` retries retain the original `request_id` and arguments.

`wt session action --wing <origin> --session <full-id> --operation session_read
--arguments '{"limit":50}' --json` and MCP `session_action` act on exact identities.
Supported operations are `session_status`, `session_read`, `terminal_read`,
`session_prompt`, `terminal_send`, `terminal_stop` and `terminal_rename`. The
arguments omit `session`; names and prefixes are refused. The result includes the
selected route and execution identity. Controller and execution wing both check
the underlying operation grant. Current saved pins, ownership, path, passkey and
writer gates still apply. A forwarded action cannot recursively route onward.
Qualified launch uses the existing runtime capabilities described below. A
typed provider approval protocol remains unavailable; terminal input does not
establish delivery to an exact pending approval request.
Qualified live phone attachment uses the same authenticated
wing-control stream as the CLI, as described below.

Only explicitly configured, verified personal remotes participate. Configure
through the existing `wt mcp connect add` handshake. No recursive fanout occurs.
Organization/shared wings and conversation/path-bound connections cannot fan out.
Unknown or old remote contracts report incomplete coverage with an upgrade or
verification error. The existing SSH inventory v1 is preserved with additive
origin and label fields; older responses lacking origin stay unproven.

Paired phone commands retain their encrypted carrier, authentication and sequence
checks. Use `op: "inventory"` or `"search"`, with `input` holding JSON
`{"query":"...","include_remotes":true}`. Use `op: "action"`, with `input` holding
`{"origin_wing_id":"...","session_id":"...","operation":"session_read","arguments":{}}`.
`wt session launch --wing <origin> --operation terminal_start
--arguments '{"command":["/bin/sh"],"cwd":"/personal/project"}' --json`
and MCP `session_launch` start a new session on an explicit personal origin.
Use `terminal_start` or `agent_start` with their existing argument schemas.
The execution wing chooses its launch policy, workspace, credentials and durable
state; nothing is copied between wings. The response contains the exact newly
created `session_id`, `origin_wing_id`, selected route and underlying result.
Verified saved pins and both endpoints' launch grants, path restrictions,
session quotas, passkey and sandbox checks still apply. Organization/shared
wings and conversation-bound connections cannot use qualified launch.
Continuation through `resume_session` is refused by this new-session operation.
One route is dispatched once; no automatic retry or alternate-origin launch
occurs after a lost response. Plain terminal and unlinked agent launches do not
promise idempotency: inspect the origin inventory before deliberately retrying.
Linked conversation launches retain their existing exact `request_id` receipt
contract. A phone may select a different healthy endpoint before dispatch.

Paired phones use `op: "launch"` with `input` holding JSON
`{"origin_wing_id":"...","operation":"terminal_start","arguments":{}}`.
Phone/browser `agent_start` launches preserve the existing linked-personal-
conversation requirement; supported providers remain those of that underlying
capability. Unlinked provider launch is available through the qualified CLI/MCP
surface, within its own authority.
Encrypted browser tunnels use `sessions.inventory` / `sessions.search` with
`arguments` and `session.control` with `operation: "session_action"`.
Use `session.control` with `operation: "session_launch"` for qualified launch;
this owner-bound personal path preserves socket client and passkey restrictions
even when the origin is local. An existing browser passkey token does not bypass
the socket path's unavailable MCP passkey ceremony.
Live phone terminal commands are `fleet_attach`, `fleet_read` and `fleet_input`.
Each `input` envelope includes `origin_wing_id` and the full `session_id`.
Attach additionally accepts `read_only` or explicit `takeover`; input additionally
accepts the terminal bytes as `input`. For example:

```json
{"op":"fleet_attach","input":"{\"origin_wing_id\":\"execution-wing\",\"session_id\":\"full-session-id\",\"read_only\":true}"}
```

The encrypted response includes the selected `route`, exact identity and mode.
Read returns bounded `output`, a rendered terminal `screen`, `ended` and
`truncated`. Attach waits for the actual egg acknowledgement, retains one route
for its lifetime, and never retries a takeover or input through another route.
The client explicitly detaches/reconnects after reading state. Generic `detach`
and connection close release either attachment type. Legacy local `input` cannot
write through a qualified attachment; qualified input cannot cross origins even
when two sessions have the same ID. A read-only attachment cannot input.

The gateway rechecks owner binding, current grants, path/passkey policy,
`phone_fleet`, and the saved remote pin before each read/input. The execution wing
also rechecks current policy before forwarding terminal frames; reads check the
execution wing before returning buffered content. Failed checks close the
attachment. A one-second policy observer also closes a quiet attachment after
delegation/pin revocation; its remote policy check is bounded to two seconds.
Readiness uses one five-second budget across SSH, welcome and the egg acknowledgement; output retention is 64 KiB and
individual input is limited to 16 KiB. A returned input `ok` records transport
submission, not a provider delivery receipt; after loss its outcome is unknown.
No egg token is sent to the phone, and no remote software is provisioned.

Remote phone delegation additionally requires `phone_fleet: true` in the personal
gateway's wing.yaml and the authenticated durable local owner binding. The
flag hot-reloads through SIGHUP; disabling it revokes existing gateway
attachments and new remote requests. Pairing
alone does not enable remote access. The bridge retains clients.yaml restrictions;
locked/passkey-protected wings refuse this socket-backed phone fleet path rather
than bypassing the existing MCP ceremony limitation.

Pinned coordinator rows sort first through any healthy route. The optional
`presence` reports awake/asleep, recorded last/next wake and last note, source,
observation time and staleness. `wt session heartbeat <full-id> --state asleep
--last-wake <RFC3339> --next-wake <RFC3339>` (MCP `coordinator_heartbeat`) records
presence on an owned coordinator role session. It records data and never installs
or arms timers. Missing evidence is unknown; records older than 120 seconds are
stale/unknown; absent active egg process identity reports dead/unavailable while
retaining wake times. Native working/idle states are an explicitly labeled fallback
that retains the state-defining event's timestamp. Missing, invalid, future or
older-than-120-second native timestamps produce stale/unknown presence. Reading
an inventory or a newer transcript message never refreshes old native evidence.

Search covers Wingthing-managed sessions and wing journals plus archived chat,
not unrelated vendor sessions that were never imported. Reads are bounded to
8 MiB decompressed per transcript file, 32 MiB per query and 200 result rows;
query length is at most 256 bytes and 8 words. Native `source_complete` distinguishes import completeness from event-page
`has_more`; a partial import makes the search incomplete. Limit, malformed archive, cancellation
and source failure must not be presented as complete search coverage. A future
paged index can remove these bounds without changing qualified identity.

Validate with `make test-multiwing`, `make test-multiwing-race`, `make check` and
`make gate`. The built fixture runs three disposable stable/preview wings, encrypted paired
phone requests, real config/SIGHUP revocation and SSH Unix forwarding;
it proves routing/contracts, not a physical phone, public tunnel or live mesh.
