# Wingthing on Bryan's iPhone — 2026-10-11

For the **actual native iOS app**, start with [IOS-INSTALL.md](IOS-INSTALL.md).
Its installer handles signing/install/launch and the hosted Home setup when
Bryan runs it. The Safari instructions below remain a separate access path.

Baseline: this isolated clone, `2c3688f`, branch
`codex/wingthing-phone-20261011`. No push, deployment, public exposure, Apple
signing, production mutation, or real agent control is authorized or performed.

## What works today

| Path | Implemented | Missing proof / limitations |
| --- | --- | --- |
| **Installable mobile web coordinator** (`web/`) | `/app/` standalone manifest, 180/192/512 px icons, viewport handling, parent/child agent inventory, lifecycle status, native transcript, persisted drafts, explicit uncertain-input recovery, `session_prompt` steering and continuation, confirmed `pty.kill` Stop execution. Uses the existing encrypted relay control APIs. | No signed native binary needed. Requires an awake Mac/forge, an existing reachable local roost with conversation-enabled wing and authenticated Claude. Loopback-only roost cannot be forwarded directly: it rejects external Host and Origin. Authenticated phone gateway, touch polish, install/sign-out help and foreground read refresh are implemented in this branch. No offline control, push/background delivery or generic durable `agent_run` UI. Stop acknowledgement is distinct from observed process exit and does not cancel external effects. Physical Safari/keyboard/home-screen and live tunn WebSocket acceptance remain coordinator gates. |
| **Native iOS** (`mobile/ios/`) | SwiftUI iOS 16+ conversation client, pinned HTTPS Home identity, bearer verification, encrypted relay transport, Keychain credential storage, inventory/transcript, creation/follow-up/recovery and a capability-bound exact Stop adapter; development signing/install Make targets. | No demonstrated signed install on Bryan's phone. No native tunn OAuth/cookie login or second gateway credential header. No local-only Unix socket/SSH MCP adapter, generic runs/tasks UI, passkeys or push. Main lacks the native `conversation_stop` / `wingthing.exact_stop.v1` server contract: native Stop remains unavailable. Historical simulator/unsigned builds do not prove this checkout. |
| **Android** | This checkout tracks only Go wire-contract generation and a tunnel test vector in `android/contract/`; no `mobile/android` app exists. | No APK or runnable native app here. The earlier recovery branch contains an uncompiled Compose draft with pinned Gradle/JDK/SDK dependencies missing and debug signing disabled. It is not an installable phone app. The web route can also run on Android. |

## Fastest path: Safari home-screen app through private tunn

Use this Mac's existing authorized **local roost**, the new loopback phone
gateway, then a tunn HTTPS endpoint restricted to Bryan's exact verified email.
Sign in through tunn's GitHub OAuth, enter the independently generated phone
access key, open a coordinator, and choose Safari → Share → Add to Home Screen.
Budget 5 minutes for local setup/build, 5 for tunn login and gateway, 5 for
phone login/install and 5 for a real read/steer/stop check: **under 30 minutes of
Bryan time if his Claude login, local wing and tunn account are already ready**.
First-time provider/account setup or native signing is outside that budget.
Exact commands and acceptance checks are in [GO-LIVE.md](GO-LIVE.md).

No Wingthing hosted account/subscription is required for a self-hosted local
roost. Forward the gateway, never the bare roost or `control.sock`. A wing that
only runs `--local-only` has no web relay: it needs the explicitly started local
roost path in GO-LIVE. Do not replace or unlock Bryan's existing wing.

## Authentication, authority and threats

1. **tunn gate:** exact-email allow-list plus GitHub OAuth/verified identity;
   enable GitHub 2FA/passkeys. Use no domain wildcard, public endpoint, shared
   agent grant or token-in-URL. tunn browser cookies authenticate both HTTP and
   WebSocket upgrades. MCP OAuth grants alone do not log Safari into this UI.
