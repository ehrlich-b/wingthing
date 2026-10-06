# Fly Operations Guide

Wingthing's Fly app is the public coordinator: identity, the authorized wing
directory, key exchange, WebRTC signaling, the portal, and the optional encrypted
relay. Agents execute only on a user's selected wing. A Fly machine is not an
execution wing and does not receive a user's workspace or provider credentials.

## Checked-in topology

The active `fly.toml` is deliberately login-only:

- `[processes].login` is enabled;
- `[processes].edge` is commented out;
- `[http_service].processes` contains only `"login"`;
- the `wt_data` volume and the configured VM stanza apply only to `login`; and
- the service keeps at least one login machine running.

The active service attachment is therefore:

```toml
processes = ["login"]
```

That means an ordinary `fly deploy` of the checked-in file does not create or route
traffic to an edge process. Verify the live machine count with `make ops ACTION=status`; the
configuration is the deployment target, not evidence of what an earlier manual
scale command left running.

The login process owns the SQLite volume at `/data` and serves the public HTTP and
WebSocket surface. The binary also contains an optional edge role. An edge has no
durable volume, skips the relay database, proxies login-owned HTTP/API work to the
login process, and keeps synchronized session, wing, and entitlement caches.

On Fly, an unset `WT_NODE_ROLE` is inferred before configuration loading: an
already-mounted `/data` directory means `login`; its absence means `edge`. This
ordering matters because configuration initialization itself may create state below
`/data`. An edge with `FLY_APP_NAME` and no explicit `WT_LOGIN_ADDR` derives
`http://login.process.<app>.internal:8080`. An explicit `WT_NODE_ROLE` or
`WT_LOGIN_ADDR` takes precedence.

## Placement and durable state

| Decision | Public Fly deployment |
| --- | --- |
| **Execution wing** | The access-filtered wing explicitly selected by `wing_id`. The coordinator never substitutes a Fly process as the execution target. |
| **Workspace** | An existing `cwd` on the selected wing. The service does not clone or synchronize repositories or untracked files. |
| **Display** | `agent_run` returns semantic state over direct MCP. `agent_start` creates a persistent PTY; hosted browser/control relay is entitlement-gated, while CLI, SSH, and self-hosted displays remain separate paths. |
| **Provider credentials** | The execution owner's agent home on the selected wing. Fly secrets are service credentials, not Claude, Codex, SSH, or other user provider credentials. |
| **Durable memory** | The login volume stores gateway account, organization, auth, entitlement, and routing records. Each wing remains authoritative for its task database, sessions, provider history, optional Wingthing memory, and workspaces. |

The public `direct-free` relay policy changes transport entitlement, not ownership
or organization authorization. Wing-side roles, paths, grants, and bounds still
apply.

## Internal-node trust

The `/internal/*` API accepts a network caller without `WT_INTERNAL_SECRET` only
when the receiving process is configured as a Fly app machine and the request
arrives from a cluster-private address. That trusts the Fly organization's private
network boundary; it is not cryptographic caller authentication. Set the same
separate `WT_INTERNAL_SECRET` on every process if other applications in that Fly
organization are not equally trusted. A split non-Fly deployment must set the
secret and keep the node transport private and encrypted where it can cross an
untrusted network. Do not reuse `WT_JWT_KEY` as the internal secret.

## One-time setup

Confirm the app and region in `fly.toml`, create the login volume, and
generate an EC P-256 signing key so wings can authenticate against any public
process:

```sh
fly apps create wingthing
fly volumes create wt_data --region ewr --size 1
fly secrets set WT_JWT_KEY=$(wt keygen)
```

## Release and deploy the login-only configuration

The public website and installer are one versioned contract. Publish and verify the
matching GitHub release before deploying a site that documents it. From the exact
commit being promoted:

```sh
git tag vX.Y.Z
git push origin vX.Y.Z
# wait for the release workflow to publish all five assets
gh release view vX.Y.Z
curl -fsSL https://wingthing.ai/install.sh | sh
make deploy
```

`make deploy` runs `check` and the default `gate` profiles: web and Go unit
tests/build, integration fixtures, vet, race on touched packages, vulnerability
checks, release command-surface contract, and the configured historical-baseline
compatibility gate before
`fly deploy`; it does not publish a GitHub release. The compatibility script uses
`WT_COMPAT_BASELINE_REF` as a single override baseline when set and otherwise runs
both pinned defaults, `v0.144.1` and `v0.147.0`, declared in
`scripts/test-backward-compat.sh`. It exercises each selected baseline and the candidate
in both gateway/wing orders; it is not a claim that either pin is always the immediately
previous release, and it does not exercise a mixed Fly login/edge fleet.

