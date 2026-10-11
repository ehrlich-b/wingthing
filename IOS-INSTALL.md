# Native Wingthing on Bryan's iPhone

This installs the SwiftUI app from `mobile/ios/Wingthing.xcodeproj`, scheme
`Wingthing`, directly onto your iPhone. Apple's mailed verification and App
Store review are not prerequisites for a Personal Team development install.
The app requires iOS 16 or later.

## Run it

The requested entry point is `~/repos/claude/bin/wt-ios-install`. This agent
session could read that directory but macOS denied writes to it. Bootstrap
once with this command; the installer creates that entry point before proceeding:

```text
! /Users/ehrlich/repos/w9-wingthing-phone-20261011/mobile/ios/scripts/install_phone.sh
```

After that, including when renewing a development install:

```text
! /Users/ehrlich/repos/claude/bin/wt-ios-install
```

The script uses the full Xcode at `/Users/ehrlich/Downloads/Xcode.app` through
`DEVELOPER_DIR`. It preserves the system's `xcode-select` setting. Its numbered
steps check Xcode, select the cable-connected paired iPhone from `devicectl`
JSON, discover/cache a signing Team ID, build Release with automatic signing,
install, launch, prepare a separate hosted Mac wing, verify Home identity, and
copy the setup link privately. Repeating it reuses the team, wing, login and
workspace, and performs incremental builds. It holds an installer lock and
rejects unexpected existing wing/workspace policies.

## One-time human steps

1. Open Xcode and finish any license or platform/component installation.
   **Xcode > Settings > Accounts > + > Apple Account**: sign in yourself. Select
   your **Personal Team** while the paid membership is pending. The script reads
   provisioning metadata from Xcode preferences, without accessing Keychain or
   certificates directly. If discovery is unavailable/ambiguous, it asks once
   for the 10-character Team ID and remembers it. Select the Team under the
   Wingthing target's **Signing & Capabilities** if Xcode has not recorded it.
2. Connect the unlocked iPhone by cable. Tap **Trust This Computer** and enter
   its passcode. Check **Xcode > Window > Devices and Simulators** if it is absent.
3. On iPhone, **Settings > Privacy & Security > Developer Mode**: enable it,
   restart, and confirm. The setting may first appear after connecting to Xcode.
4. After first install, if iOS refuses launch as an untrusted developer, use
   **Settings > General > VPN & Device Management > Developer App > Trust**.
   Then rerun the installer with `--connect-only`. Approve Xcode signing/Keychain
   dialogs yourself; the agent has performed no Apple login or signing action.

If command-line provisioning fails because the new phone is unregistered,
choose your Team and phone in Xcode and **Run** once, then rerun. The CLI build
uses `-destination 'generic/platform=iOS'`, `-allowProvisioningUpdates` and
`-allowProvisioningDeviceRegistration`, with signing enabled only for that
invocation. Its default bundle ID is `dev.ehrlich.wingthing.phone.<TEAM_ID>`.
Use a bundle ID owned by your selected Team if overriding it. Keep the same
team/bundle ID to preserve the app's saved state across reinstalls.

