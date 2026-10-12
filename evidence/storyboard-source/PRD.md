# Wingthing product requirements

11 October 2026 · Reviewable product proposal · Bryan first, then external users.

Bare `wt` is the main entrypoint for daily work and guided first run; `wt claude` remains an advanced shortcut. Bare `wt` brings his sessions on EhrlichMac, forge and other saved machines into one fast navigator. Bryan’s iPhone can read, steer and stop a paired session; Wingthing Pro adds reach from anywhere, private previews, identity sharing, SSH jump access, and hosted Firecracker agents with persistent workspaces.

The binding contract is [inputs/wingthing-app-spec.md](inputs/wingthing-app-spec.md). Product packaging and authority come from [inputs/wingthing.md](inputs/wingthing.md). Pairing follows [inputs/wingthing-pairing-spec.md](inputs/wingthing-pairing-spec.md) and [inputs/PROTOCOL.md](inputs/PROTOCOL.md). The [13-page storyboard](storyboard/index.html) has 100 scenario cards. [SOURCE-AUDIT.md](SOURCE-AUDIT.md) distinguishes carrier behavior, target requirements and unverified acceptance. “Planned” describes a target, not a shipped feature. Prices and service policies remain proposals until approved.

## Goals and users

1. Bryan can start Claude, return to any session, and reach his forge coordinator without thinking about transport or daemon lifecycle.
2. A developer with several agents can find blocked work quickly, inspect progress, steer one exact execution, detach safely, and recover durable results after a client failure.
3. A phone user can pair a specific wing without an account, control only its sessions, and revoke a lost phone on each wing immediately.
4. A paying user understands the complete bill before launching cloud work and can obtain a PR, artifacts or private preview while their laptop sleeps.
5. Alice can inspect exactly the resource Bryan shared and lose live access promptly after revocation.
6. New external users reach their first useful session before being asked to buy anything. Distribution, support and observed friction outrank VM-density optimization.

Primary personas: Bryan on EhrlichMac and forge; an individual developer managing local/SSH coding agents; Bryan on his iPhone. Secondary: a Pro cloud-workspace owner and Alice as an invited reviewer. Organization admins and SSO teams are later.

A **wing** is the daemon hosting sessions; a **roost** is the machine used for work. UI calls machines EhrlichMac, forge and cloud-1. This friendly meaning does not redefine the carrier’s `wt roost` command, which starts relay plus wing. Machine/session names are labels; pinned wing and execution IDs are authorization/routing identities.

## Packaging and boundaries

| Free, local first | Wingthing Pro, one subscription | Later |
|---|---|---|
| Install, local wt/Claude/Codex, session navigation and run inspection | E2E relay reach to registered roosts | Organizations and seat billing |
| Saved OpenSSH profiles and remote attachment using the user’s SSH credentials | `wt publish PORT` private URLs and identity grants | Okta/Entra/Google Workspace SSO |
| LAN and tailnet phone pairing/control without Wingthing login | `wt ssh ROOST` through the branded jump route | Shared organization roosts and VMs |
| Independent pairing/revocation per wing | Firecracker cloud agents, persistent workspaces, paid compute | Advanced packing and margin optimization |

Customers see Wingthing throughout; underlying tunn infrastructure is never another product they must visit. Off-Wi-Fi access through an existing tailnet is free. Paid relay access requires owner registration and entitlement; a phone never logs into Wingthing or acquires account authority. No upgrade may silently widen device, repository or filesystem grants.

The architecture, pricing and PM-question files arrived and were read in full on 2026-10-11. Their findings are incorporated in flows 7, 10 and 11, this PRD and the build plan. No lane placeholders remain. Approval is not inferred from a research recommendation; proposed commercial terms still need Bryan’s decision.

## Cloud and pricing proposal adopted from the VM lane

Read [vm/VM-ARCHITECTURE.md](vm/VM-ARCHITECTURE.md), [vm/PRICING.md](vm/PRICING.md) and [vm/OPEN-QUESTIONS.md](vm/OPEN-QUESTIONS.md) in full on 2026-10-11. Offer and policy values below are proposals, visibly labeled in the storyboard. The adopted source hashes are recorded in [tools/vm_decisions.json](tools/vm_decisions.json).