Deploying the site before the matching release creates an installation outage: the
newer installer refuses an older binary whose command surface does not match the
site. Confirm that the installed binary reports `vX.Y.Z` before continuing.

The new `direct-free` restriction is not a completed security boundary until every
public gateway process is current. Check every live machine and image digest before
declaring the policy active.

## Hosted relay policy

`wt serve` defaults to the backward-compatible `legacy` policy so an upgrade does
not silently change a private gateway. The checked-in Fly configuration explicitly
sets `WT_RELAY_POLICY=direct-free`: free accounts can use login, the authorized wing
directory, key exchange, bounded discovery/passkey messages, and WebRTC signaling,
but the gateway denies PTY and general control payload relay. Accounts with relay
access retain that hosted browser transport.

On `direct-free`, the historical billing-free personal and organization upgrade
endpoints are disabled, and the account UI does not grant relay access. Existing
entitlements and cancellation paths remain valid; new relay access must come from
the deployment's billing or operator workflow. Private legacy gateways and
self-hosted roosts retain their operator-controlled relay behavior. A wing with
`hosted_relay: deny` refuses relayed payloads even when the account or self-hosted
gateway would otherwise permit them.

The checked-in configuration also sets the migration cutoff explicitly:

```text
WT_RELAY_MIGRATION_BEFORE=2026-08-26T00:00:00Z
```

Accounts created on or before that instant retain temporary relay parity while the
migration rule is active. If the value changes, deploy the same RFC3339 value to all
gateway processes. Startup fails when the current and deprecated cutoff variable
names are both present with different values. The logged-in API publishes
`relay_allowed` and `relay_reason`; an enabled edge synchronizes the login node's
decision.

## Enable the optional edge group

Do this only when edge capacity is intentionally part of the deployment. Two edits
are required in `fly.toml` before scaling:

1. Uncomment `[processes].edge`.
2. Change the HTTP service attachment to:

   ```toml
   processes = ["login", "edge"]
   ```

Then deploy that configuration before creating edge machines:

```sh
fly deploy
make ops ACTION=status
make ops ACTION=deploy-edge REGIONS=nrt COUNT=1
make ops ACTION=deploy-edge REGIONS=lhr COUNT=1
```

