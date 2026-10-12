# Wingthing on Bryan's iPhone

Pair the native app with **EhrlichMac's main wing first**, the same wing used by `wt` and the dashboard. Pair **forge** separately afterward. A pairing lets this phone see, attach to, type into and stop sessions on that machine. It cannot change wing configuration, pair another device or control an unpaired machine.

Click through [the storyboard](docs/pairing/STORYBOARD.html) while pairing. [The protocol](docs/pairing/PROTOCOL.md) specifies the PAKE, pinned identity, encrypted messages, storage and revocation.

## Install or reconnect

Bryan runs the existing installer himself:

```sh
WT_IOS_CLONE="$PWD" ./mobile/ios/scripts/wt-ios-install
# If the app is already installed:
WT_IOS_CLONE="$PWD" ./mobile/ios/scripts/wt-ios-install --connect-only
```

Run from the intended checkout. A copied installer wrapper needs `WT_IOS_CLONE` set to that checkout explicitly.

The signing/install stages retain the existing human-controlled Xcode workflow. Use the Apple Account and Team already selected in Xcode; connect and trust the iPhone by cable and enable Developer Mode. The agent only builds unsigned simulator/device SDK artifacts and does not change signing, accounts or Keychains. `--install-only` skips pairing. Keep the same Team and bundle ID when reinstalling to preserve the app's saved pairings.

The connect stage builds a pairing-capable CLI locally, uses the **normal main wing state directory** (`~/.wingthing` unless overridden), and runs `wt pair`. It creates no separate phone wing, workspace or account-token link. If the main wing is absent, the script starts it locally without an account. If an existing daemon predates pairing, the script stops with instructions to update/restart that same wing yourself; it never silently replaces a live daemon. Preserve its existing state directory and `wing_key`. This checkout has not been deployed to EhrlichMac or forge.

## Seven steps

1. In EhrlichMac's terminal run `wt pair`. Read the machine-specific permission header. It shows a terminal QR, six-word fallback code, five-minute countdown and “Waiting for scan...”.
2. Open Wingthing on Bryan's iPhone. Its first-run screen is **Scan to pair**. Allow Camera and Local Network when the app requests them, then scan the terminal QR.
3. The phone shows **Pair with EhrlichMac?**, the full SHA-256 wing fingerprint and four emoji. Compare all four with the terminal. Tap **Emoji match · Pair** on the phone and press Enter in the terminal. Both approvals are required. If they differ, cancel on both sides.
4. BoringSSL SPAKE2 authenticates the one-time secret and binds it to the scanned wing key. The phone creates its own P-256 key in Secure Enclave when available, otherwise device-only Keychain. The wing records the phone's public key and returns a machine-specific credential through encryption. No account bearer goes to the phone.
5. The phone opens EhrlichMac's session list: names, status and blocked/unread badges. Tap a session to attach, type or stop. Stop requires an explicit confirmation. Input is never automatically replayed after a connection failure.
6. Local-only is the default: on the same Wi-Fi no account is needed, and the terminal says “Local network only. That’s fine; nothing else needed.” LAN and Bonjour are tried first, then the scanned Tailscale IPv4 and MagicDNS endpoints; tailnet access also needs no Wingthing account or payment. Away from home through the hosted relay, EhrlichMac must be registered with the wingthing.ai relay once via `wt login` and run a relay-connected wing with hosted transport allowed. The phone never logs in. The relay forwards ciphertext to the pinned wing key; it cannot read credentials or session content. The relay route is implemented in this checkout and needs its normal operator deployment before hosted use; no relay deployment was performed here. Bryan can grant his owner account relay entitlement using the exact operator SQL in [PROTOCOL.md](docs/pairing/PROTOCOL.md#build-and-owner-relay-entitlement); no payment or Stripe write is necessary. The explicit `wt login` restart enables the relay on that same main wing. After registering a previously local-only Mac, reconnect the phone once on LAN to save its relay origin. A future tunn carrier can preserve the same protocol.
7. `wt devices` lists phones paired with this wing and their paired-at/last-seen times. `wt devices revoke "Bryan's iPhone"` immediately closes its live access and denies future commands. Run the command separately on every paired machine after losing the phone.

To pair forge second, run `ssh forge-tunn wt pair` using forge's own main-wing state and a native `CGO_ENABLED=1` build with a C++17 compiler and scan that terminal QR. The app's **Machines and fallback setup** menu lists EhrlichMac and forge as separate pairings. Their keys, grants and revocation are independent. Forge needs the same pairing-capable binary; this task does not install or restart it.

## Failures and fallback

- **Expired or already used:** press Enter in the terminal for a new QR. The previous secret never works again.
- **Unreachable:** “EhrlichMac isn't reachable from here. On the Mac, run `wt login` once to enable remote access.” Keep the wing awake and use the same Wi-Fi or connected tailnet for direct pairing. Initial carrier discovery can try the explicitly scanned relay; an uncertain session command cannot be replayed through another route.
- **Fingerprint mismatch:** red hard stop. Never continue or replace the pin silently.
- **Emoji mismatch:** cancel on the phone and terminal; create a fresh invitation.
- **Lost phone:** use `wt devices revoke` on EhrlichMac and forge. Revocation cannot erase content already downloaded to a phone.

Run `wt pair --show-link` only when needed to display its one-time invitation link. The hidden **Paste pairing link** menu is for a full `wingthing://pair#…` invitation; it follows the same checks and both confirmations. A six-word code alone does not identify a machine or its pinned public key. The hidden **Existing Home setup** menu preserves the previous `wingthing://home` form for existing account-based connections; that fallback is distinct from device pairing and carries broader existing account authority. `WT_IOS_SETUP_LINK_FILE` is rejected by the QR connect step; use the app's hidden legacy form explicitly when needed.

| Override | Purpose |
| --- | --- |
| `WT_IOS_TEAM`, `WT_IOS_DEVICE`, `WT_IOS_BUNDLE` | Existing human installation team, cable-connected phone and owned bundle ID. |
| `WT_IOS_WING_DIR` | Explicit main wing state directory; defaults to `WINGTHING_DIR` or `~/.wingthing`. Do not choose a new phone-specific directory. |
| `WT_IOS_GO`, `WT_IOS_XCODE`, `WT_IOS_CLONE`, `WT_IOS_STATE` | Existing installer toolchain, checkout and private installer paths. |

## Validation and required phone check

Local validation covers Go enrollment/device/relay failure cases with the race detector, the existing WingthingCore suite plus pairing tests, a native Swift-to-Go encrypted PAKE/device proof fixture, mocked installer behavior and an unsigned simulator build. All fixture wings use temporary directories and synthetic sessions; they never read main-wing credentials or start real agent sessions. Actual camera, Local Network permission, Secure Enclave/Keychain behavior, session interaction and cellular routing remain physical-phone acceptance work.

**REQUIRED-UNRUN:** Bryan opens the installed native app on his iPhone, scans a fresh `wt pair` QR on EhrlichMac, compares both sets of emoji, approves on both sides and verifies the resulting live session list. Signing/installing, production mutations, pushes and deployments were not performed by the agent.
