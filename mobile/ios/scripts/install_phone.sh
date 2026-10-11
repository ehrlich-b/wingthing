#!/bin/bash
# Bryan runs this through ~/repos/claude/bin/wt-ios-install.
set +x
set -euo pipefail
umask 077

die() { printf 'STOP: %s\n' "$*" >&2; exit 1; }
step() { printf '\n== %s\n' "$*"; }
need() { command -v "$1" >/dev/null 2>&1 || die "Missing $1; install it and rerun."; }
ask() {
  printf '%s ' "$1" >/dev/tty 2>/dev/null || die 'Interactive input unavailable; set the documented WT_IOS_* override and rerun.'
  IFS= read -r REPLY </dev/tty || die 'No answer; rerun with the documented override.'
}
MODE=${1:-all}
[[ $# -le 1 ]] || die 'Usage: wt-ios-install [--install-only|--connect-only|--clear-clipboard|--help]'
case "$MODE" in
  --help)
    printf '%s\n' 'Usage: wt-ios-install [--install-only|--connect-only|--clear-clipboard]' \
      'See IOS-INSTALL.md for signing/device/Home overrides and one-time human steps.'
    exit 0 ;;
  --clear-clipboard) pbcopy </dev/null; exit 0 ;;
  all|--install-only|--connect-only) ;;
  *) die 'Unknown option; use --help.' ;;
esac
[[ $(uname -s) == Darwin ]] || die 'Run this installer on the Mac.'
CLONE=${WT_IOS_CLONE:-$(cd "$(dirname "$0")/../../.." && pwd)}
XCODE=${WT_IOS_XCODE:-/Users/ehrlich/Downloads/Xcode.app}
STATE=${WT_IOS_STATE:-$HOME/.local/state/wt-ios-install}
SUPPORT=$CLONE/mobile/ios/scripts/ios_install_support.py
ENTRYPOINT=${WT_IOS_ENTRYPOINT:-$HOME/repos/claude/bin/wt-ios-install}
for cmd in python3 xcrun xcodebuild defaults plutil pbcopy open taskpolicy nice; do need "$cmd"; done
[[ -f $SUPPORT && -f $CLONE/mobile/ios/Wingthing.xcodeproj/project.pbxproj ]] || die 'Native project/support files are missing.'
[[ -x $XCODE/Contents/Developer/usr/bin/xcodebuild ]] || die "Install full Xcode at $XCODE, open it and finish its license/components."
export DEVELOPER_DIR="$XCODE/Contents/Developer"
need install
if ! cmp -s "$CLONE/mobile/ios/scripts/wt-ios-install" "$ENTRYPOINT"; then
  [[ ! -L $ENTRYPOINT ]] || die 'Installer entry point is a symlink; inspect it first.'
  mkdir -p "$(dirname "$ENTRYPOINT")"
  install -m 755 "$CLONE/mobile/ios/scripts/wt-ios-install" "$ENTRYPOINT"