| Decision | Architecture recommendation / PM proposal |
|---|---|
| MVP provider | Buy Fly Machines / Firecracker; separate execution organization and per-workspace private network in ewr; self-hosted fleet only after measured demand/margin |
| Initial sizes | Standard: 2 shared vCPUs / 4 GB; Large: 2 shared vCPUs / 8 GB, 2× credit burn and $0.70/h overage; one active VM per account. Small/performance later |
| Pro offer | $29/month; 20 Standard hours (72,000 credit-seconds); no rollover |
| Additional compute | $0.35 per ready-running Standard hour / $0.70 per Large hour, billed by seconds; overage disabled by default, opt-in hard cap |
| Outbound network | 100 GB/account-month included, then $0.08/GB only with explicit opt-in and cap |
| Persistent workspace | One 10 GB /workspace volume included while Pro active; extra volumes/storage deferred |
| Model usage | BYO API key for MVP, paid directly to provider; later billed mode proposed at upstream token cost × 1.20, separate from compute |
| Idle / run limits | Stop after 15 idle minutes; warn 2 minutes before. Default finite run ceiling 2h; up to 24h only with reservation |
| Stop / snapshot | Standard checkpoints files and stops, then cold resumes. Optional ≤2 GB memory suspend only after validation; no guaranteed memory restore |
| Post-Pro retention | 14-day export-only grace with exact deadline; provider snapshots 5-day window, off-host backups expire within 7 days |
| Charging / liability | Customer compute begins at verified ready; pre-ready/failure COGS absorbed. Final charge does not exceed accepted cap even if provider stop fails |
| Economics | Infrastructure estimate ~$0.0353/h running compute; ~$0.212 per active hour at 20h/month with retained-workspace assumptions. Excludes inference, payment, common Pro reserve and labor; modeled $29/20h contribution is 75.6% before support/engineering and other exclusions |

