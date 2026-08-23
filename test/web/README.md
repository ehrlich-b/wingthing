# Browser E2E tier — shared-roost org mode

`make test-web` runs a full browser-to-egg canary in Docker: a roost in
RoostMode (dummy OAuth client) with an org wing.yaml mirroring a real
shared deployment — admins, per-role path ACLs, audit — and a Playwright
container driving three seeded users through the flows nothing else tests
above the protocol level:

- dashboard + shared wing visibility for admin and members, desktop and mobile
- palette terminal launch into a role path (mock-agent installed as `claude`)
- the E2E identity lock (fail-closed key derivation, TOFU pinning)
- terminal input/output round trip and resize-over-tunnel
- detach + reattach with scrollback replay
- per-path ACL denial for non-members (filtered dir.list)
- account page (org section correctly hidden in roost mode), /api/orgs

Auth uses sessions seeded straight into roost.db (`seed.sql`) — no OAuth
secrets, no external services. The roost is served over plain HTTP, which is
NOT a secure browser context, deliberately: the suite regresses insecure-origin
support, so no secure-context-only web API may be load-bearing in the app.

Member sessions require a per-path egg.yaml (folder-ACL design); `entry.sh`
installs the trusted-container policy (`base: none`) into each role dir, the
same shape production ansible installs per role path.

Artifacts land in `out/`: `results.json` (hard assertions), one screenshot per
step, and `roost.log`. Exit code is non-zero if any step fails.
