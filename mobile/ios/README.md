# Native Wingthing iOS client

The default native SwiftUI flow opens **Scan to pair**, pairs Bryan's iPhone with EhrlichMac's main wing using `wt pair`, and shows that machine's existing sessions. Forge is a separate pairing. LAN/Bonjour and tailnet work without a Wingthing account; the optional hosted relay remains encrypted to the scanned wing key. Both the terminal and phone must approve the same four emoji. Per-wing device keys/credentials use Secure Enclave and device-only Keychain storage; the phone does not restore an old account connection on startup.

Use [IOS-INSTALL.md](../../IOS-INSTALL.md), [STORYBOARD.html](../../docs/pairing/STORYBOARD.html) and [PROTOCOL.md](../../docs/pairing/PROTOCOL.md). The installer connect step runs `wt pair` against the normal main wing. The hidden paste-link fallback uses the same pairing checks. **Existing Home setup** separately preserves the older account-based conversation adapter described below.

October 11 QR validation: 133 Swift tests (including real Go SPAKE2/device proof interoperability), mocked installer checks and an unsigned simulator build pass. Invocation-local manifest sandbox flags are needed in this managed environment; no global Xcode or signing setting changed. Actual camera, Local Network permission, Secure Enclave/Keychain on Bryan's phone and cellular acceptance remain required. Counts below describe earlier conversation milestones.

## Native fleet and pinned thread

The paired-device inventory combines independently paired wings by `(origin_wing_id, session_id)`. Proven copies share a row with direct/via route chips. Equal display IDs on different origins remain separate. A remote machine label without an origin stays a possible duplicate and cannot receive actions. Healthy direct routes take precedence; opening a view can fall back to a healthy intermediary. Input and Stop never replay after a transport failure. Stop confirmation captures the exact selected row.

A pinned session with `role: coordinator` opens as the **wing** message thread above machine groups. Messages come from the existing native lifecycle journal through the owned, read-only `thread` operation; terminal output is not presented as conversation text. The terminal button is one tap away. Sending explicitly claims the existing terminal attachment before the phone input command. There is no provider approval action or invented read receipt. Cold search reads native message pages on the exact paired origins, including paged archived metadata advertised by the wing. Searches are bounded and label incomplete history. Archived views never attach, send or Stop. File attachments, remote SSH forwarding and background notification delivery require further backend support.

`history` advertises paged archived metadata only after the device handshake reports that capability. Older peers keep their active inventory. `origin.attach/read/thread/input/stop/detach` carry an exact origin in the encrypted device envelope. The distinct operation prefix lets older wings reject a remote action rather than treating its session ID as local. A wing must implement `phonepair.OriginHandler` with its own authorized forwarding adapter before accepting these operations. The native client and loopback fixture implement the contract; this change does not enable production remote forwarding.

Notifications require explicit consent and use generic content for attention observed while the app is open. There is no APNs or background-delivery claim. A simulator-only paid fixture flag, `WT_IOS_PAID_FIXTURE_ENABLED=1`, exposes a labeled dormant cloud-request form on the coordinator. It sends only an ordinary session request; it cannot call a VM/billing API or raise a spending cap. Release/device startup does not expose this fixture.

Run `make test-phone-native build-phone-testpeer` at the repository root, then `make simulator-phone-check` here with an explicit `IOS_QA_DEVICE`, `IOS_DERIVED_DATA`, `IOS_QA_RESULTS` and `PHONE_TEST_PEER`. The dedicated iPhone 17 Pro / iOS 26.3 simulator uses real Go SPAKE2 pairing and device proof over loopback with synthetic session content. The runner restores the simulator's appearance and text size and exports screenshots from XCTest. It never signs, installs on a physical phone, contacts a provider or touches an existing wing.

## Implemented slice

A newly configured home with no saved selection shows its existing conversation
inventory and Home settings. It does not select or launch a conversation until
the user taps a row. `make simulator-first-connection-check` verifies this
journey against an encrypted synthetic peer at the largest system text size,
including the unfiltered accessibility audit and zero launch/input/Stop counters.
The default unconfigured app opens the QR scanner.

**New conversation** can create the first parent on an already authorized
personal home. The form reads the pinned computer's existing `wing.info`
capability, Claude installation and project list, then lets the person choose
a project, a bounded name, an exact model and the first message. Create
rechecks that read-only evidence before saving an immutable launch intent.
The only launch shape is the existing bounded headless parent template with
the runtime's bound Wingthing MCP tools. The adapter has no arbitrary argv,
provider, permission, credential or workspace override.