fi
[[ ! -L $STATE ]] || die 'Installer state is a symlink; inspect it before rerunning.'
mkdir -p "$STATE"
chmod 700 "$STATE"
for file in "$STATE"/*; do [[ ! -L $file ]] || die 'Installer state contains a symlink; inspect it.'; done
mkdir "$STATE/install.lock" 2>/dev/null || die "Another installer or a stale lock exists at $STATE/install.lock; inspect it before rerunning."
cleanup() {
  rm -f "$STATE/setup-link" "$STATE/identity-link" "$STATE/xcode-preferences.plist"
  rmdir "$STATE/install.lock" 2>/dev/null || true
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP

step '1/7 Xcode and the one-time human setup'
printf '%s\n' 'Xcode > Settings > Accounts > + > Apple Account: sign in yourself (Personal Team is enough).' \
  'Connect/unlock iPhone by cable; tap Trust This Computer and enter its passcode.' \
  'iPhone Settings > Privacy & Security > Developer Mode: enable, restart, then confirm.' \
  'After first install, if requested: Settings > General > VPN & Device Management > Developer App > Trust.'
if ! xcodebuild -checkFirstLaunchStatus >"$STATE/xcode-preflight.log" 2>&1; then
  open "$XCODE"
  die 'Finish the Xcode license/components yourself, then rerun.'
fi
xcrun --sdk iphoneos --show-sdk-path >/dev/null || die 'Install the iOS platform in Xcode Settings > Components.'

step '2/7 Find the connected, trusted iPhone'
xcrun devicectl list devices --timeout 30 --json-output "$STATE/devices.json" >"$STATE/devices.log" 2>&1 \
  || die "Device discovery failed; inspect $STATE/devices.log."
REQUESTED=${WT_IOS_DEVICE:-}
PHONES=$(python3 "$SUPPORT" devices "$STATE/devices.json" "$REQUESTED")
[[ -n $PHONES ]] || die 'No matching cable-connected, paired iPhone. Unlock/trust it, open Xcode > Window > Devices and Simulators, then rerun.'
COUNT=$(printf '%s\n' "$PHONES" | wc -l | tr -d ' ')
if [[ $COUNT != 1 ]]; then
  printf '%s\n' "$PHONES"
  ask 'More than one iPhone: paste its identifier (or rerun with WT_IOS_DEVICE):'
  PHONES=$(python3 "$SUPPORT" devices "$STATE/devices.json" "$REPLY")
  [[ -n $PHONES && $(printf '%s\n' "$PHONES" | wc -l | tr -d ' ') == 1 ]] || die 'Select exactly one listed iPhone.'
fi
IFS=$'\t' read -r DEVICE PHONE_NAME DEVELOPER_MODE <<<"$PHONES"
printf 'Selected: %s (%s)\n' "$PHONE_NAME" "$DEVICE"
[[ $DEVELOPER_MODE != disabled ]] || die 'Enable Developer Mode on this phone, restart/confirm, then rerun.'

if [[ $MODE != --connect-only ]]; then
  step '3/7 Reuse or discover the signing team'
  TEAM=${WT_IOS_TEAM:-}
  if [[ -z $TEAM && -f $STATE/team ]]; then TEAM=$(cat "$STATE/team"); fi
  if [[ -z $TEAM ]]; then
    TEAMS=
    if defaults export com.apple.dt.Xcode "$STATE/xcode-preferences.plist" >"$STATE/preferences.log" 2>&1; then
      TEAMS=$(python3 "$SUPPORT" teams "$STATE/xcode-preferences.plist")
    fi
    rm -f "$STATE/xcode-preferences.plist"
    if [[ -n $TEAMS && $(printf '%s\n' "$TEAMS" | wc -l | tr -d ' ') == 1 ]]; then TEAM=$TEAMS
    else
      [[ -z $TEAMS ]] || printf 'Teams recorded by Xcode:\n%s\n' "$TEAMS"
      printf '%s\n' 'Find Team ID in Xcode Accounts/team details (or your developer account Membership Details).' \
        'A Personal Team may first need selection under Wingthing target > Signing & Capabilities.'
      ask 'Paste the 10-character Team ID once (or rerun with WT_IOS_TEAM):'
      TEAM=$REPLY
    fi
  fi
  [[ $TEAM =~ ^[A-Z0-9]{10}$ ]] || die 'Team ID must be 10 uppercase letters/digits; override with WT_IOS_TEAM.'
  printf '%s\n' "$TEAM" >"$STATE/team"
  printf 'Signing team: %s\n' "$TEAM"
  BUNDLE=${WT_IOS_BUNDLE:-dev.ehrlich.wingthing.phone.$TEAM}
  [[ $BUNDLE =~ ^[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+$ ]] || die 'Invalid WT_IOS_BUNDLE identifier.'
  printf '%s\n' "$BUNDLE" >"$STATE/bundle"

  step '4/7 Build the native Release app and provision with your Xcode account'
  printf 'Build output stays in %s/build.log; Apple may request account/Keychain approval.\n' "$STATE"
  if ! taskpolicy -b nice -n 15 xcodebuild -project "$CLONE/mobile/ios/Wingthing.xcodeproj" \
    -scheme Wingthing -configuration Release -destination 'generic/platform=iOS' \
    -derivedDataPath "$STATE/derived-data" -jobs 2 -disableAutomaticPackageResolution -skipPackageUpdates \
    -allowProvisioningUpdates -allowProvisioningDeviceRegistration DEVELOPMENT_TEAM="$TEAM" \
    PRODUCT_BUNDLE_IDENTIFIER="$BUNDLE" CODE_SIGN_STYLE=Automatic CODE_SIGNING_ALLOWED=YES \
    CODE_SIGNING_REQUIRED=YES CODE_SIGN_IDENTITY='Apple Development' build >"$STATE/build.log" 2>&1; then
    die "Build/signing failed; inspect $STATE/build.log privately. For account/provisioning errors, select your Team and phone in Xcode and Run once, then rerun."
  fi
  APP=$STATE/derived-data/Build/Products/Release-iphoneos/Wingthing.app
  [[ -d $APP ]] || die 'Xcode reported success but Wingthing.app is missing.'

  step '5/7 Install and launch on the iPhone'
  xcrun devicectl device install app --device "$DEVICE" --timeout 120 "$APP" >"$STATE/install.log" 2>&1 \
    || die "Install failed; inspect $STATE/install.log privately. Keep phone unlocked; register/select it in Xcode if its profile excludes this phone."
else
  step '3–5/7 Reuse the installed native app'
  [[ -n ${WT_IOS_BUNDLE:-} || -f $STATE/bundle ]] || die 'Install first, or supply WT_IOS_BUNDLE for an already installed app.'
  BUNDLE=${WT_IOS_BUNDLE:-$(cat "$STATE/bundle")}
fi
if ! xcrun devicectl device process launch --device "$DEVICE" --timeout 60 "$BUNDLE" >"$STATE/launch.log" 2>&1; then
  die "App is installed but could not launch. Unlock it, enable Developer Mode, trust Developer App in VPN & Device Management if asked, then rerun with --connect-only. See $STATE/launch.log privately."
fi
[[ $MODE != --install-only ]] || { printf 'Native Wingthing installed and launched. Rerun with --connect-only for Home setup.\n'; exit 0; }

step '6/7 Connect a compatible HTTPS Home'
if [[ -n ${WT_IOS_SETUP_LINK_FILE:-} ]]; then
  # An existing Mac/forge/self-hosted Home: no login/start/config changes.
  python3 "$SUPPORT" validate "$WT_IOS_SETUP_LINK_FILE" || die 'Setup file must be owned by you, mode 600, and contain a valid HTTPS Home link with an existing token.'
  cp "$WT_IOS_SETUP_LINK_FILE" "$STATE/setup-link"
else
  GO=${WT_IOS_GO:-$HOME/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.9.darwin-arm64/bin/go}
  [[ -x $GO ]] || die 'Go 1.26.9 is missing; set WT_IOS_GO to its executable.'
  need claude
  need install
  WING=$HOME/.wingthing-ios-phone-20261011
  WORK=$HOME/phone-native-work-20261011
  BIN=$HOME/.local/bin/wt-ios-phone-20261011
  printf '%s\n' 'Home: https://wingthing.ai; executor: a separate personal Mac wing.' \
    "Workspace: $WORK (tools read ro:/, write only this workspace; tool network: none)." \
    'Hosted access needs existing account entitlement. Complete the Wingthing browser approval if it opens.'
  mkdir -p "$CLONE/.scratch/bin" "$HOME/.local/bin"
  [[ ! -L $BIN ]] || die 'Managed wing executable is a symlink.'
  (cd "$CLONE" && taskpolicy -b nice -n 15 env GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off \
    GOMAXPROCS=2 GOCACHE="$CLONE/.scratch/go-build" GOMODCACHE="$CLONE/.scratch/gomod" \
    "$GO" build -p 2 -ldflags '-X github.com/ehrlich-b/wingthing/internal/config.ReleaseChannel=stable' \
    -o .scratch/bin/wt-ios-install ./cmd/wt) >"$STATE/wing-build.log" 2>&1 \
    || die "Wing build failed; inspect $STATE/wing-build.log. Seed the clone Go cache if dependencies are missing."
  if ! cmp -s "$CLONE/.scratch/bin/wt-ios-install" "$BIN"; then install -m 700 "$CLONE/.scratch/bin/wt-ios-install" "$BIN"; fi
  python3 - "$WING" "$WORK" <<'PY'
import json, pathlib, sys
wing, work = map(pathlib.Path, sys.argv[1:])
marker = 'wt-ios-install-v1\n'
policies = {wing: {'wing.yaml': 'roost: https://wingthing.ai\nlabel: bryans-native-phone\npaths: [' + json.dumps(str(work)) + ']\nconversations: enabled\nhosted_relay: allow\nlocked: false\nallow_keys: []\n'},
            work: {'egg.yaml': 'fs: ["ro:/", "rw:./"]\nnetwork: none\n'}}
for directory, files in policies.items():
    if directory.is_symlink(): raise SystemExit('STOP: Managed wing/workspace is a symlink.')
    if directory.exists():
        tag = directory / '.wt-ios-install-owner'
        if tag.is_symlink() or not tag.is_file() or tag.read_text() != marker:
            raise SystemExit('STOP: Existing wing/workspace is unrecognized; it will not be overwritten.')
        for name, text in files.items():
            p = directory / name
            if p.is_symlink() or not p.is_file() or p.read_text() != text:
                raise SystemExit('STOP: Managed wing/workspace policy changed; inspect it instead of overwriting.')
for directory, files in policies.items():
    if directory.exists(): continue
    directory.mkdir(mode=0o700)
    for name, text in files.items():
        p = directory / name
        p.write_text(text); p.chmod(0o600)
    (directory / '.wt-ios-install-owner').write_text(marker)
PY
  # Clear relay/process overrides without changing the normal provider login.
  WT=(env -u WT_NODE_ROLE -u WT_APP_HOST -u WT_WS_HOST -u WT_BASE_URL \
    -u WINGTHING_PREVIEW_DIR -u WT_ROOST_ALLOWED_EMAILS WINGTHING_DIR="$WING" "$BIN")
  "${WT[@]}" login --roost https://wingthing.ai >"$STATE/wing-login.log" 2>&1 \
    || die "Wingthing login failed; inspect $STATE/wing-login.log privately and rerun."
  "${WT[@]}" wing status >"$STATE/wing-status.log" 2>&1 || die 'Managed wing status is invalid; inspect it.'
  if ! grep -q '^wing daemon is running ' "$STATE/wing-status.log"; then
    "${WT[@]}" wing start --roost https://wingthing.ai --egg-config "$WORK/egg.yaml" >"$STATE/wing-start.log" 2>&1 \
      || die "Managed wing startup failed; inspect $STATE/wing-start.log."
  fi
  "${WT[@]}" phone link >"$STATE/setup-link" 2>"$STATE/link.log" \
    || die "Home link creation failed; inspect $STATE/link.log privately."
fi
chmod 600 "$STATE/setup-link"
printf '%s\n' 'Checking HTTPS health, bearer account and the pinned online wing (up to 60 seconds).'
python3 "$SUPPORT" verify "$STATE/setup-link" \
  || die 'Home/account/wing could not be verified. Check network, wing availability, hosted entitlement and its lock/passkey policy. The app remains installed; rerun --connect-only after resolving it.'

step '7/7 Prefill Home and hand off the credential privately'
python3 "$SUPPORT" identity "$STATE/setup-link" >"$STATE/identity-link"
# Only public identity travels in devicectl arguments/logs; token stays in clipboard.
if ! xcrun devicectl device process launch --device "$DEVICE" --timeout 60 \
  --payload-url "$(cat "$STATE/identity-link")" "$BUNDLE" >"$STATE/home-launch.log" 2>&1; then
  printf '%s\n' 'Open Wingthing on the phone and tap Connect a home (or Home settings).'
fi
pbcopy <"$STATE/setup-link"
printf '%s\n' 'Private setup link copied to the Mac clipboard; no token or credential URL is printed.' \
  'On iPhone: Home > Paste setup link (allow paste) > Connect. Wait for Online, then open a conversation.' \
  'Universal Clipboard needs the same Apple Account, Wi-Fi/Bluetooth and Handoff on both devices.' \
  'Without Handoff: open the private device_token.yaml locally and enter its bearer in Home; see IOS-INSTALL.md.' \
  'Keep the executor Mac awake/online. Nothing sends a prompt until you explicitly send/create on the phone.' \
  'Native whole-conversation Stop, pairing/QR scanning, token refresh and push remain unavailable.' \
  'After pasting, run wt-ios-install --clear-clipboard or copy unrelated text to replace the private link.'
