# Browser E2E tier — shared-roost org mode

`make test-web` runs a full browser-to-egg canary in Docker: a roost in
RoostMode (dummy OAuth client) with an org wing.yaml mirroring a real
shared deployment — admins, per-role path ACLs, audit — and a Playwright
container driving three enrolled users plus one non-enrolled negative control
through the flows nothing else tests
above the protocol level:

- dashboard + shared wing visibility for admin and members, desktop and mobile
- palette terminal launch into a role path (the static `canary-agent` echo
  binary installed as `claude` — the sealed shared-host runtime refuses
  scripts, so the stand-in must be a self-contained native executable)
- the E2E identity lock (fail-closed key derivation, TOFU pinning)
- terminal input/output round trip and resize-over-tunnel
- detach + reattach with scrollback replay
- per-path ACL denial for non-members (filtered dir.list)
- account page (org section correctly hidden in roost mode), /api/orgs
- exact-email roost enrollment rejects a pre-existing outsider cookie and hides
  the wing inventory
- a separate no-allowlist roost proves the historical org-mode deployment
  contract: the same authenticated outsider remains admitted and can see the
  embedded shared wing when `WT_ROOST_ALLOWED_EMAILS` is absent
- end-session removes the durable terminal through the authenticated tunnel
- a second hosted-policy server proves direct-only free readiness UI and blocked
  deep links, while explicit Pro and temporary-migration accounts retain relay

Auth uses sessions seeded straight into roost.db (`seed.sql`) — no OAuth
secrets, no external services. The roost is served over plain HTTP, which is
NOT a secure browser context, deliberately: the suite regresses insecure-origin
support, so no secure-context-only web API may be load-bearing in the app.

Member sessions require a per-path egg.yaml (folder-ACL design); `entry.sh`
installs the trusted-container policy (`base: none`) into each role dir, the
same shape production ansible installs per role path.

Artifacts land in `out/`: `results.json`, `legacy-org-results.json`, and
`direct-results.json` (hard assertions), screenshots, and server logs. Exit code
is non-zero if any step fails.

`deployed-org.mjs` is the corresponding live-roost canary. It takes four
short-lived session tokens from the environment and verifies the public TLS
endpoint, org identity/role path filtering, mobile layout, the full encrypted
terminal open/detach/reattach/end lifecycle, and the no-enrollment-allowlist
backward-compatibility contract. It records the one terminal ID it creates in
`deployed-org-results.json`, allowing the operator to clean up precisely after
an interrupted run. It does not create or modify database records itself.

CI normally uses Playwright's bundled Chromium. A live operator can set
`WT_E2E_CHROMIUM_EXECUTABLE` to an installed Chrome or Chromium executable to
avoid downloading a browser.