`make ops ACTION=deploy-edge` only changes the edge count in the named regions. It now refuses
to run while the process command is commented out or the HTTP service excludes
`edge`. After scaling, use `make ops ACTION=status`, check `/health` through the public service,
and inspect each process's startup log. Edge logs must say `auto-detected node role:
edge` and name the derived or configured login address; login logs must say
`auto-detected node role: login`.

Current edges proxy portal HTML and hashed static assets to login, so those assets
come from the login release. When introducing that proxy rule to a fleet containing
older edges, update and verify edges before updating login; after that transition,
keep login and edge on the same promoted image rather than assuming the historical
compatibility pin covers mixed edge releases.

To set total counts across the process groups after edge is enabled:

```sh
make ops ACTION=scale LOGIN=1 EDGE=3
```

To remove Tokyo edges, or every edge respectively:

```sh
fly scale count edge=0 --region nrt
make ops ACTION=scale LOGIN=1 EDGE=0
```

After the last edge is removed, return `fly.toml` to its checked-in login-only form
if edge is no longer an intended deployment option: remove `"edge"` from
`http_service.processes`, comment the edge command, and deploy that configuration.

## Optional edge request path

When the edge group is enabled and attached to the HTTP service:

1. the login machine is the only process with `wt_data`;
2. an edge starts without `/data`, detects `edge`, and skips SQLite;
3. login-owned HTTP and API work is proxied over the private Fly address;
4. the edge synchronizes login-owned session, entitlement, and wing state; and
5. a WebSocket for a wing connected elsewhere can receive a `fly-replay` response
   that asks Fly to replay the upgrade on the owning machine.

These are optional code paths, not a description of the active checked-in topology.

## Self-hosted contrast

The simplest self-hosted deployment is one process with no `WT_NODE_ROLE`,
`FLY_MACHINE_ID`, gossip, or `fly-replay`: use `wt roost start` for a portal,
gateway, and embedded wing, or `wt serve` for the gateway alone. A private OAuth
gateway or roost should set `WT_ROOST_ALLOWED_EMAILS`; OAuth authenticates an
account but does not enroll it in a private service. Self-hosted relay policy is
operator-controlled and does not depend on a wingthing.ai hosted-relay entitlement.

Configure OAuth, email and optional billing secrets for this installation.
When endpoints use separate hosts, set `WT_APP_HOST` and `WT_WS_HOST`
consistently and provision their external DNS/TLS. User provider credentials
belong only on the execution wing.

## Self-hosted roost operation

### Start a roost

```bash
wt roost start --https
open https://localhost:8443
```

The command starts the gateway, waits for it to become ready, then starts an
embedded wing against the local gateway. Both share one process lifecycle and
log stream. `--https` creates a localhost-only CA and leaf certificate on demand.
The CA private key remains mode `0600` in `~/.wingthing/local-tls` and never leaves
the host. WT installs only the public CA certificate in the current user's trust
store. Port 8443 is for the browser; the embedded wing uses the separate
loopback-only HTTP endpoint on port 8080.

Common operations:

```bash
wt roost start --https
wt roost status
wt roost stop
wt roost start --foreground --https
wt local-cert status
wt local-cert remove
```

Without OAuth configuration, the portal uses local single-user mode. Local HTTPS
is opt-in. Omitting `--https` keeps HTTP, while the single-user/no-login default
binds to `127.0.0.1:8080` and explicitly non-loopback local addresses are refused.
Authenticated shared-roost listeners retain their configured bind behavior.

With a public HTTPS URL and an OAuth provider, it becomes a multi-user shared
runtime:

```bash
WT_BASE_URL=https://lab.example.com \
GITHUB_CLIENT_ID=... \
GITHUB_CLIENT_SECRET=... \
WT_ROOST_ALLOWED_EMAILS=alice@example.com,bob@example.com \
wt roost start --addr :8080
```

That public/shared command does not create or install a local CA. Its existing
external TLS ingress and OAuth configuration are unchanged. OAuth proves account
identity; the exact email list is the application enrollment boundary. If it is
omitted, any account accepted by the provider can enroll, so an equivalent
membership restriction is required at the provider or ingress.

Set project roots, labels, sandbox defaults, and audit policy through the same
wing configuration used by a standalone wing.

### Claude deployment policy and personal state

Each authenticated user's Claude profile belongs to that user. Its writable
`CLAUDE_CONFIG_DIR` is the user's isolated `.claude` directory; onboarding,
theme, project trust, credentials, and preferences persist there across sessions.
Wingthing does not copy the service account's Claude profile over it or restore
the host's old settings when an isolated Claude session exits. Personal local
sandboxes retain their existing host-config snapshot protection.

For isolated Claude launches, the service account's `~/.claude/settings.json`
supplies only the deployment `model`, `effortLevel`,
`env.CLAUDE_CODE_EFFORT_LEVEL`, and `env.DISABLE_AUTOUPDATER` when it is `"1"`
(a host-managed install is read-only inside the jail, so its updater can only
fail there). Wingthing passes the model with `--model` and the rest with
`--settings`,
without rewriting the user's saved settings or exposing the host settings file
inside the jail. Existing deployments using Sonnet 4.6 and environment effort
`max` keep that policy:

```json
{
  "model": "claude-sonnet-4-6",
  "effortLevel": "high",
  "env": { "CLAUDE_CODE_EFFORT_LEVEL": "max" }
}
```

Changing this file affects subsequent launches without resetting profiles.
Already-running sessions are not restarted. Explicit session model arguments
remain supported. A malformed or unreadable policy stops the launch with an
error; an absent policy retains Claude's ordinary defaults.

This is a model default, not a model-access security boundary. Host credentials
still use the existing private credential helper, and filesystem/owner authority
still comes from the shared-host jail and authenticated resource checks. Neither
arbitrary host settings nor another user's settings are inherited.

### Run components separately

Use separate processes when the gateway and execution runtime belong on
different machines:

```bash
## gateway only
wt serve