The phone saves the intent before dispatch. A lost acknowledgement keeps the
same first message, project, model and request ID; relaunch and reconnect only
read, while **Retry creation** explicitly replays that exact launch. A confirmed
failure permits an explicit new attempt. The encrypted reply must identify a
matching parent/root, wing, name and workspace, and the subsequent native read
must match the acknowledged execution before following it. A reply that arrives
after another selection cannot retarget the screen or its draft. The decrypted
reply must echo the inner request ID; the outer relay correlation alone is not
sufficient. Older replies without that echo leave delivery unconfirmed. Prompt
receipts additionally bind the session ID.

The original creation milestone passed **84 core tests** and the SwiftUI typecheck. Nine creation
journeys cover empty-home creation, immutable replay/restart, save failure,
confirmed failure, project revalidation and delayed-selection fences.
`make simulator-creation-check` runs the separate synthetic native creation and
first-home suite. All **three simulator tests passed**, including unfiltered
largest-text accessibility audits of the creation form, new parent and existing
inventory. Lost-reply recovery produces two requests and exactly one root.
The Release debug-route guard also passes. Real provider inference from this adapter and a reachable
physical-phone home still require live acceptance.

- An explicit HTTPS home profile binds a configured origin, expected account ID, home executor WingID, and pinned X25519 public key. `/health` is only liveness. `/auth/check` and `/api/app/wings` verify the existing bearer identity and pinned executor before encrypted control. Redirects and cookie fallback are disabled. Additional executor pins must be supplied explicitly to the core.
- Existing encrypted browser control is implemented with CryptoKit X25519, HKDF-SHA256 (`wt-tunnel`, 32 zero salt bytes), and AES-256-GCM. It supports `conversation_list`, `conversation_read`, `session_read`, `session_status`, and `session_prompt`. Request IDs and the selected executor bind each response; there is no plaintext or alternative-origin fallback.
- Native SwiftUI uses the approved Astra conversation shell: one quiet toolbar, neutral user bubbles, unboxed assistant text, a keyboard-safe rounded composer, and expandable child rows with explicit Open task. Device theme and uncapped Dynamic Type use the approved tokens. The exact logical parent remains selected behind its brand/back navigation. Tool/provider records and execution IDs stay in Details; reconnect remains explicit. Parent selection and cached conversation content survive restart. Child selection does not borrow the child's attention state for the parent dot.
- State comes from native lifecycle evidence. A completed foreground turn is distinct from a completed task. A dead provider is archived; unsupported, stale, or restored lifecycle evidence cannot light a live completion state. Cached content remains readable offline.
- Unsent drafts are atomically saved after each edit, bound to the exact profile/account/wing/conversation/session/provider. They survive parent/child navigation, offline use and process relaunch, separately from pending input. Save failures retain the visible text and show a notice. A confirmed input is not restored as an unsent draft.
- Explicit input saves its immutable execution/reference, body, and request ID atomically before transport. An uncertain result remains uncertain. Only an explicit **Check receipt** reuses the same request and payload. Reconnect does not resend. `native_receipt_observed` requires the corresponding proof flag; `not_sent` requires `definitely_not_sent`.
- An ended Claude root with a fresh, exactly bound `headless_continuation` advertisement can accept a follow-up through the existing `agent_start` contract. The native adapter sends only the original source execution, parent role, message and immutable request ID. It saves that intent before transport, binds confirmation to the wing/root/provider/request/message hash, and follows only the acknowledged target. Explicit **Retry follow-up** replays the saved intent; reads and reconnect never resend it. A failed launch restores the message for a new explicit attempt.
- Whole-task Stop requires the exactly bound capability and receipt adapter; main lacks the corresponding `conversation_stop` server operation and capability, so real native Stop is unavailable. The synthetic client uses this adapter from the stable composer action or Details. Confirmation captures the displayed execution, which must still match before creating an intent. Its receipt separates a signal request from observed provider termination; leaving the screen or disconnecting sends no Stop.
- Optional `coordinator_context` observations decode without inventing HomeRoostID or owner epoch. Logical DotID, executor WingID, and a future HomeRoostID remain separate. Local saved input is not a durable coordinator inbox receipt, acceptance, or processing claim.

The `Home` form connects only to a home the user configures: the user enters the home address, account ID, executor WingID, pinned public key, and an access token they already hold, then explicitly connects. The app never obtains, mints, or refreshes a token, and no vendor account is required for a user-owned compatible Home. QR device pairing is the default flow; this hidden Home form is the older existing-account fallback and never performs phone sign-in. `Disconnect` drops the client and forgets the token; saved tasks and transcripts stay readable offline, marked as possibly out of date. `Reconnect` re-runs the read-only identity checks against the same configured home and never resends locally saved pending input. Verified bearers persist in non-synchronizing, device-only, when-unlocked Keychain items scoped to origin/account/wing/key; tunnel private keys remain in memory. Disconnect forgets the saved and attempted Home credentials. Relaunch can restore a configured Home, while device pairings use their independent machine-scoped enrollment flow. Product UI contains no fixture agents.

