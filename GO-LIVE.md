# Install Wingthing on the owner's phone

The owner or an authorized operator runs these commands for local setup and
phone access. No production deployments, grants, billing changes or public endpoint
are needed. Read [PHONE.md](PHONE.md) for authority and threat boundaries.

## 1. Use the verified local build (about 5 minutes)

```sh
cd /path/to/wingthing-checkout
export WT_PHONE_GO="$(command -v go)"
taskpolicy -b nice -n 15 sh scripts/check-phone.sh
```

The script runs all Node tests, builds embedded web assets, runs gateway race
tests and CLI configuration tests, then builds `.scratch/bin/wt-phone`. It uses
clone-local Go/npm caches. The Go module cache was seeded by a local copy of
the invoking Go module download cache; no secret files were copied. If
moving to my-server, set `WT_PHONE_GO` to its Go **1.26.9** binary and seed that
clone's `.scratch/gomod/cache/download` from its existing module cache, or
deliberately allow dependency downloads with `GOPROXY=https://proxy.golang.org`
and `GOSUMDB=sum.golang.org`. Linux uses `nice -n 15` without `taskpolicy`.

Use a protected executable path outside the agent's writable workspace:

```sh
mkdir -p "$HOME/.local/bin"
install -m 700 .scratch/bin/wt-phone "$HOME/.local/bin/wt-phone-20261011"
export WT_PHONE="$HOME/.local/bin/wt-phone-20261011"
```

## 2. Select the local roost (about 5 minutes if needed)

**Fastest:** reuse an already running, authorized, conversation-enabled local
roost on `127.0.0.1:8080`. Open `http://127.0.0.1:8080/app/` on the Mac and
confirm its wing, agent list and coordinator transcript. Preserve its existing
state, lock and path policy. Do not run the fresh-roost block if it is already
ready. A local-only Unix socket alone is insufficient.

If none exists, create a fresh, restricted personal roost. This explicitly
opts a new wing into conversations without changing an existing wing. It uses
the owner's already authenticated Claude CLI, so check `claude --version` and its
existing login before proceeding. The app's New coordinator form takes an exact
model ID; use the one supported by the owner's installed CLI/provider account.

In Terminal A, from the Wingthing checkout (the subshell stops on any failed check):

```sh
(
set -eu
export WT_PHONE="$HOME/.local/bin/wt-phone-20261011"
export WINGTHING_DIR="$HOME/.wingthing-phone-20261011"
export WT_PHONE_WORK="$HOME/phone-coordinator-work-20261011"
test ! -e "$WINGTHING_DIR" || { echo 'State exists: inspect it instead of overwriting it.'; exit 1; }
test ! -e "$WT_PHONE_WORK" || { echo 'Workspace exists: inspect it instead of overwriting it.'; exit 1; }
umask 077
mkdir -m 700 "$WINGTHING_DIR" "$WT_PHONE_WORK"
cat > "$WINGTHING_DIR/wing.yaml" <<YAML
label: my-phone
paths: ["$WT_PHONE_WORK"]
conversations: enabled
YAML
printf 'fs: ["ro:/", "rw:./"]\nnetwork: none\n' > "$WT_PHONE_WORK/egg.yaml"
env -u GITHUB_CLIENT_ID -u GITHUB_CLIENT_SECRET -u GOOGLE_CLIENT_ID \
  -u GOOGLE_CLIENT_SECRET -u SMTP_HOST -u WT_NODE_ROLE -u WT_APP_HOST -u WT_WS_HOST \
  -u WT_BASE_URL -u WT_ROOST_ALLOWED_EMAILS -u FLY_MACHINE_ID \
  -u WT_JWT_KEY -u WT_JWT_SECRET -u WT_RELAY_POLICY -u WINGTHING_PREVIEW_DIR \
  "$WT_PHONE" roost start --foreground --addr 127.0.0.1:8080 \
  --paths "$WT_PHONE_WORK" --egg-config "$WT_PHONE_WORK/egg.yaml"
)
```