2. **Independent gateway gate:** a private file containing a random 256-bit
   access key, entered once through a password form over HTTPS. The key is never
   put in a URL or log. A short-lived, random, Secure/HttpOnly/SameSite=Strict
   host-only session cookie guards all app/API/WebSocket requests. Restarting
   the gateway invalidates every session; rotate its key file to revoke enrollment.
3. **Origin boundary:** the gateway pins one exact HTTPS origin, rejects other
   Host headers, rejects cross-origin mutations and WebSocket upgrades, and
   translates only authenticated requests to loopback. The roost retains its
   existing loopback/DNS-rebinding checks; forwarded headers are not credentials.
4. **Wing authority:** the gateway maps to the existing local roost's single
   owner. Anyone with both gates can exercise that owner's allowed wing control,
   including executing provider/tool code and reading allowed machine files.
   This is full owner control, not a read-only dashboard or a per-agent grant.
   Existing wing path, provider, sandbox and passkey/lock policy still applies.

Trust Bryan's Mac OS account, phone/browser, Wingthing assets and tunn operator.
tunn terminates HTTPS and can read/modify the JavaScript and login request;
encrypted browser-to-wing payloads do not protect against a malicious served
client. A compromised Mac account already has local control; the gateway is not
an OS-user isolation boundary. Lost phones, stolen OAuth sessions, copied access
keys, malicious links, cross-site WebSocket requests and DNS rebinding are the
main remote threats. The independent gateway gate limits an OAuth-only theft;
strict origin checks and no-store responses limit browser attacks. Browser
transcripts/drafts may persist locally and are sensitive. Control requires live
network access; do not cache authenticated APIs in a service worker.

Revoke immediately by stopping **both the tunn publisher and gateway**. Stop
the gateway first if compromise is suspected: its proxy closes active sockets.
Rotate the access-key file and restart with a new origin or key; all old gateway
sessions fail. Restarting only tunn is not gateway-session revocation. To remove
only this phone, use the gateway's sign-out page and clear the site's Safari
data; for a lost/stolen phone revoke all gateway sessions. Deleting the home-screen
icon alone does not revoke credentials. See GO-LIVE for exact commands.

## Prior work and native handoff

Read `wingthing-mobile-20261010/MOBILE-PLAN.md` and both requested clones' logs.
Neither older clone has a local `main` ref or the new `2c3688f` object; their
committed changes were compared to their common baseline `92b1183` and this
checkout's files. Reuse the next clone's clone-local iOS validation helper and
small bounded-history fix where compatible. The e2e clone's `a591bfc`/`e455e45`
exact native Stop server and `1e3edc4` encrypted Go-peer fixture are useful
unmerged candidates, not live acceptance. Do not blindly cherry-pick their older
server/storage changes over evolved main just to claim native readiness.

For native iOS Bryan opens `mobile/ios/Wingthing.xcodeproj` with full Xcode,
adds his Apple ID under Settings → Accounts, selects his Team and a unique
bundle ID under Signing & Capabilities, connects/unlocks/trusts the iPhone,
enables Developer Mode, selects its UDID under Devices and Simulators, and
presses Run. The exact CLI equivalent and remaining protocol gap are in
GO-LIVE. Personal Team provisioning needs periodic renewal. These actions are
Bryan's; no Apple authentication or provisioning is performed here.

## Verification

This sandbox cannot use loopback or Chromium. The in-process Go gateway race tests and CLI rejection test passed; all **197 Node tests**, Vite build and the final embedded Go build passed. Coordinator must run the real loopback gateway/roost
WebSocket test, Chromium phone-layout journey, tunn unauthenticated/authorized
checks, and physical iPhone Add to Home Screen/keyboard/steer/stop acceptance.
The integration WebSocket gate compiles here but requires loopback to run. Native core validation failed before compilation with `sandbox_apply: Operation not permitted`. Exact gate commands and receipts are in GO-LIVE.
