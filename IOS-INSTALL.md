# Wingthing on your iPhone

Pair the native app with **my-mac's main wing first**, the same wing used by `wt` and the dashboard. Pair **my-server** separately afterward. A pairing lets this phone see, attach to, type into and stop sessions on that machine. It cannot change wing configuration, pair another device or control an unpaired machine.

Click through [the storyboard](docs/pairing/STORYBOARD.html) while pairing. [The protocol](docs/pairing/PROTOCOL.md) specifies the PAKE, pinned identity, encrypted messages, storage and revocation.

## Install or reconnect

The owner runs the existing installer themselves:

```sh
WT_IOS_CLONE="$PWD" ./mobile/ios/scripts/wt-ios-install
# If the app is already installed:
WT_IOS_CLONE="$PWD" ./mobile/ios/scripts/wt-ios-install --connect-only
```

Run from the intended checkout. A copied installer wrapper needs `WT_IOS_CLONE` set to that checkout explicitly.

The signing/install stages retain the existing human-controlled Xcode workflow. Use the Apple Account and Team already selected in Xcode; connect and trust the iPhone by cable and enable Developer Mode. The agent only builds unsigned simulator/device SDK artifacts and does not change signing, accounts or Keychains. `--install-only` skips pairing. Keep the same Team and bundle ID when reinstalling to preserve the app's saved pairings.

The connect stage builds a pairing-capable CLI locally. Its isolated `.scratch/go-mod` cache is filled on demand from the existing user Go module download cache (`go env GOMODCACHE`) through a file-only proxy; it never downloads missing dependencies from the network. If dependencies are unavailable, `PAIR_CLI_BUILD_FAILED` names the offline cache and private build log. A script, symlink or other non-binary at `.scratch/bin/wt-ios-pair` causes `PAIR_CLI_OUTPUT_CONFLICT` before the build; the file is left untouched for inspection. Installer fixtures use temporary clones and never write into this checkout’s `.scratch`.

The connect stage uses the **normal main wing state directory** (`~/.wingthing` unless overridden), and runs `wt pair`. It creates no separate phone wing, workspace or account-token link. If the main wing is absent, the script starts it without an account using `wt wing start --local-only --listen 0.0.0.0` for LAN pairing. If a running wing listens only on loopback, the script stops before pairing and prints stop/start commands for that state directory. The restart hint asks the running wing for its mode over the existing authenticated local control endpoint. A local-only reply prints `wt wing start --local-only --listen 0.0.0.0`; a relay reply prints `wt wing start --listen 0.0.0.0`. Other flags the wing was started with must be added by hand. If the daemon cannot be reached, does not support the mode query, or gives an ambiguous reply, the installer shows both variants with a one-line explanation. It never reads `wing.args` or `wing.status` to decide which command to suggest. Run the printed command when ready to interrupt the wing and rerun `--connect-only`.

If an existing daemon predates pairing (no `phone-control.json`), the script reads the mode reported by `wt wing status` after its live daemon check and lists active egg session names and IDs with `wt egg list`. It prints one command using the newly built CLI and the same state directory: `wt wing stop && wt wing start` for relay mode, or `wt wing stop && wt wing start --local-only --listen 0.0.0.0` for local-only mode, then exits. It stops with a named error if the live mode or session inventory is unavailable. It never restarts a running wing itself. Preserve its existing state directory and `wing_key`. This checkout has not been deployed to my-mac or my-server.

## Seven steps

