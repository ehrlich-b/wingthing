# wingthing.ai Production Canary

Date: 2026-08-25 through 2026-08-27

Status: Fly release v306 healthy; rollback releases and database backup retained

## Initial v301 deployment identity

- application: `wingthing`
- Fly organization/account: the personal organization owned by
  `ehrlich.bryan@gmail.com`, which owns the existing application
- source branch: `feature/direct-control-free-tier`
- source commit: `db0dc78` (`feat: dogfood direct agent orchestration`)
- release: v301
- image: `wingthing:deployment-01M0XHPKXMTQJ50EVJ44PBPP01`
- image manifest: `sha256:8892386e55d3d86f6d9e6459d2ead8bd0ad8ac0419f2b42efab76f9e17c41fe3`
- region/process: one `login` machine in `ewr`, version 301, with its health check
  passing
- prior application release: v300, retained as the pre-feature Fly rollback target

The deployment was built from a clean committed tree. It made the new product
positioning and Patterns catalog public and changed the hosted default to
coordination/direct control for new free accounts. It did not create edge processes
or alter the single-volume SQLite topology.

## Data safety and cohort check

Before deployment, SQLite's online `.backup` operation wrote a consistent copy to:

`/data/pre-direct-control-20260825.sqlite`

The copy was also downloaded to the operator machine. Its SHA-256 is:

`af3c1fec10406aff84b8fd6c32f66d5268c9363da7dde28db42d1a391cc1a1c9`

The Fly volume also retained its automatic snapshots. After deployment:

- `PRAGMA integrity_check` returned `ok`;
- the database contained 25 users and 8 entitlement rows; and
- all 25 existing users were created before the configured migration boundary of
  `2026-08-26T00:00:00Z`; none fell on the new-free side of the boundary.

No `WT_RELAY_POLICY` secret overrides the runtime policy, so the public OAuth
deployment uses the code default `direct-free`. Existing pre-cutoff accounts retain
temporary hosted relay parity; entitled accounts retain relay; new free accounts
created after the cutoff receive coordination and direct agent control without
hosted terminal-byte relay.

## Post-deploy canaries

The following public paths returned HTTP 200 from the new machine:

- `/`
- `/health`
- `/docs`
- `/patterns`
- `/patterns/remote-orchestration/INSTRUCTIONS.md`
- `/patterns/independent-roosts/INSTRUCTIONS.md`
- `/install.sh`

`/patterns` returned 404 before the deployment and is now live. The remote recipe
describes the shipped native direct connector; independently administered roosts
are honestly documented as separate client-side MCP entries rather than falsely
claiming peer federation.

A native `wt mcp connect --roost https://wingthing.ai` process completed MCP
initialization. Its anonymous `wing_list` call returned HTTP 401, demonstrating that
the public coordinator fails closed rather than leaking its wing roster. Fresh
authenticated human enrollment was not exercised in this canary and remains a
release follow-up.

The existing public wing reconnected three seconds after the new service started.
Startup and migration logs were clean, and the deployed machine remained healthy.

## v302 Patterns clarity follow-up

Fly release v302 deployed commit `d3d6024` after the first public review showed that
the Patterns page mixed internal product research, unimplemented gaps, and setup
guides. The current page contains six supported setups. Every card states the user
outcome, prerequisites, and concrete result in ordinary language. The copied guides
use the same framing.

The v302 page removes:

- the internal “workflows people are asking for” table;
- implementation-state vocabulary such as “client-side” and “compose now”;
- scheduled delivery, worktree, federation, and other unshipped items; and
- the independent-roost pseudo-pattern, whose old recipe URL now returns 404.

Post-deploy checks confirmed all six setup cards, their `You need`/`You get` sections,
and setup-guide actions are present. The internal narration and removed pattern are
absent. Fly reports machine version 302 healthy with its one check passing.

## v303 Self-host-first browser follow-up

Fly release v303 deployed commit `20a3d7f`. Pattern 04 now leads with the smallest
self-hosted browser topology instead of hosted entitlement tiers:

```text
localhost browser -> local roost -> SSH tunnel -> remote wing -> agent
```