Keep Terminal A open and the Mac awake. A fresh wing can control only the
configured workspace through its normal policy; its provider/tools can read
the `ro:/` filesystem grant, which is sensitive. Existing provider login access
is separate from network access granted to tools. If the broker refuses a
protected path or provider/sandbox admission, stop and inspect that refusal;
do not add unrestricted mounts or unlock an existing wing. If port 8080 is
occupied, reuse its intended roost or select another free loopback port and
change the gateway's `--upstream` below to match. No TLS certificate installation
is needed on the phone: tunn supplies external HTTPS.

## 3. Start private tunn and the authenticated gateway (about 5 minutes)

In Terminal B, substitute **the owner's exact verified GitHub email**, with no
wildcards. [tunn's public site](https://tunn.to/) documents the exact-email
allow-list and GitHub login. The local tunn WebSocket implementation preserves
Host/Origin and authenticates browser upgrades with its email-login cookie;
verify the deployed publisher/relay support it before live acceptance.

```sh
(
set -eu
export WT_PHONE_EMAIL='REPLACE_WITH_YOUR_VERIFIED_GITHUB_EMAIL'
test "$WT_PHONE_EMAIL" != 'REPLACE_WITH_YOUR_VERIFIED_GITHUB_EMAIL'
command -v tunn
if lsof -nP -iTCP:8081 -sTCP:LISTEN; then
  echo 'Port 8081 is occupied: choose another free gateway/tunn port.'
  exit 1
fi
tunn login
tunn 8081 --allow="$WT_PHONE_EMAIL"
)
```

Keep this publisher in the foreground. Copy the printed `https://….tunn.to`
origin, **without `/app/` or a trailing slash**. A random subdomain works;
do not assume a reserved name or paid plan. Starting tunn before the gateway
obtains the actual origin while no upstream control listener exists on 8081.
Never omit `--allow`; ordinary HTTP tunnels without it are public. Do not use
`--bearer`, `--inspect`, MCP grants, domain-wide sharing or public mode here.

In Terminal C, substitute that printed origin:

```sh
(
set -eu
export WT_PHONE="$HOME/.local/bin/wt-phone-20261011"
export WT_PHONE_ORIGIN='https://REPLACE_WITH_PRINTED_ID.tunn.to'
test "$WT_PHONE_ORIGIN" != 'https://REPLACE_WITH_PRINTED_ID.tunn.to'
export WT_PHONE_ACCESS_DIR="$HOME/.wingthing-phone-access-20261011"
test ! -e "$WT_PHONE_ACCESS_DIR" || { echo 'Access directory exists: inspect it before reusing it.'; exit 1; }
umask 077
mkdir -m 700 "$WT_PHONE_ACCESS_DIR"
openssl rand -hex 32 > "$WT_PHONE_ACCESS_DIR/access-key"
chmod 600 "$WT_PHONE_ACCESS_DIR/access-key"
caffeinate -i "$WT_PHONE" phone serve --origin "$WT_PHONE_ORIGIN" \
  --upstream http://127.0.0.1:8080 --addr 127.0.0.1:8081 \
  --key-file "$WT_PHONE_ACCESS_DIR/access-key"
)
```

The gateway pins the printed origin and checks its own key/session on **all**
forwarded HTTP and WebSocket paths, then rewrites authenticated requests for the
roost's existing loopback checks. It does not trust `X-Forwarded-*`, expose the
wing Unix socket, or require changes to wing-control messages. On my-server use
`nice -n 15` as appropriate and omit the Mac-only `caffeinate` wrapper.

If restarting a random-name tunn publisher changes its origin, restart the
gateway with the new exact origin, sign in again, and reinstall/update the
home-screen shortcut. A reserved hostname can be chosen later if already owned.

## 4. Install and check on the iPhone (about 5 minutes)

1. In Safari open **the printed origin plus `/app/`**. Sign in through tunn's
   GitHub OAuth as the allowed verified email. Enable GitHub 2FA/passkeys.
2. The gateway's Wingthing login form then asks for the private access key.
   On the Mac, open `$HOME/.wingthing-phone-access-20261011/access-key` in a
   local editor, copy it privately using Universal Clipboard, and paste it into
   the password field. Never put it in a URL, shared message, screenshot or log.
3. After login, verify the expected wing and coordinator. For the fresh roost,
   tap **New coordinator**, choose the new workspace, give it a label and the
   exact supported Claude model ID, then create it and send an explicit message.
4. Safari → **Share → Add to Home Screen → Add** (enable **Open as Web App** if
   offered). Launch the new Wingthing icon; its first launch may need its own
   tunn/gateway login because Safari and installed apps can have separate cookies.
5. Verify scrolling and the composer with the keyboard open in portrait and
   landscape; send a harmless message to a disposable coordinator. Verify native
   status changes. Tap **Stop execution**, then **Confirm stop**, and observe
   the native process exit. Acknowledgement alone does not establish exit.
6. Background and reopen the app. It refreshes reads and preserves unresolved
   input; it never resends on reconnect. Use **Check receipt** for any unconfirmed
   message. Use **Use Wingthing on your phone → Refresh agents** to check again.

The gateway session expires after **12 hours**. **Phone access and sign out**
opens `/phone/login`, where an active session has a sign-out button. If tunn
or gateway access expires, open `/phone/login` on the same origin to reconnect.
Offline cached transcripts/drafts are readable evidence, not live status or
permission to send. There is no push notification/background job transport.

Before treating this as ready, test an unsigned-out Safari/private window:
`/app/`, `/api/app/wings` and `/ws/relay` must not expose data or admit a socket.
After tunn login alone, app access must still require the gateway key. An
unauthorized email must fail at tunn. Retain no credential-bearing captures.

## Revoke access

**Immediate complete revocation:** press Ctrl+C in Terminal C (gateway) and
Terminal B (tunn publisher). Gateway shutdown cancels active proxy streams and
every in-memory session disappears. The local roost and its agents can keep
running; phone revocation does not stop jobs. Verify the old endpoint is
unreachable and an old session cannot read the API after restart.

To rotate enrollment after stopping the gateway:

```sh
umask 077
export WT_PHONE_ACCESS_DIR="$HOME/.wingthing-phone-access-20261011"
openssl rand -hex 32 > "$WT_PHONE_ACCESS_DIR/access-key.new"
chmod 600 "$WT_PHONE_ACCESS_DIR/access-key.new"
mv "$WT_PHONE_ACCESS_DIR/access-key.new" "$WT_PHONE_ACCESS_DIR/access-key"
```

Restart the authenticated gateway with the same commands and the new key;
restart tunn only with the exact-email allow-list. Changing the file while the
gateway runs does not rotate its loaded key: stop/restart is required. For one
phone, the sign-out form revokes its current cookie and active proxy connections;
other separately signed-in sessions remain valid. For a lost/stolen phone,
stop/restart the gateway to revoke **all** sessions, rotate the enrollment key,
and remove the site's Safari data on available devices. Deleting the icon alone
does not revoke access. Do not infer revocation from an MCP-grant operation;
this route uses browser identity and independent gateway sessions.

For the fresh disposable roost, Ctrl+C in Terminal A separately stops its
runtime. Do not remove state or terminate the owner's unrelated wings during access
revocation. Only the owner may change accounts or production settings.

## Native iOS: exact Xcode handoff and remaining gap

1. Open `/Applications/Xcode.app`, then
   `mobile/ios/Wingthing.xcodeproj`. The owner accepts license/components themselves.
2. Xcode **Settings → Accounts → + → Apple ID**: the owner signs in themselves.
3. Connect/unlock the iPhone, **Trust This Computer**, enable **Settings →
   Privacy & Security → Developer Mode** and restart if requested.
4. Xcode **Window → Devices and Simulators**: select the phone and copy its
   **Identifier/UDID**. In the Wingthing target **Signing & Capabilities**, check
   **Automatically manage signing**, select the owner's **Team**, and enter a unique
   bundle ID such as `dev.example.wingthing` if that Team owns it.
5. Select that phone as the run destination and press **Run**. Allow registration/
   provisioning and device trust prompts deliberately. A Personal Team install
   needs periodic renewal; inspect the profile's expiry in Xcode.

Exact existing Make equivalent, with the Team ID and UDID from Xcode:

```sh
cd /path/to/wingthing-checkout
DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer \
taskpolicy -b nice -n 15 make -C mobile/ios device-install \
  DEVELOPMENT_TEAM='<TEAM_ID>' IOS_DEVICE_ID='<IPHONE_UDID>' \
  BUNDLE_ID='<UNIQUE_BUNDLE_ID>' \
  IOS_SIGNED_DERIVED_DATA="$PWD/.scratch/ios/device-signed"
```

This signs, can register provisioning assets with Apple, and installs; the owner
must perform it. **Signing does not close the transport gap:** the native client
uses a pinned HTTPS Wingthing Home plus existing Wingthing bearer, and cannot
perform tunn browser OAuth or supply this gateway's cookie. It also lacks the
local-only SSH/MCP adapter. Main still does not advertise/handle native
`conversation_stop` / `wingthing.exact_stop.v1`. Native whole-conversation Stop
requires a compatible server implementation and live provider acceptance.
Browser `pty.kill` Stop execution already exists and is the web route used here.
Use an already authorized compatible HTTPS Home for native experiments.

## Verification receipts and operator checks

Recorded local results (2026-10-11): **PASS** for 197 Node tests, Vite build,
gateway race tests, CLI rejection test and final embedded Go build. The tagged
loopback test passed compilation; its execution remains required.

Loopback, Chromium, live tunn, real provider and signed device acceptance remain
required before phone access is considered verified. Run the checks below on
hosts that support their runtime requirements.

An operator runs the real HTTPS/WebSocket echo/revocation test on a host
that allows loopback, before exposure:

```sh
cd /path/to/wingthing-checkout
taskpolicy -b nice -n 15 env GOTOOLCHAIN=local GOMAXPROCS=2 \
  GOCACHE="$PWD/.scratch/go-build" GOMODCACHE="$PWD/.scratch/gomod" \
  GOPROXY=off GOSUMDB=off \
  "$(command -v go)" \
  test -p 2 -race -tags integration ./internal/phoneweb \
  -run '^TestPhoneGatewayLoopbackWebSocket$' -count=1
```

Also required: Chromium's existing desktop/mobile control-suite gate
(Docker and browser runtime needed):

```sh
taskpolicy -b nice -n 15 env GOTOOLCHAIN=local GOMAXPROCS=2 \
  GOCACHE="$PWD/.scratch/go-build" GOMODCACHE="$PWD/.scratch/gomod" \
  GOPROXY=off GOSUMDB=off make e2e-web \
  GO="$(command -v go)"
```

Then run the exact physical Safari/tunn/keyboard/steer/stop journey in step 4. The
existing browser suite is broader shared-roost coverage; it does not establish
Safari installation or gateway authentication. Native gates, on a host that
permits SwiftPM's normal manifest sandbox:

```sh
taskpolicy -b nice -n 15 sh mobile/ios/scripts/offline_validation.sh core
taskpolicy -b nice -n 15 sh mobile/ios/scripts/offline_validation.sh release
DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer \
  taskpolicy -b nice -n 15 sh mobile/ios/scripts/offline_validation.sh xcode
```

The imported native history fix bounds each read to 100 pages / 10 seconds and
preserves a cursor for explicit continuation, with focused synthetic tests.
The recorded native core gate failed before compilation with
`sandbox_apply: Operation not permitted`. Run it on a supported host before
reporting a native PASS. Android has no app build target in this checkout and
is not a native launch candidate.