1. If my-mac's main wing is absent, start it with `wt wing start --local-only --listen 0.0.0.0`. In my-mac's terminal run `wt pair`. Read the machine-specific permission header. It shows a terminal QR, six-word fallback code, five-minute countdown and “Waiting for scan...”.
2. Open Wingthing on your iPhone. Its first-run screen is **Scan to pair**. Allow Camera and Local Network when the app requests them, then scan the terminal QR.
3. The phone shows **Pair with my-mac?**, the full SHA-256 wing fingerprint and four emoji. Compare all four with the terminal. Tap **Emoji match · Pair** on the phone and press Enter in the terminal. Both approvals are required. If they differ, cancel on both sides.
4. BoringSSL SPAKE2 authenticates the one-time secret and binds it to the scanned wing key. The phone creates its own P-256 key in Secure Enclave when available, otherwise device-only Keychain. The wing records the phone's public key and returns a machine-specific credential through encryption. No account bearer goes to the phone.
5. The phone opens my-mac's session list: names, status and blocked/unread badges. Tap a session to attach, type or stop. Stop requires an explicit confirmation. Input is never automatically replayed after a connection failure.
6. Local-only is the default: on the same Wi-Fi no account is needed, and the terminal says “Local network only. That’s fine; nothing else needed.” LAN and Bonjour are tried first, then the scanned Tailscale IPv4 and MagicDNS endpoints; tailnet access also needs no Wingthing account or payment. Away from home through the hosted relay, my-mac must be registered with the wingthing.ai relay once via `wt login` and run a relay-connected wing with hosted transport allowed. The phone never logs in. The relay forwards ciphertext to the pinned wing key; it cannot read credentials or session content. The relay route is implemented in this checkout and needs its normal operator deployment before hosted use; no relay deployment was performed here. The owner can grant the owner account relay entitlement using the exact operator SQL in [PROTOCOL.md](docs/pairing/PROTOCOL.md#build-and-owner-relay-entitlement); no payment or Stripe write is necessary. The explicit `wt login` restart enables the relay on that same main wing. After registering a previously local-only Mac, reconnect the phone once on LAN to save its relay origin. A future tunn carrier can preserve the same protocol.
7. `wt devices` lists phones paired with this wing and their paired-at/last-seen times. `wt devices revoke "My iPhone"` immediately closes its live access and denies future commands. Run the command separately on every paired machine after losing the phone.

To pair my-server second, run `ssh server.example.com wt pair` using my-server's own main-wing state and a native `CGO_ENABLED=1` build with a C++17 compiler and scan that terminal QR. The app's **Machines and fallback setup** menu lists my-mac and my-server as separate pairings. Their keys, grants and revocation are independent. Pairing on my-server requires the same pairing-capable binary.

## Failures and fallback

- **Expired or already used:** press Enter in the terminal for a new QR. The previous secret never works again.
- **Unreachable:** “my-mac isn't reachable from here. On the Mac, run `wt login` once to enable remote access.” Keep the wing awake and use the same Wi-Fi or connected tailnet for direct pairing. Initial carrier discovery can try the explicitly scanned relay; an uncertain session command cannot be replayed through another route.
- **Fingerprint mismatch:** red hard stop. Never continue or replace the pin silently.
- **Emoji mismatch:** cancel on the phone and terminal; create a fresh invitation.
- **Lost phone:** use `wt devices revoke` on my-mac and my-server. Revocation cannot erase content already downloaded to a phone.

Run `wt pair --show-link` only when needed to display its one-time invitation link. The hidden **Paste pairing link** menu is for a full `wingthing://pair#…` invitation; it follows the same checks and both confirmations. A six-word code alone does not identify a machine or its pinned public key. The hidden **Existing Home setup** menu preserves the previous `wingthing://home` form for existing account-based connections; that fallback is distinct from device pairing and carries broader existing account authority. `WT_IOS_SETUP_LINK_FILE` is rejected by the QR connect step; use the app's hidden legacy form explicitly when needed.

| Override | Purpose |
| --- | --- |
| `WT_IOS_TEAM`, `WT_IOS_DEVICE`, `WT_IOS_BUNDLE` | Existing human installation team, cable-connected phone and owned bundle ID. |
| `WT_IOS_WING_DIR` | Explicit main wing state directory; defaults to `WINGTHING_DIR` or `~/.wingthing`. Do not choose a new phone-specific directory. |
| `WT_IOS_GO`, `WT_IOS_XCODE`, `WT_IOS_CLONE`, `WT_IOS_STATE` | Existing installer toolchain, checkout and private installer paths. |

## Validation and required phone check

Local validation covers Go enrollment/device/relay failure cases with the race detector, the existing WingthingCore suite plus pairing tests, a native Swift-to-Go encrypted PAKE/device proof fixture, mocked installer behavior and an unsigned simulator build. All fixture wings use temporary directories and synthetic sessions; they never read main-wing credentials or start real agent sessions. Actual camera, Local Network permission, Secure Enclave/Keychain behavior, session interaction and cellular routing remain physical-phone acceptance work.

**REQUIRED-UNRUN:** the owner opens the installed native app on their iPhone, scans a fresh `wt pair` QR on my-mac, compares both sets of emoji, approves on both sides and verifies the resulting live session list. Signing/installing, production mutations, pushes and deployments were not performed by the agent.