These are lane planning assumptions and product recommendations, not an approved price or SLA. [Fly pricing](https://docs.fly.io/about/pricing/) and [suspend documentation](https://docs.fly.io/reference/suspend-resume/) were spot-checked on 2026-10-11; full alternative/vendor research and cost formulas are in the lane documents. Published ready-time promises require a real-workload pilot.

## Flows and functional requirements

| Flow | Required user behavior | Carrier boundary / target |
|---|---|---|
| [01 Install](storyboard/01-install.html) | Install with `curl wingthing.ai/install.sh \| sh`; bare `wt` shows empty navigator; explain wing/roost in one sentence. Repair installer/PATH errors without an account. | Bare interactive navigator implemented; noninteractive bare wt shows help. Installer diagnostics are planned. |
| [02 Start](storyboard/02-start.html) | Cold `wt claude` starts one main wing and attaches. Explicit cwd, scoped dashboard new parent and generic Codex egg. | Cold start implemented; `wt claude --cwd` and scoped-parent dashboard parity planned. `wt egg codex` is the supported interactive syntax. |
| [03 Navigator](storyboard/03-navigator.html) | Group/filter all machines/projects; attach/detach; blocked/unread; native mouse/scroll; recent-run recovery. | Real keyboard bindings preserved. Blocked/failed exists; unread and recent-run UI planned. |
| [04 Machines](storyboard/04-machines.html) | Save forge SSH profile, pin verified wing, route exact session, show offline/old-version states. | Existing free SSH path; remote install, provider auth and workspaces are prerequisites. |
| [05 Phone](storyboard/05-phone.html) | QR, six-word fallback with endpoint/identity, five-minute expiry, two-sided emoji, independent wing grant, session list/input/stop/attention, immediate revoke. | Protocol target planned in this carrier; original approved storyboard reused. LAN → tailnet → entitled relay. |
| [06 Runs](storyboard/06-runs.html) | Task submission, durable modern run receipts, phase/policy/next action, sandbox review, offline inspection. | `wt run` legacy tasks distinct from `agent_run`; planner/inspector do not prove live readiness or resume work. |
| [07 Upgrade](storyboard/07-upgrade.html) | Four contextual upsells; full price; Stripe; instant no-relogin entitlement; receipts; management/cancellation; downgrade. | All paid target UX planned; preserve unpaid drafts and free work. |
| [08 Reach](storyboard/08-reach.html) | Owner registers once, phone uses pinned E2E relay; route/latency visible; failures preserve input uncertainty. | Pro relay target; no route fallback after identity failure, no automatic input replay. |
| [09 Share](storyboard/09-share.html) | `wt publish 3000`, owner-only URL, Alice Google/GitHub identity, resource-specific revoke, `wt ssh forge`. | Planned Pro commands. Existing saved SSH attachment stays free. Preview confidentiality differs from phone E2E. |
| [10 Cloud](storyboard/10-cloud.html) | Terminal/dashboard/phone intent; size/credits/cap; boot; persistent workspace; repo/secrets/model/network/tools; attach; meter; idle/snapshot; results; teardown/export. | Full planned lifecycle; VM architecture/pricing owned by parallel lane. |
| [11 Billing](storyboard/11-billing.html) | Usage/limits/invoices; failed card; notification preferences; user-reviewed safe support report. | Planned owner-account management, never phone-session authority. |
| [12 Teams](storyboard/12-teams.html) | One later card for org SSO, explicit shared roost/VM grants, denial and revocation. | Deferred; no individual Pro-to-team seat promise. |

### Daily work and multi-machine safety

FR-01. `wt claude` may start an absent wing for the implicit owner after validating selected configuration. A policy rejection, named-client rejection or live-but-unready daemon is authoritative; do not restart or replace it. Parallel cold starts share one lifecycle-locked daemon. Cancellation of a waiting viewer cannot kill accepted work.

FR-02. Implement `wt claude --cwd` through the same parent/MCP route, resolving canonical cwd on the execution machine. Current workaround is `cd` then `wt claude`. Dashboard `n` must preserve selected machine/project and distinguish generic interactive egg from scoped parent orchestration. No inferred remote cwd sync.

FR-03. Preserve actual navigator keys: arrows/j/k, Enter attach, n new, r rename, x stop, / filter, ! attention, m machines, i details, ? help, R refresh, q/Ctrl-C quit. Stop is a qualified-machine/session confirmation; Enter/Escape cancel it. Ctrl+B then q detaches; Ctrl+B twice passes Ctrl+B. Provider terminal mouse and scroll remain native; do not invent navigator mouse controls.

FR-04. Keep local inventory usable during remote failure. Last successful rows stay explicitly stale; unknown liveness is not exited. Plain `wt attach NAME` remains local, while `forge:SESSION_ID` resolves only the saved forge profile. Never select a same-named session on another machine after routing failure. Host-key or pinned-wing changes fail closed.

FR-05. Unread uses a per-viewer durable read cursor, distinct from blocked/failed lifecycle attention. Recent-run discovery uses authorized durable inventory and retains exact wing/run/session IDs. Pagination or corrupt metadata failure means incomplete discovery. Do not relaunch, infer absence, reconstruct expired output, or claim cleanup from completion. Legacy task, native turn, provider process and semantic task completion remain separate concepts.

FR-06. Show launch phase, effective policy and actionable next step. `wt doctor launch-plan` remains an explicit local-only read-only diagnosis; report success is not launch success. `REQUIRED-UNRUN` evidence is neither a pass nor a live-readiness claim. `wt run-inspect` requires a hash-verified completed snapshot, never ambient state; no capture/restore/replay is added to that command.

### Phone pairing and privacy

FR-07. `wt pair` contacts the main running wing’s authenticated local control endpoint. It never starts a second wing with a new identity. Invitation deadline is creation + 300 seconds; secrets are single-use, memory-only and invalidated on cancel, wrong proof, timeout, success or terminal exit. A fresh Enter creates a new invitation.

FR-08. Use the exact reviewed BoringSSL SPAKE2 integration and protocol transcript. Compare full scanned pin before continuation; mismatch hard-stops with no Continue action. Both phone approval and terminal Enter are required after matching four emoji. Manual fallback requires address and public identity plus six words, not a code-only locator. Review cryptography before public security claims.

FR-09. Each pairing has a phone P-256 key and opaque wing-bound credential in device-only Keychain; use Secure Enclave where supported. Wing persists only credential hash and device registry atomically before success. Dropped final pairing replies can retrieve the original encrypted success in the remaining invitation lifetime; duplicate confirm cannot create another grant.

FR-10. Device RPCs remain `sessions`, `attach`, `read`, `input`, `stop`, `detach`, `close`. No configuration, enrollment, billing, vault, file browsing, SSH launch, another wing or VM-management rights. Each connection authenticates challenge possession and checks sequence/replay/wing binding. Reconnect does not replay input.

FR-11. Prefer LAN, then tailnet, then registered/entitled relay. Extend invitation endpoint schema and safe validation for tailnet IP/MagicDNS without allowing arbitrary HTTP, redirects, cookies or pin bypass. LAN/tailnet require no Wingthing account. Relay needs owner `wt login` once, not phone login. A revoked device is denied on every route.

FR-12. `wt devices` and `wt devices revoke` operate on the same wing registry. Persist revocation and close streams before success, under the command admission lock. Ambiguous names require device ID. Revoke separately on EhrlichMac, forge and cloud-1. No remote-erasure promise for cached lost-phone content.

FR-13. Consent to notifications and generic lock-screen content by default. Push payloads must exclude session names, transcript, commands and secrets; background iOS delivery design is a launch question, not a proven LAN-only feature. Camera/local-network denial has a Settings/rescan/manual-entry path. Physical iPhone camera, Keychain, cellular, stop and revoke are mandatory acceptance gates; simulators do not substitute. App Store distribution waits on the Apple correspondence in the product profile.

### Paid access, identity and billing

FR-14. Upsells appear only at relay-needed off-Wi-Fi use, publishing, cloud intent and sharing with Alice. Free local and tailnet routes remain available beside the offer. Preserve exact action drafts; payment cannot publish, launch or grant access by itself.

FR-15. Checkout is account-owner scoped. Only verified, deduplicated server events activate entitlement; success redirects do not. Existing registered clients refresh a versioned entitlement without re-login. Target activation within ten seconds after webhook confirmation, with pending/retry states and receipts. Owner dogfood uses the planned audited admin step: Accounts → verified Bryan account → Entitlements → Wingthing Pro → Owner grant → Save. There is no shipped owner CLI; build and audit this step before relying on it.

FR-16. Proposed cancellation stops renewal and retains Pro through the paid period. At effective expiry, close relay/publish/share/SSH paid routes and block cloud starts; stop billable cloud compute according to disclosed terms. Keep local processes and valid LAN/tailnet grants. Retained cloud data/export deadlines and outstanding usage are separately disclosed; deletion cannot be a silent side effect. Payment failure has a distinct dunning deadline, not an invented grace period.

FR-17. Private publish defaults to owner-only on an exact roost/port/resource ID. Alice authenticates a verified Google/GitHub identity for that resource. Preview access never grants terminal access; observer/controller session roles are separate. Revocation closes active streams and denies future requests. `wt ssh` needs an explicit OS-principal grant and is broader than session control. Do not claim phone relay E2E hides HTTP preview plaintext from a terminating identity gateway.

### Cloud workspace and lifecycle

FR-18. Terminal, dashboard and phone can initiate a reviewable cloud draft. The proposed `wt run --cloud <workspace>` syntax is visibly planned. MVP phone initiation uses existing paired-coordinator input against an owner-preauthorized bounded template; no direct VM RPC or new phone account login. Without that template, preserve the draft for Mac approval. Pair cloud-1 independently for direct phone attachment.

FR-19. A launch review names workspace, repo, branch, agent, model billing mode, allowed secret names, network, region/size, credits, estimate and cap. Reserve account-wide budget atomically before allocation, including concurrent requests, in-flight consumption and a shutdown margin. No charge or VM allocation merely from editing a draft. Idempotent allocation and reconciliation cannot duplicate VMs after a lost response.

FR-20. Lifecycle includes draft, authorized/reserved, provisioning, booting, agent-starting, ready/running, idle, suspending/stopping, suspended/stopped, resuming, failed, exporting, deleting and deleted, with durable identity and timestamps. “Ready” requires control-plane health and native provider evidence. Failures preserve workspace and receipts and reconcile orphan compute/volumes.

FR-21. Persistent volume contains repo, edits, project dependencies and chosen artifacts; document exact mount, size, storage billing and retention. Root filesystem/system package persistence is separate. No shared-volume concurrency promise. Snapshot eligibility, cold boot versus memory restore, credential scrubbing, incompatible restore and region migration are explicit, based on VM findings.

FR-22. GitHub App installations are selected-repo scoped, including private repos. Use short-lived per-repo tokens via a broker; no stored PAT, copied Mac SSH key, token in Git remote URL, image or snapshot. Clone/create branch/push approved branch/open PR under least privilege; no default-branch push by default. Revoke immediately on repo removal, app uninstall or workspace grant loss.

FR-23. Secrets are per-workspace encrypted vault entries. Agent UI lists names/availability only. Runtime injection is purpose-bound and excluded from prompts, transcript records, logs and image/snapshot layers. Sensitive operations need a separate broker/runner with controlled output before transcript persistence. Generic redaction alone is not acceptance; arbitrary approved code can encode/exfiltrate values. Scope network and secret access tightly and disclose that granting code access to a secret lets that code use it. Decide whether the agent ever receives values directly; otherwise implement operation-only access.

FR-24. BYO Claude/OpenAI key is distinct from Wingthing-billed inference. BYO usage is billed by the model provider and outside the Wingthing monetary cap; guide provider-side limits. Wingthing-billed mode requires provider policy approval, price, per-request metering and a model-specific cap before launch. Never promise desktop consumer subscription credentials work on hosted agents.

FR-25. Default egress includes selected package registries, GitHub and selected model APIs, with explicit owner toggles for additional destinations. Protected supervisor proxy/firewall policy must block unprivileged-task bypass through direct IP, IPv6, alternate DNS or redirects; no inbound except authorized relay routes. Private published previews do not create raw public VM ports. Abuse controls cover mining/spam and invalid budgets without collecting transcript.

FR-26. Versioned toolchain image includes wt, compatible agent CLIs and common toolchains. Adding dependencies is allowed through approved registries; OS package or image changes require explicit scope and persistence explanation. Native sandbox/platform compatibility is tested, not inferred from container fixtures.

FR-27. Live cost separates elapsed compute, credits, reserved budget, overage, storage and model mode. Provider-confirmed billable intervals enter an idempotent ledger; Stripe receives finalized usage, never controls live cap enforcement. Delayed meter values are estimates. Idle policy uses actual provider/task activity, not viewer presence; blocked work cannot burn indefinitely. Notify and stop before cap, rotate credentials on resume, and never replay uncertain prompts.

FR-28. Results include durable PR reference, downloadable artifacts and a private `wt publish` preview. Preview is unavailable when compute stops. Teardown separates VM stop, disk retention, export, deletion and backup purge. Exports contain repo/edits/artifacts and exclude vault values and device credentials. Show actual deadline before retention expiry and delete only under approved lifecycle/retention policy.

### Cloud release safeguards from the VM lane

FR-32. Use a separate Fly execution organization and per-workspace custom private network; validate provider quotas before launch. Remove the carrier’s internal private-IP authentication fallback and require explicit service credentials. Cloud mode requires functioning cgroup v2 limits instead of silent rlimit fallback. Supervisor/proxy/firewall configuration is outside the unprivileged task’s authority; a guest-root/kernel compromise can bypass guest nftables, so provider-level internet fencing remains a validation gate. Do not grant arbitrary root or claim that local sandbox flexibility already establishes cloud isolation.

FR-33. One active VM and one included 10 GB workspace per Pro account for MVP. Cloud runs have a finite two-hour default ceiling, optionally up to 24 hours with budget. Budget alerts at 80%/95% precede admission/stop enforcement. Customer pays no more than the accepted Wingthing cap; exceptional provider/model overrun is absorbed by Wingthing. Included outbound traffic is 100 GB/month; additional $0.08/GB is explicitly opted-in/capped and may stop preview/egress at exhaustion. Stripe export uses Billing meter events from the finalized ledger, not the removed legacy usage-record integration. Cloud attachment uses a stable protected workspace identity or requires explicit re-pairing after identity replacement; a restored snapshot must not resurrect revoked credentials or grants.

### Cross-cutting requirements

FR-29. Every flow has failures, edges, who-sees-what and accurate Free/Pro/planned labels. Visual reference styling and card structure are reused, especially flow 5. Storyboard pages are self-contained, work from disk, issue no external requests, support dark/light, keyboard focus and phone width; print/no-JS reveal all cards.

FR-30. Product targets: WCAG 2.2 AA, VoiceOver and uncapped Dynamic Type on iOS; no color-only status; visible focus; readable touch targets and 320 CSS-pixel width without document overflow. Keep keyboard out of phone composer controls and preserve selection/drafts through reconnect. Browser validation of this visual spec does not establish native app accessibility acceptance.

FR-31. Relay confidentiality is route-specific: phone session content encrypted to pinned wing; hosted operator can access guest/source/storage; model provider sees prompts; preview gateway may see HTTP; Stripe sees billing data. Surface these boundaries per flow. Diagnostics/telemetry exclude prompts, transcripts, source, secrets, raw argv, QR secret, keys and full paths by default.

## Non-goals and release sequence

No public anonymous previews, automatic cross-machine workspace sync, automatic prompt replay, account-wide authority from phone pairing, global zero-knowledge claims, generic SSH shells from phone session grants, indefinite free cloud compute, secret safety via regex alone, team seats/SSO MVP, or release/tag/deploy as part of this storyboard task. No self-hosted Firecracker packing work before validated user demand. Provider-managed sandbox alternatives and later bare-metal margin work belong in the VM recommendation, not the daily-user critical path.

Release sequence: first make Bryan’s Mac workflow and forge coordinator reliable; then physical-phone local pairing/revoke; recruit first external users and record activation/support evidence; introduce paid relay/private sharing with entitlement; ship a capped BYO-key cloud pilot and measured outcome; expand model billing and teams after demand and security/billing acceptance. [BUILD-PLAN.md](BUILD-PLAN.md) defines executable tasks and release gates.

## Success metrics

Metrics below are proposed product targets, not measured results. Gather local consented counters or cohort summaries; do not record transcript, repo names, full paths or credentials. Dogfood and external-user cohorts are separate.

| Metric | Definition and denominator | Initial target / window |
|---|---|---|
| First-session activation | New installs completing one admitted provider session and a successful detach/reattach within 24h / observable consenting new installs | ≥70% during pilot; report unknown denominator when install telemetry unavailable |
| Time to first useful session | Install-complete to first successful provider interaction, excluding explicit provider-account onboarding and reporting it separately | Median ≤5 minutes, p90 ≤15 minutes |
| Daily-driver reliability | Successful wt claude launches/attaches / explicit attempts; policy denials classified separately | ≥99% across Bryan’s first 100 attempts; zero duplicate unintended launches |
| Multi-machine activation | Users adding forge/another saved machine and successfully attaching once within seven days / users attempting machine add | ≥80%; exact-route and identity failures separately reported |
| Phone activation | Invitation starts reaching confirmed pairing and one read + explicit input + confirmed stop / invitation starts by same physical test device | ≥90% in pilot; zero wrong-wing grants/replays |
| Pro conversion | Free active users subscribing within 30 days of their first contextual paid intent / unique eligible users with that intent | ≥10%; report by four trigger types and net retained conversion after refunds |
| Checkout / entitlement | Completed verified subscriptions / checkout starts; time webhook-confirmed to usable entitled route | ≥60% completion; entitlement p95 ≤10 seconds |
| Cloud activation | First cloud launch reaching ready and a PR/artifact/preview result within seven days / users attempting first paid cloud launch | ≥80%; classify repo, model, capacity and task-quality failures |
| Budget accuracy | Final billed amount within consented cap; duplicated VM allocations, ledger intervals and charges | Zero cap breaches/duplicate charges; estimated vs final delta reported |
| Week-one retention | Activated users using a session on ≥2 distinct days in days 2–7 / activated users observed for seven days | ≥50% in pilot |
| Four-week retention | Activated users with any meaningful session in days 22–28 / activated users with a complete 28-day window | ≥35%; compare phone/Pro/cloud cohorts |
| Paid retention | Pro subscribers renewing into month two / subscribers reaching first renewal, excluding incomplete cohorts | ≥80%; downgrade reasons captured with optional choice |
| Outcomes / support | User-reported useful PR/artifact and resolved blocking support issues / responses and opened blocking issues | Report weekly; median unblock ≤1 business day in staffed pilot |

Meaningful activity is an admitted provider interaction, confirmed attachment/reading, steering, or explicit stop, not background polling. Retention is cohort-based; don't count incomplete observation windows as churn. Required physical-device security failures block launch regardless of percentage targets. Cost metrics include unit cost, margin, credits, provider invoices and storage/egress, using the VM lane's proposal with measured pilot inputs before expansion.

## Open questions for Bryan

1. Approve the parallel lane’s Pro price, compute credits, rates, overage default and account cap. Should overage be opt-in only? Which storage/egress terms appear at launch? The lane proposes $29/month, 20 Standard hours, $0.35/h Standard / $0.70/h Large opt-in overage and 100 GB outbound then $0.08/GB; confirm the complete offer.
2. Ship BYO API keys first, with Wingthing-billed inference later? Which providers and subscription/auth modes have approved hosted-agent use?
3. Approve the provider MVP, smallest practical agent size, idle threshold, memory snapshot availability/TTL, retention after stop/downgrade and export/delete/backup policy from the VM lane.
4. Is sending a bounded cloud request to an existing paired coordinator the acceptable phone-only MVP? Direct phone launch would need separately approved authority and cannot silently reuse pairing for VM/billing access.
5. Require operation-only secret access through a broker, or allow secret-bearing agent tools with explicit risk disclosure? “Never in transcript” needs an enforceable execution/output boundary, not only redaction.
6. Confirm the audited owner-entitlement admin step and which account ID is Bryan’s; define how operators recover it without broad backdoor grants.
7. What is Apple mailing, and who completes enrollment/address verification, physical iPhone camera/Keychain/cellular/VoiceOver tests, signing and TestFlight/App Store rollout?
8. Which background attention notifications should be shipped first, and what delivery service is allowed for local-only users? What names/content can be on the lock screen?
9. What payment-failure grace, refunds, cap-failure liability, support staffing and invoice retention are promised to first customers?
10. Does MVP session sharing include observer only, with controller later? Confirm preview HTTP gateway visibility and initial recipient identity providers.

11. Should cloud-1 retain a protected stable wing pin and device registry across stopped-VM replacement, or require explicit re-pairing when bootstrap creates a fresh identity? Restore must never revive revoked grants.

The specific lane questions are available in [vm/OPEN-QUESTIONS.md](vm/OPEN-QUESTIONS.md); unresolved product decisions stay explicitly open, not silently finalized by mock copy.

## PM reconciliation — tonight’s binding addendum

The newest synced spec is `/work/lanes/skeptic/inputs/mac/drive/wingthing-app-spec.md` (copied in `/work/lanes/pm-pass/evidence/wingthing-spec.md`). Its 17:50 addendum supersedes earlier wt-claude-first entry wording: every flow starts at bare `wt`; other commands are shortcuts. No-agent onboarding detects locally, presents exact vendor installers with explicit consent, verifies, hands off to native provider login, and opens the first admitted session. Existing-agent users may add more without blocking.

The header names the actual connection; initial Mac text is “Local only: this Mac + phones paired on your network.” Help explains free saved SSH and LAN/tailnet, paid relay/cloud, and external model/installer traffic. A route change updates the header; local-only does not imply offline inference.

All contextual Pro moments are implemented dormant under fleet-7zpb: rollout flag OFF by default, separate from Pro entitlement. OFF means no automatic upgrade prompt, even at a phone/publish/cloud/share failure. Flag-enabled moments are dismissible, once per moment and nonblocking, with original draft and free alternatives preserved. Enabled checkout acceptance follows WT-17/18; dormant UX never waits for commercial terms. These are planned requirements, not shipped behavior.

First-session counts and severity findings: `/work/lanes/pm-pass/PM-REVIEW.md`; consolidated owner decisions: `/work/lanes/pm-pass/DECISIONS.md`. Free onboarding does not depend on those paid decisions.