## wing on another machine
wt login --roost https://portal.example.com
wt start --roost https://portal.example.com
```

Each wing chooses one gateway URL in its current profile. Use a separate
`WINGTHING_DIR` when one machine needs independent login and key state for
another portal.

### Multi-wing behavior

A roost gateway can register the embedded wing and external wings.

- The browser receives its authorized wing roster, probes each wing through the
  encrypted tunnel, and can show sessions from several wings.
- HTTP MCP `wing_list` receives that same access-filtered roster and marks only
  the embedded wing as currently controllable.
- Native `wt mcp connect` receives the roster and controls an explicitly selected
  external wing over a direct WebRTC data channel.
- `wt wings --roost URL` can query the same roster and encrypted wing metadata.
- The roost's HTTP MCP endpoint currently calls only the embedded wing because
  its native tools have no `wing_id` target.

The HTTP adapter remains embedded-wing-only for compatibility. The native direct
adapter is the multi-wing agent-manager path and has no mutable current wing.

Independent roosts do not discover or federate with one another. A client
selects a roost by URL. An LLM can register several HTTP MCP URLs under separate
names and choose the name explicitly.

Internal peer-directory and edge-routing code coordinates replicas of one
hosted gateway. WebRTC may connect a browser directly to a wing. Neither is
peer-roost discovery.

### Security model

The gateway and embedded wing remain logical trust domains even though they
share a process. Terminal and encrypted tunnel payloads use the same
application protocol as a standalone wing.

For a self-hosted roost, the machine operator controls both components and can
read the wing's files, process memory, workspaces, and provider credentials.
Application encryption protects the route and network boundary; it cannot
protect a user from the host or hypervisor administrator.

On a shared host, Wingthing gives each enrolled owner a separate agent home
and enforces owner-scoped resources. This is useful multi-user policy, not a
claim of protection from root.


## Local HTTPS

Status: implemented

Reviewed: 2026-08-27

### Outcome

A person can run a single-user self-hosted portal with:

```bash
wt roost start --https
open https://localhost:8443
```

or run the gateway separately for a remote wing:

```bash
wt serve --local --https
```

The first invocation creates a localhost-only certificate authority on demand
and asks the operating system to trust its public certificate for the current
user. WT says what it is doing before the trust command runs. The CA private key
never leaves the Wingthing profile.

### Why there are two listeners

```text
browser ===== HTTPS :8443 ===== local gateway
                                    |
wing ===== loopback HTTP :8080 =====+
```

The browser needs a trusted secure origin. A local wing, embedded wing, or remote
wing arriving through an SSH reverse forward needs an endpoint it can reach
without trusting the browser computer's private CA.

Both listeners use the same relay handler and bind only to loopback in local
HTTPS mode. The ordinary HTTP endpoint is therefore a host-local transport, not
a LAN service. For a remote wing, SSH authenticates and encrypts the segment
between hosts before it reaches that loopback endpoint.

The two-listener design avoids copying the CA certificate or any private key to a
remote Linux or WSL machine. It also preserves `wt start --local` and the existing
reverse-forward recipe.

### Certificate material

WT creates these files under `WINGTHING_DIR/local-tls`:

| File | Contents | Mode |
| --- | --- | --- |
| `ca-key.pem` | ECDSA P-256 CA private key | `0600` |
| `ca.pem` | public self-signed CA certificate | `0644` |
| `localhost-key.pem` | ECDSA P-256 server private key | `0600` |
| `localhost.pem` | public server certificate | `0644` |
| `trusted` | successful, platform-verified user trust-store marker | `0600` |

The directory is mode `0700`. Writes use a temporary file and atomic rename.
Existing CA material is never silently replaced: incomplete, corrupt, mismatched,
not-yet-valid, or expired CA state fails with an explicit error. A corrupt or
near-expiry leaf may be regenerated under the same CA.

The CA has a zero-length intermediate path and critical name constraints for:

- `localhost`;
- `127.0.0.0/8`; and
- `::1`.

The leaf contains only `localhost`, `127.0.0.1`, and `::1` SANs and server-auth
usage. The root is valid for ten years; the leaf rotates when fewer than thirty
days remain.

### Trust ceremony

The explicit `--https` flag is consent to create and install this local material.
Before installation WT prints:

- the CA private-key path and mode;
- the public certificate path;
- the localhost-only constraint;
- that only the public certificate enters the trust store; and
- on macOS, that a native Certificate Trust Settings dialog may appear.

Platform destinations are:

| Platform | Current-user destination |
| --- | --- |
| macOS | explicitly selected `~/Library/Keychains/login.keychain-db` user trust settings |
| Windows | current-user Root certificate store |
| Linux | Chromium NSS database at `~/.pki/nssdb` |

Linux needs `certutil` from `libnss3-tools` or `nss-tools`. WT initializes a
missing Chromium NSS database without a password and without root.

WT checks the platform destination before writing the successful marker. On
macOS it evaluates the generated leaf with the SSL policy, the `localhost` name,
and the explicit login keychain. Windows and Linux confirm the precise root in
their current-user browser stores. A failed check is never recorded as trusted.
Markers made by older builds are reinstalled and upgraded once. The verified
marker then prevents a daemon or temporarily locked macOS login keychain from
reopening the ceremony on every start. `wt local-cert remove` removes this
precise public root and clears either marker version. On macOS removal clears
both the trust setting and the public certificate. It leaves the keys on the box
so a running listener is not broken and WT cannot silently replace an authority
that was previously trusted.

### Address and mode safety

When `--https` is selected:

- the implicit `:8080` default becomes `127.0.0.1:8080`;
- both supplied addresses must be explicit loopback addresses with nonzero ports;
- wildcard, LAN, DNS, and public hosts are rejected before any key is created;
- HTTP and HTTPS may not resolve to the same loopback socket;
- the browser-facing base URL becomes `https://localhost:8443`; and
- a stale public `WT_BASE_URL` cannot change that local origin.

