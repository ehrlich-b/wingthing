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
      'See IOS-INSTALL.md for signing/device/main-wing overrides and one-time human steps.'
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
[[ $MODE != --install-only ]] || { printf 'Native Wingthing installed and launched. Rerun with --connect-only to scan the main wing QR.\n'; exit 0; }

step "6/7 Connect to this Mac's main wing"
[[ -z ${WT_IOS_SETUP_LINK_FILE:-} ]] || die "The QR flow pairs with the main wing. For an old Home link, use the app's hidden Existing Home setup fallback."
GO=${WT_IOS_GO:-$HOME/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.9.darwin-arm64/bin/go}
[[ -x $GO ]] || die 'Go 1.26.9 is missing; set WT_IOS_GO to its executable.'
WING=${WT_IOS_WING_DIR:-${WINGTHING_DIR:-$HOME/.wingthing}}
BIN=$STATE/wt-pair
printf '%s\n' "Pairing target: this Mac’s main wing in $WING (the same one wt and the dashboard use)." \
  'Same Wi-Fi: no account needed. Away from home: run wt login once on this Mac; the phone never logs in.'
mkdir -p "$CLONE/.scratch/bin"
(cd "$CLONE" && taskpolicy -b nice -n 15 env CGO_ENABLED=1 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off \
  GOMAXPROCS=2 GOCACHE="$CLONE/.scratch/go-cache" GOMODCACHE="$CLONE/.scratch/go-mod" \
  "$GO" build -p 2 -ldflags '-X github.com/ehrlich-b/wingthing/internal/config.ReleaseChannel=stable' \
  -o .scratch/bin/wt-ios-pair ./cmd/wt) >"$STATE/wing-build.log" 2>&1 \
  || die "Wing build failed; inspect $STATE/wing-build.log. Seed the clone Go cache if dependencies are missing."
install -m 700 "$CLONE/.scratch/bin/wt-ios-pair" "$BIN"
WT=(env -u WINGTHING_PREVIEW_DIR WINGTHING_DIR="$WING" "$BIN")
"${WT[@]}" wing status >"$STATE/wing-status.log" 2>&1 || die 'Main wing status is invalid; inspect it.'
if ! grep -q '^wing daemon is running ' "$STATE/wing-status.log"; then
  "${WT[@]}" wing start --local-only >"$STATE/wing-start.log" 2>&1 \
    || die "Main wing startup failed; inspect $STATE/wing-start.log."
fi
# An existing daemon must already run the pairing-capable binary. Do not replace
# or restart a running main wing while its sessions may be in use.
[[ -f $WING/phone-control.json ]] || die 'Your running main wing needs the pairing-capable binary. Update/restart that wing yourself, preserving this same state directory, then rerun --connect-only.'
step '7/7 Scan this with the Wingthing app'
printf '%s\n' 'Open Wingthing → Scan to pair. Scan this QR, compare all four emoji, confirm on the phone, then press Enter here.' \
  'Keep this Mac awake. The QR expires in five minutes. Ctrl-C cancels. No phone account or browser handoff.'
"${WT[@]}" pair || die 'Pairing did not finish. Press Enter for a new QR or rerun --connect-only.'