## Verification on this Mac

The persistence baseline at `f3fac02` passed **75 core tests** and the native UI typecheck, plus **10 simulator acceptance tests**: two exact draft/reading relaunch tests, three encrypted synthetic composer/replay/Stop tests, three existing-preview read-only navigation tests, and one normal-text test in each theme on a 390×844 iPhone simulator. The largest-text suites passed unfiltered accessibility audits. The Release debug-route guard and unsigned physical-device SDK compilation passed. No real prompt or Stop, provider call, signing, provisioning or phone installation occurred in these checks.

The iOS transcript owns a native UIScrollView containing the selectable SwiftUI messages. It saves a stable message ID and signed point offset, restores against current layout geometry, and follows new output only when within 80 points of the bottom. Missing or stale message geometry cannot replace the saved position. This local view state is separate from transport receipts and carries no authority or token. Physical-device and manual VoiceOver behavior still need acceptance.

On 2026-10-04 the package check passed **55 Swift Testing tests** and compiled `WingthingUI` against the Command Line Tools Mac SDK. Xcode **26.3 (17C529)** was then verified at a locally installed `Xcode.app`, with first-launch setup complete, iPhone Simulator SDK 26.2, and iOS 26.3.1 runtime available. The unsigned iOS app built successfully, installed and launched on a fresh owned iPhone 17 Pro simulator. Both the default empty Tasks screen and the real personal parent conversation were captured and inspected. Connected inspection used the explicit debug-only route below, with Send and Stop disabled. The default developer selection remains Command Line Tools; full Xcode was selected only for each invocation.

The composed recovery/mobile candidate passes **59 Swift Testing tests**. Its native UI now prioritizes messages, keeps provider/tool records in Details, offers exact child shortcuts and parent return, preserves history position while reading, and exposes a Latest action. Task-tree polling reads new linked-task evidence without retargeting the selected execution or draft. Long messages use small selectable reading blocks with speaker context for accessibility; concatenating the blocks preserves the exact native UTF-8 content. At accessibility text sizes, child links move into a compact task menu and the parent status remains in its accessible label. Landscape inspection moves the read-only label into the header to preserve reading space. The normal connected mode retains its message composer.

Actual XCUITest acceptance on the dedicated iPhone 17 Pro simulator passed **all three tests with zero failures** at the largest system text size: child navigation, history scrolling/Latest, Details, parent return, reconnect, the empty-start Home connection journey, the unfiltered full accessibility audit, and landscape with a reading viewport greater than 80 points. These runs submitted no prompts or Stop requests. Earlier failed audits remain preserved in the acceptance evidence. VoiceOver, physical phone, signing, and TestFlight acceptance remain open.

```sh
cd mobile/ios
make check
plutil -lint WingthingApp/Info.plist Wingthing.xcodeproj/project.pbxproj
```

The package uses Swift Testing because this CLT installation has its Testing framework but lacks XCTest. Build, cache, configuration, security, and module-cache paths default to task-specific `/tmp` directories. A managed outer sandbox can prevent SwiftPM's normal nested manifest sandbox; host approval is then required to run the same command, without disabling SwiftPM sandboxing.

The tests exercise a synthetic encrypted peer, not a real home, token, provider, or phone. They cover a golden ciphertext generated by the existing browser crypto implementation, exact bearer endpoints, wrong user/key/request rejection, same IDs on different wings, parent/child inspection, local persistence/restart, unsupported approval, native transcript/tool replay, archived provider state, reconnect without resend, and an uncertain input's exact receipt check. Fixture keys and tokens appear only in test sources.

### Debug-only local preview acceptance

`HomeProfile.localPreview` and `HomeClient(localPreview:)` are usable only in DEBUG builds. They accept only the literal origin `http://127.0.0.1:PORT`, the expected user `local` and one explicitly pinned wing ID and public key. No credential is accepted: a supplied one is refused before any network call, and no `Authorization` header is sent. Verification reads the real `/api/app/me` (`id` `local`, `release_channel` `preview`, `provider` `local`) and requires the pinned wing exactly once in `/api/app/wings`. After that, the existing encrypted wing control is used. A saved local-preview profile is revalidated when it is decoded and fails in release builds. The default HTTPS existing-bearer path is unchanged, and nothing selects local mode automatically.