The published recipe contains executable `wt serve --local`, SSH reverse-forward,
`wt login --roost`, and `wt start --roost` commands. It states the loopback trust
boundary, device authentication, browser-to-wing terminal encryption, and the rule
that local mode must not be bound to a LAN or public interface. Public checks
confirmed `/health`, `/patterns`, and the recipe all return HTTP 200 with the new
content.

Before deployment, the same topology was exercised locally against a real WSL2
wing. A localhost-only roost on macOS was carried through an authenticated SSH
reverse tunnel and a WSL-adapter-only bridge. The WSL wing authenticated with an
isolated device token and launched the real Claude Code 2.1.185 binary in an
isolated project directory. A macOS browser attached at a localhost session URL,
the wing re-keyed the browser-to-wing channel, and its replay buffer delivered the
live Claude terminal. No Wingthing listener was exposed to the LAN.

## v304 Sealed provider-login follow-up

The WSL canary then exposed an expired Claude OAuth token. The remote Claude CLI's
supported headless flow produced a one-time HTTPS authorization page and accepted
the resulting code on WSL. This is the intended trust boundary: when login runs in
a Wingthing terminal, the one-time code travels inside the browser-to-wing encrypted
terminal stream, the roost cannot read it, and Claude redeems and stores the durable
credential only on the wing.

After login, a real Claude run inside the Wingthing Linux sandbox returned the exact
probe `WSL_WINGTHING_OK`. Its logs showed the allowlisted Claude domain proxy active
and an unrelated Datadog destination blocked. Fly release v304 deployed commit
`cde1eae`, which adds this verified provider-login step and trust explanation to the
public recipe. `/health` and the published Markdown recipe both returned HTTP 200
with the new content.

## v305-v306 Local HTTPS follow-up

Fly release v305 deployed `6c4746a`, adding the opt-in device-local HTTPS flow for
`wt roost start --https` and `wt serve --local --https`. The generated CA is
name-constrained to localhost/loopback, private keys remain under the local
Wingthing state directory, and only the public CA enters the current user's trust
store.

Fly release v306 deployed `d1610e6`, which verifies the platform trust result before
recording installation success and fixes the macOS trust ceremony exposed during
the field E2E. On 2026-08-27, `fly status -a wingthing` reported the single `login`
machine in `ewr` started at version 306 with one of one health checks passing. Its
image is `wingthing:deployment-01M0Z236DFZZGPMREMW4NH4P7R`.

These releases updated the hosted documentation and binary used by the self-hosted
walkthrough; they did not make the device-local CA part of public Fly TLS.

## Rollback

Release v305 is the immediate rollback for v306, v304-v301 are prior feature
releases, and v300 remains the pre-feature rollback
in Fly's release history. After any rollback, verify
`/health`, wing reconnection, and existing sessions. Use the pre-deploy SQLite backup
only if the old runtime cannot safely read the current schema; restoring it would
intentionally discard writes made after the backup and therefore requires a separate
operator decision.

## Known canary limits

This deployment does not close every P0 product gate. The remaining high-value
checks are:

- fresh authenticated login and MCP enrollment on a post-cutoff free account;
- a physical home-plus-office two-wing exercise through the public coordinator;
- N-1 published client/wing compatibility against v301;
- browser-direct terminal transport for free human users;
- typed workspace/worktree preparation; and
- remote schedules, service identities, and delivery targets.

Bryan remains the deeper organization-mode and real-Sonnet field canary; see the
[Bryan field report](bryan-wingthing-direct-control-field-report.md).

As rechecked on 2026-08-27, the latest GitHub release remains `v0.144.1`, which
predates `wt mcp connect` and local `--https` even though the Fly website documents
them. Do not treat the current `curl .../install.sh | sh` path as a successful
feature install until a matching GitHub release is published. This branch makes the
installer fail safely in that state and adds a checksum/command contract plus
release-workflow gates; those changes are not present in Fly v306.

After v301 was deployed, Bryan was pinned to the exact `db0dc78` runtime. A fresh
authenticated native connector listed its one authorized wing and fetched
`wingthing_capabilities` over direct WebRTC while `hosted_relay: deny` remained in
force. Its three tracked pre-existing session PIDs survived the final restart.
