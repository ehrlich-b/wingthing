# Product gap audit — 2026-10-18

Implementation examined: `014b7e543be379428b61425ccd093d0c1f2260f4`.
This refresh changes documentation only. The implementation tree remains that
source; the documentation commit and fresh gate receipts are recorded separately
in the delivery report. WT identifiers refer to the 2026-10-16 PM inventory.
Owners below are proposed responsibility areas, not assignments or new scope.

Evidence type **source** means code or a documented contract inspected at that
head. **Unverified** means acceptance has not been demonstrated here; test code,
historical prose and a build are not retained runtime receipts. **Changed** means
the old claim needs a narrower statement. **Closed** applies only to the named
subproblem, never implicitly to every acceptance item. This audit makes no new
runtime PASS claim. The delivery report links fresh gate logs, their commands and
the exact tested head; those synthetic gates do not establish physical-phone,
hosted service, real-provider or installed-release acceptance.

## Current inventory

Each retained row names a remaining check and current evidence. Line numbers
refer to the implementation head above; links resolve within this repository.

| Item | Disposition and current source evidence | Remaining acceptance / proposed owner |
| --- | --- | --- |
| WT-001 — Send | **Open, source:** [paper.js](../web/src/paper.js#L247) submits by opening the older chat view and copying the draft at lines 250–253; it does not call the prompt operation. The shared composer still says Send at line 132. | One deliberate send, immutable uncertain-delivery ID, lifecycle gating and both responsive sizes remain unverified. **Browser messaging**. |
| WT-002 — Codex reader | **Open, source:** [conversation-state.js](../web/src/conversation-state.js#L110) restricts input readiness to Claude and `claude_hook`, even though [paper.js](../web/src/paper.js#L197) shows a composer on a connected wing. | Native Codex lifecycle/receipt handling and scoped Claude regression remain unverified. **Browser messaging + provider adapters**. |
| WT-003 — direct-free onboarding | **Changed, source:** [data.js](../web/src/data.js#L251) clears ineligible cached sessions and [palette.js](../web/src/palette.js#L48) gates launch, but [paper.js](../web/src/paper.js#L94) still renders generic login/start instructions and a New session button when wings exist. | Keep existing denial; verify direct setup guidance and all deep links/cached-wing states, with Pro/self-hosted controls. Do not describe this as absent server enforcement. **Browser onboarding**. |
| WT-004 — phone route | **Changed, source:** three distinct current routes are listed below. [PHONE.md](../PHONE.md#L7) is a `2c3688f` snapshot; its GitHub gateway default at line 22 differs from current [signed tunn SSO](pwa.md#L31). | Choose/document a default route and demonstrate its complete read/send/takeover/hand-back/stop journey. Physical phone and live ingress remain unrun. **Phone experience + access documentation; owner selects default**. |
| WT-005 — local MCP startup | **Changed, source:** [README.md](../README.md#L84) already requires a running wing in the same state directory; [install.html](../internal/relay/templates/install.html#L68) still says no daemon is required for local MCP. [Installer output](../scripts/install.sh#L81) prints registration without the local startup recipe; the [template](../internal/downloads/installer.sh.tmpl#L81) matches. | Fresh-HOME registration/capabilities and stopped-wing diagnostic checks remain unverified; distinguish standalone egg from MCP. **Install/onboarding**. |
| WT-006 — missing provider diagnostic | **Open, source:** [server.go](../internal/egg/server.go#L525) defaults preparation failures to `preparation_failed`; [errors.go](../internal/control/errors.go#L293) projects “launch preparation failed.” No dedicated missing-provider kind is in the accepted launch reason list at line 53. | Clean-PATH local/SSH reproduction, typed executable/setup guidance and secret exclusion remain unverified. The old browser capture is not a current CLI receipt. **CLI diagnostics + launch control**. |
| WT-007 — empty agent list | **Open, source:** [palette.js](../web/src/palette.js#L29) substitutes `['claude']` when the selected machine has no agents. | Machine-specific setup/login guidance, capability refresh and detected-agent path selection remain unverified. **Browser launch**. |
| WT-008 — ended controls | **Open, source:** [paper-state.js](../web/src/paper-state.js#L18) recognizes exited sessions, but [paper.js](../web/src/paper.js#L193) derives composer and takeover availability from machine connectivity at lines 197–200. | Read-only retained history, missing/unavailable distinction and explicit recovery at both sizes remain unverified. **Browser session lifecycle**. |
| WT-009 — release verification | **Open, source:** [.github/workflows/release.yml](../.github/workflows/release.yml) remains the tag build/publish job; no tracked `.gitlab-ci.yml` exists. [fly-ops.md](fly-ops.md#L81) still requires a GitHub release and `gh release view`. | Staged tag/head/hash receipt, checksum rejection and workflow equivalence remain unverified. No publication is authorized by this audit. **Release engineering**. |
| WT-010 — access explanation | **Changed, source:** [render.js](../web/src/render.js#L515) already shows tier and free-account provisioning copy at lines 533–538, but does not combine selected-wing policy with account capability. | Free/Pro/migration/self-hosted explanations and `hosted_relay: deny` precedence remain unverified; preserve offers/prices. **Account UX + entitlement policy**. |
| WT-011 — offline wording | **Open, source:** [paper-state.js](../web/src/paper-state.js#L6) maps any `online === false` to asleep, with no sleep evidence; `awayLabel` repeats it at line 37. [paper.js](../web/src/paper.js#L148) already shows last-seen and retry UI. | Two-wing disconnect, stale lifecycle/control denial and non-resending retry remain unverified. **Browser machine status**. |
| WT-012 — entry links | **Open, source:** [README.md](../README.md#L3) retains a GitHub badge, line 17 a GitHub demo asset, line 502 a GitHub clone command; [pty.js](../web/src/pty.js#L376) retains the GitHub issue URL. Manual downloads already name GitLab at README line 48. | Tracked entry-link audit and post-promotion hosted-page receipt remain open; preserve module IDs and third-party references. **Public documentation + release engineering**. |
| WT-013 — human run results | **Open, source:** [run-inventory.md](run-inventory.md#L3) documents qualified `agent_list` and CLI recovery, while [paper.js](../web/src/paper.js#L36) consumes session inventory and transcript messages, not a durable run/result list. | Human read-only result discovery, exact IDs, durable terminal distinctions and owner/wing negative controls remain unverified. Backend inventory is not missing. **Browser supervision**. |
| WT-014 — sharing availability | **Changed, source:** [reach-share.md](reach-share.md#L31) already distinguishes dormant development fixtures from runtime authority; [shared-roost instructions](../patterns/shared-web-roost/INSTRUCTIONS.md) describe the existing broader-roost route. | Current default-shell discoverability and two enrolled synthetic users plus denied outsider remain unverified; do not infer per-session invites from either route. **Collaboration documentation + browser access**. |
| WT-015 — human labels | **Open, source:** [agent_runs.go](../internal/localmcp/agent_runs.go#L86) passes label to [ValidateSessionName](../internal/eggclient/local_sessions.go#L372), which rejects spaces and limits names to 64 characters. | `weekend canary`, compatible slug retries and distinct display/resource names remain unverified. **Agent API contract**. |
| WT-016 — worktree preparation | **Open, source:** [product brief](agent-manager-product-brief.md#L121) says to compose existing terminal setup with launch; its capability table at line 163 marks typed preparation partial. The `workspace_prepare` example around line 550 is a proposal, not shipped acceptance. | One bounded idempotent worktree primitive and all policy/dirty-target/retry checks remain future work. **Workspace control**. |
| WT-017 — audit freshness | **Addressed as documentation:** this inventory supersedes the active status in [GAPS.md](../GAPS.md). The historical reports are retained verbatim below its new historical boundary. Prompt admission and phone contracts are separated below. | Runtime acceptance stays limited to retained exact-head receipts in the delivery report; missing physical/provider evidence stays unrun. **Product audit maintenance**. |

## Prompt admission and privacy are separate

**Changed / old oversize-exposure claim closed at the source-contract level:**
[codex_prompt.go:8–37](../internal/egg/codex_prompt.go#L8) measures JSON-escaped
prompt content, excluding surrounding quotes, with a **130,048-byte** limit
(`131072 - 1024`). This is not a Unicode character count or the later-session
prompt limit. The margin covers the NUL and initial-run metadata.
[runs.go:242–249](../internal/wingsession/runs.go#L242) validates before admission;
[eggclient/egg.go:763](../internal/eggclient/egg.go#L763) validates at spawn too.
Oversize rejection contains sizes only. The old request to submit an unbounded
140 KiB initial prompt to discover Linux E2BIG is stale for this path.

Current regressions are [egg/codex_prompt_test.go](../internal/egg/codex_prompt_test.go),
[wingsession/codex_prompt_test.go](../internal/wingsession/codex_prompt_test.go)
and [localmcp/codex_prompt_test.go](../internal/localmcp/codex_prompt_test.go).
Their presence is source evidence; fresh execution receipts belong in the
delivery report. Queued steering budgets escaped prior context separately and
keeps direction/request validation; it does not authorize truncating fresh input.

**Still open — argv/privacy:** [eggclient/egg.go:938](../internal/eggclient/egg.go#L938)
still transports serialized initial-run content through argv, and the
[native startup contract](codex-run-readiness.md) deliberately supplies the
first TUI prompt through argv. A size bound does not remove same-UID process
visibility or redact captured prompts/transcripts. Proposed owner: **native
provider transport + security**. Cross-platform process inspection and a real
provider startup are **unrun** here; neither is established by a fake provider.

## Phone/setup contracts at this head

| Route | Current status and source | Remaining live gate / proposed owner |
| --- | --- | --- |
| Native QR/device enrollment | **Implemented contract; physical acceptance unverified.** [IOS-INSTALL.md](../IOS-INSTALL.md) requires signing/device setup, fresh QR, matching emoji and approval on both sides; [validation section](../IOS-INSTALL.md#L53) explicitly leaves the real phone unrun. `wt pair` needs the existing main-wing state and a pairing-capable CGO build. [pair.go](../cmd/wt/pair.go) provides loopback/LAN guidance. | Signed install, camera, Local Network permission, Keychain, actual session controls and cellular routing remain **unrun**. Local-only now binds loopback; LAN pairing needs the explicit printed `--listen 0.0.0.0` restart with the live wing's mode preserved. **Native mobile + pairing**. |
| Browser inventory pairing | **Implemented inventory-only contract; hosted acceptance unverified.** [BROWSER.md](pairing/BROWSER.md#L9) explicitly denies input/launch/actions/terminal attachment; requires a registered relay-capable wing, exact app origin, PAKE and committed origin-scoped key/grant. It cannot substitute for browser control. | Fresh synthetic browser-join receipts can establish only their loopback scope, not deployed relay behavior or physical-phone control. **Browser pairing + relay**. |
| Signed tunn browser access | **Opt-in implementation contract; live ingress/phone/provider acceptance unverified.** [pwa.md](pwa.md#L31) uses one configured account UUID, public Ed25519 JWKS, exact HTTPS origin/issuer/audience and short-lived signed assertions into a personal loopback roost. It grants that owner's existing control policy, not a device inventory grant. | Public tunn assertion/ACL and stream revocation behavior, real-phone read/send/takeover/hand-back/stop and real providers remain **unrun**. [PHONE.md](../PHONE.md) and [GO-LIVE.md](../GO-LIVE.md) are older GitHub-gateway recipes, not proof of this route. **Personal web access + ingress integration**. |

## Historical evidence boundary

The 2026-10-10 dogfood narrative in GAPS, the 2026-10-11 PHONE/GO-LIVE snapshots,
older run-inventory gate prose and other historical documents retain their
original dates, source heads and claims. No old PASS is promoted to this head.
References to untracked `.scratch` logs are **unverified historical reports**
unless the actual receipt is retained and bound to its original source. No
physical/provider check was run, no production endpoint contacted, and no
implementation work is added by this refresh.