`session_read` asks for one event per page to avoid combining large native events under the unchanged coordination response cap. An oversized individual event still fails. `readToHead` pages until the head and enforces identity, sequence and cursor progress. It stops at 500 pages or 120 seconds. The native wire serializes exchanges and reuses its WebSocket for the same origin and credential. A failed exchange closes that connection and is never resent automatically. The `WingthingLivePreview` executable reads the root conversation and each of its children. It prints a JSON receipt with IDs, cursors and the final native assistant text. It never prints thinking, tool input or results, or PTY output. It does not launch, prompt, stop, sign in or pair:

```sh
make live-preview PREVIEW_ORIGIN=http://127.0.0.1:PORT PREVIEW_USER=local PREVIEW_WING=WING_ID PREVIEW_KEY=BASE64_PUBLIC_KEY PREVIEW_ROOT=ROOT_CONVERSATION_ID
make live-preview-release-check
```

This is a same-Mac check of the Foundation/CryptoKit adapter. It does not show that a phone can reach the Mac.

The Debug **simulator** app can inspect that same explicitly pinned home. Pass all six `SIMCTL_CHILD_WT_IOS_PREVIEW_` launch values (`ENABLED=1`, `ORIGIN`, `USER=local`, `WING`, `KEY`, `ROOT`) to `xcrun simctl launch`; `SELECTED` optionally names a child already present in the parent tree. Missing, unknown or credential fields are refused. The ordinary app starts empty without these values, and this launch route is absent from Release and physical-device app startup. Preview mode verifies the real preview account and wing, only reads existing conversations, and disables Send and Stop. Its deterministic metadata-only profile preserves the cached parent on relaunch. No credential or authorization is created.

On 2026-10-04 this route installed and launched on the owned iPhone 17 Pro simulator and rendered the actual archived personal parent and native message text. Child selection, connected interaction, accessibility and physical phone acceptance are separate remaining checks.

Full Xcode is now installed on this development Mac. Use its explicit developer directory for app builds rather than changing the shared command-line tool selection. On another Mac, the human installs Xcode, accepts its license and installs an iOS runtime first.

The repeatable app build is
`DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer make simulator-build`.
It first checks the full Xcode installation, first-launch setup status and
installed simulator SDK, then builds the unsigned app for a generic iOS
Simulator into `/tmp/wingthing-ios-simulator-build`. It never runs initial setup,
accepts a license, downloads components or changes `xcode-select`. Building does
not start a simulator or establish rendered/device acceptance.

`Wingthing.xcodeproj` references the local package and targets iOS 16+. Signing is disabled and no team, entitlement, notification setup, or device enrollment is configured. The plist/project syntax check and unsigned Xcode simulator build passed on this Mac. That does not establish signing, physical iPhone, or TestFlight readiness.

`make device-unsigned-check` compiles the Release app against the installed
physical-device SDK with signing disabled. It selects no Apple team or phone,
registers no app/device, creates no provisioning asset and installs nothing.
That unsigned product cannot run on an iPhone. Actual development installation
requires the intended Personal Team and exact phone, plus human device trust
and Developer Mode where required.

## Read-only simulator UI acceptance

`make simulator-ui-check` builds an unsigned app and XCTest UI runner with two jobs on an explicit existing simulator. Supply `IOS_QA_DEVICE`, `IOS_DERIVED_DATA`, a fresh `IOS_QA_RESULTS` path, and the metadata-only `PREVIEW_ORIGIN`, `PREVIEW_USER`, `PREVIEW_WING`, `PREVIEW_KEY`, `PREVIEW_ROOT`, and `IOS_QA_CHILD`. The origin must be an exact HTTP `127.0.0.1` origin. The generated test specification is copied; no credentials, grants, provider processes, or shared signing settings are changed. Only the dedicated simulator's content-size preference is temporarily set to `accessibility-extra-extra-extra-large` and restored in `finally`, with restoration checked in the receipt. The app's font category is not pinned by launch arguments, so the audit can exercise genuine Dynamic Type changes. Screenshots and the unfiltered accessibility audit are retained in the result bundle. A nonzero test exit remains a failure.

## Concrete next acceptance journey

The simulator-only DEBUG fixture is separate from real preview inspection. Run
`make simulator-fixture-check` with an existing `IOS_QA_DEVICE`, selected
`IOS_DERIVED_DATA` and fresh `IOS_QA_RESULTS`. It exercises the native composer,
lost acknowledgement, reconnect without resend, immutable explicit replay,
failed-launch draft recovery and confirmed Stop over the actual encrypted
adapter into an in-process reserved `.invalid` home. It opens no network or
provider, accepts no real profile/credential arguments, and restores the
dedicated simulator's text size after its unfiltered accessibility checks.
Its bootstrap is excluded from release and physical-device launches.

