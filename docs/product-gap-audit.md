# Product gap audit — integration update 2026-10-19

Implementation examined: `64a4d58d` (the merged implementation before this documentation update).
This source review supersedes the dated inventory below wherever the merged
implementation changes its claims. Source coverage is distinct from runtime
acceptance: synthetic fixtures do not prove live providers, physical devices,
installed releases or hosted production. Proposed owners remain responsibility
areas, not assignments. No implementation scope is added by this update.

| Item | Current evidence and disposition | Remaining check / proposed owner |
| --- | --- | --- |
| WT-001 — Send | **Changed, source-closed handoff defect:** [paper.js](../web/src/paper.js) submits through [conversation-recovery.js](../web/src/conversation-recovery.js), retaining exact input/request identity and showing Delivered only after a matching native receipt. | Exact-once Claude/Codex fixtures cover retry/empty/duplicate cases; real providers remain unverified. **Browser messaging**. |
| WT-002 — Codex reader | **Changed, implemented contract:** [chat.js](../web/src/chat.js), [conversation-state.js](../web/src/conversation-state.js) and [native fixtures](../web/test/fixtures/README.md) cover ordered text/tool/completion, replay, unknown variants and exact hook readiness for both providers. | Live provider-version compatibility remains unverified. **Browser messaging + provider adapters**. |
| WT-003 — direct-free onboarding | **Changed, implemented guidance:** [onboarding.js](../web/src/onboarding.js) and [paper.js](../web/src/paper.js) explain native access and self-hosting; ineligible routes avoid runtime reads/controls. | Real account/provider installation remains unverified. **Browser onboarding**. |
| WT-004 — phone route | **Changed:** [native enrollment](../IOS-INSTALL.md), [inventory-only pairing](pairing/BROWSER.md) and [signed tunn access](pwa.md) remain separate contracts. | Choose default and run full physical-phone control journey. **Phone experience + access documentation; owner selects default**. |
| WT-005 — local MCP startup | **Open:** [README](../README.md) requires a running wing; [install page](../internal/relay/templates/install.html) and [installer template](../internal/downloads/installer.sh.tmpl) retain registration/startup guidance to reconcile. | Fresh-HOME install and stopped-wing diagnosis. **Install/onboarding**. |
| WT-006 — missing provider | **Changed, implemented diagnostic:** [server.go](../internal/egg/server.go) checks missing executable before preparation; [errors.go](../internal/control/errors.go) retains typed setup guidance across wire responses. | Real local/SSH provider installation remains unverified. **CLI diagnostics + launch control**. |
| WT-007 — empty agent list | **Changed, source-closed invented Claude:** [palette.js](../web/src/palette.js) and [paper.js](../web/src/paper.js) use detected agents, machine setup, refresh and passkey recovery. | Real installation/sign-in remains unverified. **Browser launch**. |
| WT-008 — ended controls | **Changed, partial:** [conversation-state.js](../web/src/conversation-state.js) gates input by live readiness and [paper.js](../web/src/paper.js) disables ended Send; terminal takeover still follows connected-machine availability. | Full retained history and lifecycle control contract remain open. **Browser session lifecycle**. |
| WT-009 — release verification | **Changed, implemented verification-only pipeline:** [.gitlab-ci.yml](../.gitlab-ci.yml), [release verification](release-verification.md) and [validator](../scripts/release-verify.py) bind tag/head/gate logs/checksums. | Actual GitLab Linux/macOS runner profiles and authorized release remain unverified. **Release engineering**. |
| WT-010 — access explanation | **Changed, partial:** [render.js](../web/src/render.js) and [onboarding.js](../web/src/onboarding.js) follow available Account options and distinguish provisioning. | Full selected-wing/account policy explanation remains open. **Account UX + entitlement policy**. |
| WT-011 — offline presence | **Changed, implemented contract:** [paper-state.js](../web/src/paper-state.js), [paper.js](../web/src/paper.js) and [data.js](../web/src/data.js) retain identities/last-seen and separate offline, checking and access failures. | Physical sleep/network recovery remains unverified. **Browser machine status**. |
| WT-012 — entry links | **Changed, tracked entry cleanup:** [README](../README.md), [pty.js](../web/src/pty.js), [public templates](../internal/relay/templates/home.html) and [docs tests](../internal/docscheck/docs_test.go) use GitLab; module identities remain unchanged. | Post-promotion hosted page remains unverified. **Public documentation + release engineering**. |
| WT-013 — human run results | **Open:** [run inventory](run-inventory.md) describes qualified agent recovery; [paper.js](../web/src/paper.js) still displays session inventory and transcript evidence. | Durable human run/result discovery and negative controls. **Browser supervision**. |
| WT-014 — sharing | **Changed:** [reach sharing](reach-share.md) separates dormant development fixtures from runtime authority; [shared roost](../patterns/shared-web-roost/INSTRUCTIONS.md) is the broader existing route. | Default discoverability and two-user/outsider acceptance remain unverified. **Collaboration documentation + browser access**. |
| WT-015 — human labels | **Open:** [agent_runs.go](../internal/localmcp/agent_runs.go) uses [ValidateSessionName](../internal/eggclient/local_sessions.go), retaining slug constraints. | Display/resource name separation remains future work. **Agent API contract**. |
| WT-016 — worktree preparation | **Open:** [product brief](agent-manager-product-brief.md) still distinguishes terminal composition from proposed typed workspace preparation. | Bounded idempotent primitive remains future work. **Workspace control**. |
| WT-017 — audit freshness | **Addressed as documentation:** this current source inventory supersedes the pinned snapshot below. [Prompt admission](../internal/egg/codex_prompt.go) remains separate from argv/privacy; [isolation baseline](isolation-conformance.md) and [remote mutation audit design](remote-mutation-audit.md) distinguish current gaps from proposed enforcement. | Live acceptance and implementation of decided durable auditing remain open. **Product audit maintenance + security**. |

## Historical source snapshot — implementation 014b7e5

The following 2026-10-18 report retains its original source links and dispositions
for provenance. It is historical evidence; the integration inventory above takes
precedence. Its implementation-tree statement applies only to that original
source review, not to this merged implementation.

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
