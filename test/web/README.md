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

For isolated visual review without a provider, run
`python3 test/web/inventory-fixture.py`, then start the frontend with
`npm --prefix web run dev -- --host 127.0.0.1 --port 8264` and open
`http://127.0.0.1:8264/app/fixtures/session-inventory.html` in an owned browser tab.
The fixture uses the real app markup and inventory renderer, with synthetic
parent/child, unknown-provider, attention and offline rows. Search, filters,
keyboard focus, details and simulated WSL disconnect/reconnect work; every
runtime action is disabled. Its screenshot is visual evidence only, not proof
of live launch, approval, attachment or reconnect behavior. The generated HTML
is ignored and rebuilt from the current app markup.

Frontend session content uses `[wing_id, session_id]` references. Terminal text
and thumbnails read/write only `v2` cache keys; old bare-ID content remains
unused because its source wing cannot be recovered reliably. Canvas geometry
starts under `wt_canvas_layout_v2` with qualified keys. Attention broadcasts
use a user-scoped v2 channel and carry both IDs. `make web` includes regression
tests for equal provider IDs on two wings, independent attention/preview state,
canvas startup rekey/focus/removal, and stale terminal connection references.
These tests verify UI state isolation; supported-browser launch/reconnect QA
still requires the Browser runtime's Node REPL tool.

Preview input conflicts preserve the agent session and expose explicit
`Take control`; `Observe` appears only when the wing already enables spectate.
The UI never repeats takeover during reconnect. Resize requests carry the
acknowledged `controller_id`, including canvas panes; a pending, replaced or
read-only attachment cannot send resize. Canvas Stop retains the card until
the wing returns its typed acknowledgement and leaves a readable error after
failure. Temporary launch IDs cannot be stopped before `pty.started`.
Frontend tests exercise the exact wire flags, controller binding, action
buttons and deferred/rejected stop acknowledgements. They do not replace
live browser-to-egg acceptance against the preview adapter.