The certificate ceremony is deliberately opt-in. Local listener hardening also
applies to the HTTP-only form of the same no-login mode:

| Deployment | Result |
| --- | --- |
| Existing `wt serve` / Fly edge or login node | unchanged |
| Existing single-user local HTTP serve/roost | stays HTTP; implicit `:8080` becomes `127.0.0.1:8080`, explicit non-loopback binds are rejected |
| OAuth, organization, or public shared roost | keeps external HTTPS; local CA mode is rejected |
| Single-user local serve/roost with `--https` | dual loopback listeners and local trust ceremony |

Hosted `WT_BASE_URL=https://...` remains authoritative whenever local HTTPS is
not selected.

The relay independently rejects non-loopback Host headers in local mode, so a DNS
rebinding hostname cannot turn the loopback service into its own origin. Browser
WebSocket upgrades must be same-origin. Unsafe browser requests must carry the
same exact scheme, host, and port when they include Origin, and requests marked
cross-site by `Sec-Fetch-Site` are rejected. Origin-less native wing and CLI
requests remain compatible. Authenticated hosted and organization topologies keep
their existing cross-host WebSocket behavior.

### Security boundary

HTTPS protects browser-to-local-gateway traffic and supplies a conventional
secure browser origin. It does not replace Wingthing's browser-to-wing
application encryption, wing authentication, SSH authentication, or the egg
sandbox.

The CA private key has the power to issue another localhost certificate on this
one profile. Protecting the owning OS account and `WINGTHING_DIR` remains part of
the trust boundary. A compromised gateway still serves the browser JavaScript
and is therefore inside the client trust boundary even when terminal payloads
are application-encrypted.

### Regression gates

The automated battery covers certificate constraints, SANs, chain verification,
root reuse, leaf rotation, corrupt-state behavior, expiry behavior, permissions,
symlink refusal, atomic trust markers, failed install and verification commands,
legacy-marker migration, idempotence, macOS trust-rule and certificate removal,
all platform command arguments, Linux NSS initialization, and the invariant that
no trust command receives a private-key path.

Listener tests cover unsafe-address refusal before key creation, default address
rewriting in both HTTP and HTTPS local modes, loopback alias collisions, ordinary
HTTP and HTTPS handler parity, trusted and untrusted TLS clients, local passkey
origins, Host-header/DNS-rebinding refusal, same-origin mutation and WebSocket
rules, hosted base URL preservation, opt-in flags, and mode rejection.

The ordinary full repository gate and Docker-backed shared-roost browser battery
remain required. The shared-roost battery exercises multiple users, role paths,
ACL denial, persistent terminal replay, mobile views, and existing organization
API behavior without selecting local HTTPS.

## Phone coordinator through wingthing.ai