`make simulator-visual-check` exercises a separate synthetic design conversation
and child using normal system text and temporary/restored `IOS_QA_APPEARANCE`
(`light` or `dark`). It keeps genuine full-screen PNGs in the XCTest result,
checks the toolbar remains visible with the native keyboard, and verifies child
return restores the parent's unsent draft and expanded row. Use a dedicated
390×844 iPhone simulator for comparison with Astra's approved renders. No mock
attachment/result feature is presented as implemented. Message-ID/within-message scroll restoration and durable unsent drafts are now
implemented. `make simulator-local-state-check` exercises exact parent/child
drafts across process relaunch, the same message/point offset after child return,
relaunch and rotation, and explicit Latest. Its persistence UUID and synthetic
profile are DEBUG simulator-only; production startup still restores no access.
Full history/settings polish and manual VoiceOver acceptance remain open.

1. On a Mac with full Xcode, build the unsigned simulator app and verify the empty welcome/connection screens, native navigation, dynamic type, VoiceOver, small-screen layout, and foreground/background polling behavior. No vendor request should occur on first launch.
2. Use an already authorized HTTPS home profile reachable from the phone, with the exact account, persistent WingID, pinned public key and existing bearer supplied privately to the app. Configuring existing access creates no grant. New pairing/sign-in, TLS trust, endpoint exposure or persistent credentials require separate scope. A locked wing currently requires its existing passkey flow; this draft fails visibly rather than granting or bypassing it.
3. Configure the exact origin/account/executor pin, reconnect, select a real parent, inspect a linked child's native transcript and tool records, return through the parent dot, and confirm restart restores the logical selection. Provider status must match native evidence.
4. Send one input to a ready Claude native execution. Interrupt transport before receipt, reconnect, and confirm there is no automatic resend. Explicitly check the saved request. A changed account, key, current execution, or stale response must not retarget it.
5. Disconnect the home. Keep cached task content visible with offline/unknown state, then reconnect to the same configured peer. Repeat on a physical phone only after endpoint reachability and authorization are explicitly approved.

## Remaining gates

- Full Xcode/iOS SDK/simulator compilation, connected child/history/Details/parent/reconnect interaction, the empty-start Home journey, the largest-text unfiltered audit, and landscape reading space passed. VoiceOver, background behavior and physical phone acceptance remain open.
- Keychain credential storage is implemented; native pairing/sign-in, token renewal, identity QR approval, and passkey authorization are not. No permissions or grants were requested.
- Current preview roost loopback binding prevents a physical phone from reaching the Mac. The app does not alter that guard. Local QR identity sharing would not make a home reachable across NAT; away access needs an explicitly selected user-owned endpoint or existing VPN/Tailscale/self-host relay. Hosted home is a separate explicit choice with no baked-in address.
- Generic agent launch, terminal input leases and exact native approval decisions are not adapted. New parent creation, ended-root follow-up and whole-task Stop are narrow typed adapters; their native interaction checks use synthetic transport, not a live authorized phone. `needs_input` provides attention and an execution/state cursor, not a provider approval request ID. There is no Approve button or autoapproval.
- HomeRoostID persistence, coordinator epoch fencing, durable inbox/outbox receipt stages, background delivery, push notifications, migration, and failover remain unsupported. No exactly-once external-effect claim is made.
- `session_prompt` uses the server's existing immutable reservation/receipt behavior. This draft preserves the observed provider ID locally; the current request schema does not add a new expected-provider precondition or authority field.

## Source contract

The native adapter follows `web/src/crypto.js`, `web/src/tunnel.js`, `internal/auth/crypto.go`, `internal/ws/protocol.go`, `internal/relay/handler.go`, `internal/relay/app_handlers.go`, `internal/relay/pty_relay.go`, `cmd/wt/conversation_browser.go`, `cmd/wt/conversations.go`, and `internal/egg/session_prompt.go` / `lifecycle.go`.

Primary Apple API references: [URLSessionWebSocketTask](https://developer.apple.com/documentation/foundation/urlsessionwebsockettask), [Curve25519.KeyAgreement](https://developer.apple.com/documentation/cryptokit/curve25519/keyagreement), [AES.GCM](https://developer.apple.com/documentation/cryptokit/aes/gcm), and [NavigationStack](https://developer.apple.com/documentation/swiftui/navigationstack). Foundation WebSocket and CryptoKit require iOS 13 or later; this project uses iOS 16 for NavigationStack.
