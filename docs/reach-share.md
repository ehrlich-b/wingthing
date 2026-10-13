# Connection details and dormant private access

Open **Connection & privacy** on `/app/` to inspect the selected wing and measure
one encrypted `wing.info` request. The displayed request round trip includes
roost transport, connection setup and wing processing. It is an observation,
not an SLA or a physical LAN/cellular detector. Privacy details describe the
existing browser trust boundary, TOFU identity pins and persistent wing keys.

The dashboard groups sessions by exact wing and nearest catalogued project.
Search matches name, agent, project, machine and ID. Attention filtering follows
native lifecycle and existing attention signals. Completion markers are local
per-viewer **unseen completions**, not arbitrary unread output. Offline rows
retain last reported lifecycle and disable runtime actions. Stop asks for the
machine and exact session; Enter/Escape cancels an armed confirmation. A wing
acknowledgement remains distinct from an independently observed process exit.

## Dormant paid UI

A local `wt roost start --foreground --dev` exposes reach, private-sharing and
SSH preview tabs through `reach_share_preview` in authenticated `/api/app/me`.
Both existing development and local-mode flags must be true. Hosted development
and normal local roosts do not expose those tabs. This is an additive use of the
existing `--dev` flag: the pinned code has no separate contextual Pro-offer flag.
There are no new purchase offers or paid runtime endpoints.

The tabs exercise in-memory, non-authoritative fixture state. Registration is
idempotent per wing; route preference is LAN, tailnet, then an entitled relay.
Key mismatch and device revocation stop before fallback. Reconnect observes the
original immutable input and never submits another copy. Fixture receipt checks
must match wing, session and intent; an absent receipt stays unknown.

Private app drafts bind wing plus port. Google/GitHub fixture subjects receive
an exact app-view or separate session-observer grant. No real OAuth account or
email is used. The Alice view omits owner controls; failures distinguish an
unavailable upstream from generic denial without leaking resource metadata.
Revocation closes fixture viewers and rejects subsequent access. Duplicate ports
on different wings remain different resources. Observer input stays disabled.
SSH produces a planned command review with machine and OS principal; it never
opens an OS shell. None of these fixture grants can authorize a runtime action.

Runtime paid publication, hosted phone relay selection, durable input receipts,
OAuth identity invitations, real preview stream revocation and SSH transport
remain unimplemented. Native phone grants/device management, physical cellular
latency and multi-route inventory merging require their separate implementation
and acceptance. No browser fixture proves these production paths.

## Reproduction

`make check` runs web contracts, all untagged Go tests and builds both channels.
`make test-web-reach-share` starts a disposable loopback roost and two encrypted
fixture wings, then drives Chromium at desktop and mobile browser sizes. Install
Playwright in a disposable prefix or provide `PLAYWRIGHT_MODULE`; optionally
provide `CHROMIUM_PATH` to use an existing executable. `WT_REACH_OUT` selects the
receipt/log directory, `WT_REACH_SHOTS` the screenshot directory and
`WT_REACH_PORT` an unused loopback port. The script shuts down its owned roost
and records request types without credentials or private keys.