Use a **stable build of this branch**, an awake, online Mac, and an installed/authenticated Claude CLI. The separate wing uses the Mac's normal provider login; its Wingthing state, bearer, key, sessions and daemon files are isolated. Preview remains loopback-only. From this clone, build into a new executable outside the coordinator workspace:
```sh
nice -n 15 make web
mkdir -p "$HOME/.local/bin"
nice -n 15 go build -p 2 -ldflags '-X github.com/ehrlich-b/wingthing/internal/config.ReleaseChannel=stable' -o "$HOME/.local/bin/wt-phone" ./cmd/wt
WT_PHONE="$HOME/.local/bin/wt-phone"
export WINGTHING_DIR="$HOME/.wingthing-phone"
test ! -e "$WINGTHING_DIR" || exit 1 # choose another fresh directory if it exists
"$WT_PHONE" channel --json # must report stable
mkdir -p "$HOME/phone-coordinator-work"
printf 'fs: ["ro:/", "rw:./"]\nnetwork: none\n' > "$HOME/phone-coordinator-work/egg.yaml"
"$WT_PHONE" login --roost https://wingthing.ai # approve device login as the phone's account
cat > "$WINGTHING_DIR/wing.yaml" <<YAML
wing_id: $(cat "$WINGTHING_DIR/wing-id")
roost: https://wingthing.ai
label: phone-coordinator
paths: ["$HOME/phone-coordinator-work"]
conversations: enabled
hosted_relay: allow
locked: false
allow_keys: []
YAML
chmod 600 "$WINGTHING_DIR/wing.yaml"
"$WT_PHONE" wing start --roost https://wingthing.ai
"$WT_PHONE" phone link # copy the single setup URL from stdout
```
Keep `WINGTHING_DIR` exported for every command here, including later stop/status commands. Do not copy state from the existing local roost. Keep state and executable under the OS home, outside writable workspaces, TMPDIR and provider profile paths; the broker refuses exposed layouts instead of adding sandbox exceptions.

In iOS **Home**, tap **Paste setup link**, then **Connect**. Universal Clipboard works; tapping or AirDropping the link opens the prefilled form without connecting. The account and pinned wing identity are checked before the bearer enters Keychain. The link contains a credential: keep it private. Use `phone link --no-token` to enter the token separately, or `--json` for a JSON `url`. Older logins check `/auth/check` only at the configured roost; its account is then saved locally with the token. Manual fallback: enter these values in **Home** (the bearer is `device_token` in this wing's private `$WINGTHING_DIR/device_token.yaml`):

| Form field | Value |
| --- | --- |
| Transport | Hosted home I choose |
| HTTPS home address | `https://wingthing.ai` |
| Account ID | `user_id` in this wing's `device_token.yaml`, or `/auth/check.user_id` |
| Home wing ID | `<wing-id>` from `$WINGTHING_DIR/wing-id`, matching the roster |
| Home wing public key | `public_key` in this wing's `device_token.yaml`, matching the roster |
| Existing access token | `<bearer-token>`, without the `Bearer ` prefix |

Connect Home, tap **New conversation**, choose `phone-coordinator-work`, enter `coordinator`, an exact supported `<claude-model-id>` and your first message, then tap **Create conversation**. The phone creates the headless Claude root under the hosted account's owner; its logical conversation persists after the turn exits. The broker retains its fixed tool subset, finite bounds and existing sandbox protections.

After the turn ends, wait for **Ready for follow-up**, then send your next message. `session_read` and `conversation_read` advertise `headless_continuation`; iOS calls `agent_start` with `resume_session`, `conversation_role: parent`, `input` and an immutable `request_id`. Each follow-up creates a new execution of the same conversation/provider session/model. If unconfirmed, retry the saved message rather than creating another conversation.

Fallback: start an interactive `agent_start` parent (omit `-p`) through `"$WT_PHONE" mcp stdio --client <phone-owner>` on the Mac. `<phone-owner>` is `user-` plus the first 20 hex characters of SHA-256 of the exact `<account-id>`. Open it in iOS and keep its execution alive for `session_prompt` follow-ups.

Native passkey approval is not implemented: locked wings need a locally pinned user passkey and an encryption-identity-bound auth token; unlocked wings also demand this for enrolled users. Use this fresh unlocked wing with no pinned passkeys; preserve the existing roost's lock.

On wingthing.ai's login-node database, Bryan needs `entitlements.user_id='<account-id>'` with `entitlements.subscription_id=subscriptions.id` and `subscriptions.status='active'` unless migration-eligible. Bryan can run this read-only SQL there; at least one returned row satisfies `IsUserPro`:
```sql
SELECT e.id, e.user_id, e.subscription_id, s.status
FROM entitlements e
JOIN subscriptions s ON s.id = e.subscription_id
WHERE e.user_id = '<account-id>' AND s.status = 'active';
```
`users.tier='pro'` alone is insufficient. The alternative is `users.created_at` no later than the running `WT_RELAY_MIGRATION_BEFORE` cutoff (checked in: `2026-08-26T00:00:00Z`). `hosted_relay: allow` cannot grant entitlement; missing rows require operator/billing action.

The inspected `origin/main` (`72c7be5`) already routes these encrypted operations; no relay code deploy is needed. The running hosted deployment/account was not verified. The [hosted native path](agent-manager-product-brief.md#hosted-native-path) lists all admission checks. No production change is made here.