Personal Team provisioning profiles expire seven days after issuance; rerun
the installer to renew. See Apple's [developer account overview](https://developer.apple.com/help/account/basics/about-your-developer-account)
and [Developer Mode instructions](https://developer.apple.com/documentation/xcode/enabling-developer-mode-on-a-device).

## What a Home is and where work runs

**Home** in this app is a configured HTTPS Wingthing relay origin, account ID,
executor WingID, pinned X25519 public key, and existing bearer. It is a connection
profile, not the Proxmox machine named `home`. The executor computer runs your
Claude conversations. The relay authenticates the account and forwards encrypted
control; it does not run your provider or store its login for this path.

| Possible Home | Native suitability today |
| --- | --- |
| Hosted `https://wingthing.ai` plus a Mac wing | The installer's default. Stable relay URL reachable from the phone without tunn. Requires your existing Wingthing account's hosted relay entitlement and an awake online Mac. |
| Hosted relay plus forge's wing | Same protocol, with forge doing the work. Supply a private setup-link file generated from that wing's state; the installer does not start/change forge. |
| Mac/forge loopback roost or `--local-only` wing | Not phone-reachable native Homes. HTTP loopback preview is debug-only and refers to the phone itself on a real iPhone. A local-only wing exposes a local control socket, not an HTTPS relay. |
| A user-owned HTTPS Wingthing relay | Supported with its own authorized bearer and pinned executor, using a private setup-link file. It must serve the native endpoints without browser OAuth/cookie requirements. |
| GO-LIVE's tunn + phone web gateway | Requires tunn browser login plus gateway cookies. Native has no cookie login or second credential adapter, so this endpoint cannot be used by the installer. |

The default creates **only** these separate personal resources:

- Wing state: `~/.wingthing-ios-phone-20261011`, label `bryans-native-phone`.
- Workspace: `~/phone-native-work-20261011`. Tools read `ro:/`, write only their
  workspace, and have `network: none`; normal provider authentication is separate.
- Stable wing binary: `~/.local/bin/wt-ios-phone-20261011`, built offline from this
  clone using its existing Go module cache. An installed/authenticated Claude
  CLI is needed for actual conversations.
- Private installer state, logs and derived data: `~/.local/state/wt-ios-install`.

The script calls `wt login --roost https://wingthing.ai` in this separate wing
state. A valid existing login is checked/reused; otherwise Wingthing opens the
device-authorization page in your Mac browser. Approve as your own account.
The relay issues the bearer after browser approval through `/auth/device`
and `/auth/token`; it is stored as `device_token` in that wing's private
`device_token.yaml`. The iOS app itself does not mint or refresh bearers.
This bearer carries account authority; treat its setup link as a credential.

The new wing explicitly allows hosted relay and enables conversations. It starts
unlocked with no pinned passkeys. Existing Mac/forge wing state, locks and
provider logins are preserved. Hosted account admission is separate:
`hosted_relay: allow` cannot grant a missing subscription/entitlement. If it is
denied, resolve account admission separately or use an already configured private
HTTPS Home; the script makes no billing, SQL or relay deployment changes.
See [the existing hosted-phone notes](docs/phone-coordinator.md) for admission details.

## What you will see on the phone

The app first shows **Wingthing > Connect a home**. The script creates a
`wingthing://home?v=1&…` link via `wt phone link`, then checks HTTPS `/health`,
bearer `/auth/check` account identity, and `/api/app/wings` against the exact
wing/key. The latter may wait up to 60 seconds for the wing to appear. These
checks make no conversation/control request and do not prove cellular routing
or live provider inference.

A token-free identity deep link is passed to the installed app with
`devicectl --payload-url`. The complete credential-bearing link is copied to
the Mac clipboard; it is never printed, placed in command arguments, or included
in a terminal QR. Temporary installer link files are deleted on exit.

On iPhone, tap **Home > Paste setup link**, allow paste if asked, then **Connect**.
Universal Clipboard needs the same Apple Account, Wi-Fi/Bluetooth and Handoff
enabled on both devices; see Apple's [Universal Clipboard instructions](https://support.apple.com/en-us/102430).
Importing a link only fills the form. **Connect** verifies the account and pinned
wing again, opens the encrypted WebSocket `/ws/relay?wing_id=…`, and then stores
the verified bearer in device-only, non-synchronizing Keychain storage.
Only the public identity is saved with the Home profile; tunnel private keys
stay in memory. **Disconnect** forgets the phone credential and leaves cached
conversation content readable offline.

When Home says **Online**, open a conversation, or tap **New conversation**.
For a new wing, choose `phone-native-work-20261011`, a name, an exact supported
Claude model ID, and your first message. Nothing creates/sends a conversation
until you explicitly tap **Create conversation** or **Send**. Verify one harmless
message and follow-up on a disposable conversation yourself. Installation,
HTTP checks, and synthetic tests do not establish that live provider journey.

If Universal Clipboard is unavailable, use Home's manual fields. Open the private
wing `device_token.yaml` in a local editor and transfer its bearer privately;
the file also holds its public key and, after `wt phone link`, account ID.
The wing ID is in the adjacent `wing-id` file. Enter `https://wingthing.ai`, those
identity fields and the existing bearer, then **Connect**. For another Home,
use that Home's own token and origin. Native camera QR scanning is disabled;
the app supports manual entry, custom-scheme deep links, and clipboard import.

After pasting, replace the clipboard or run:

```text
! /Users/ehrlich/repos/claude/bin/wt-ios-install --clear-clipboard
```

The managed wing is a background daemon and remains running after the installer
exits. Keep its Mac awake/online; sleep disconnects it. With this wing state
selected, `wt wing stop` stops its executor connection; that alone does not revoke
the account bearer. Phone **Disconnect** forgets the phone's copy. For a lost
phone, account-level credential revocation is a separate operator action.

## Overrides and recovery

Set overrides before the command, as needed; never put a bearer in an environment
override or command line.

| Variable / option | Purpose |
| --- | --- |
| `WT_IOS_TEAM=ABCDEFGHIJ` | Select/reset the saved signing team without a prompt. |
| `WT_IOS_DEVICE=<identifier-or-UDID>` | Select one cable-connected paired iPhone when several are present. |
| `WT_IOS_BUNDLE=dev.ehrlich.wingthing.personal` | Override the signed/installed bundle ID. |
| `WT_IOS_SETUP_LINK_FILE=/absolute/private-file` | Reuse an existing Home, including forge, with no wing login/start/policy changes. File must be owned by you, mode 600, not a symlink, and contain a valid HTTPS link including its bearer. |
| `WT_IOS_GO=/path/to/go` | Override the pinned Go 1.26.9 executable for the wing build. |
| `WT_IOS_XCODE`, `WT_IOS_CLONE`, `WT_IOS_STATE` | Override Xcode, checkout, or installer state paths. |
| `--install-only` | Sign, install and launch; skip Home login/setup. |
| `--connect-only` | Reuse the installed app and its saved bundle ID; prepare/check/import Home again. |

To prepare a forge Home link, run its own compatible `wt phone link` with its
own `WINGTHING_DIR` and redirect output to a mode-600 private file rather than
printing it. Transfer only that setup link privately to the Mac; never copy
forge's provider credentials or private encryption key. This task has made no
forge changes.

Failures print the failed stage and private log path. They never dump those logs
or a setup URL. Inspect logs locally. A failed Home setup leaves the installed
native app usable for cached reads/manual setup; rerun `--connect-only` after
fixing the cause. Changed managed wing policies are rejected for inspection.
A stale installer lock requires checking the original invocation has ended;
rerunning never terminates an unrelated process or existing wing.

## Verified here and remaining gaps

On October 11 with Xcode 26.3 at the explicit developer directory:

| Check | Result |
| --- | --- |
| Existing `offline_validation.sh core`, `release`, `xcode` | All attempted; each stops during manifest evaluation with `sandbox_apply: Operation not permitted`, before source compilation. The Xcode gate attempted simulator and unsigned device SDK builds. |
| Supplemental core and native SwiftUI typecheck | **PASS: 125 Swift Testing tests** after a small setup-link restoration guard/test fix. |
| Supplemental Release preview guard | **PASS**; Release refuses the debug-only preview executable route. |
| Supplemental generic iOS Simulator build | **PASS**, unsigned. |
| Supplemental Release physical-device SDK build | **PASS**, unsigned; cannot run on an iPhone. |
| Installer verification | **PASS: six Python tests**, including two mocked build/install/launch runs that reuse the team, device filtering, bearer/account/key binding, secret-free identity output, and no-device refusal before signing; Bash syntax and ShellCheck pass. |
| Stable wing executable | Built offline from this checkout; `phone link --help` confirmed. |
| Signed phone / hosted live inference | **Unverified**. `devicectl` reports no connected devices in this session. No Apple account, certificate or Keychain operations were performed by the agent. No hosted login, new wing connection, prompt or public tunnel was executed here. |

The supplemental commands disable only the nested SwiftPM manifest/compiler
subprocess sandbox for this dependency-free local package in this restricted
agent session. They are explicitly separate from the failed standard gates.
Their logs are in `.scratch/ios/install-validation/`; normal Terminal installer
builds retain the standard Xcode behavior. The fix adds a fence when a setup
link arrives synchronously during saved-Home restoration, and replaces a
scheduler-priority assumption in its existing test with a deterministic event.

Implemented native features include conversation inventory and transcripts,
child navigation, local drafts/history positions, encrypted reads and explicit
prompt receipt checks, Claude parent creation and advertised headless follow-up.
Actual provider availability, model support, file/sandbox admission, hosted
account entitlement and encrypted capability support are checked at runtime.

Known gaps: **whole-conversation Stop is unavailable** on this server baseline;
its native `conversation_stop` / `wingthing.exact_stop.v1` contract is absent.
Native passkey approval, pairing/sign-in, camera QR scanning, bearer refresh,
tunn/gateway-cookie transport, generic runs UI, push and background control are
not implemented. A wing requiring passkeys cannot be made compatible by
signing the app; its existing lock policy must stay intact. Cache is offline
evidence, not live status. Reconnect does not replay unresolved messages;
use the explicit receipt/retry action. Physical keyboard/VoiceOver/cellular
acceptance remains open.

## TestFlight after Apple's letter

After Apple completes enrollment and membership is active, use your paid team
and the final owned bundle ID. Add the required app icons (this foundation
currently has no asset catalog), increase the build number, and create the
matching app record in App Store Connect. Finish its metadata and encryption
compliance answers for the CryptoKit transport.

In Xcode choose the paid team with automatic signing enabled, select a generic
iOS device, **Product > Archive**, then Organizer **Distribute App > App Store
Connect** (or **TestFlight Internal Only** for your own internal test). Wait for
processing, add yourself to an internal TestFlight group, and install through
TestFlight on the phone. External testers require the applicable TestFlight
App Review approval. TestFlight builds are available for up to 90 days; upload
new builds to continue testing. The native Home configuration/transport gaps
remain the same. The installer does not archive, upload or publish anything.
See Apple's [TestFlight overview](https://developer.apple.com/help/app-store-connect/test-a-beta-version/testflight-overview/)
and [archive/upload walkthrough](https://developer.apple.com/tutorials/develop-in-swift/test-your-beta-app).
