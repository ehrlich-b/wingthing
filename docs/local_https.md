# Local HTTPS design

Status: implemented

Reviewed: 2026-08-25

## Outcome

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

## Why there are two listeners

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

## Certificate material

WT creates these files under `WINGTHING_DIR/local-tls`:

| File | Contents | Mode |
| --- | --- | --- |
| `ca-key.pem` | ECDSA P-256 CA private key | `0600` |
| `ca.pem` | public self-signed CA certificate | `0644` |
| `localhost-key.pem` | ECDSA P-256 server private key | `0600` |
| `localhost.pem` | public server certificate | `0644` |
| `trusted` | successful user trust-store marker | `0600` |

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

## Trust ceremony

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
| macOS | login keychain user trust settings |
| Windows | current-user Root certificate store |
| Linux | Chromium NSS database at `~/.pki/nssdb` |

Linux needs `certutil` from `libnss3-tools` or `nss-tools`. WT initializes a
missing Chromium NSS database without a password and without root.

The successful marker prevents a daemon or temporarily locked macOS login
keychain from reopening the ceremony on every start. `wt local-cert remove`
removes this precise public root and clears the marker. It leaves the keys on the
box so a running listener is not broken and WT cannot silently replace an
authority that was previously trusted.

## Address and mode safety

When `--https` is selected:

- the implicit `:8080` default becomes `127.0.0.1:8080`;
- both supplied addresses must be explicit loopback addresses with nonzero ports;
- wildcard, LAN, DNS, and public hosts are rejected before any key is created;
- HTTP and HTTPS may not resolve to the same loopback socket;
- the browser-facing base URL becomes `https://localhost:8443`; and
- a stale public `WT_BASE_URL` cannot change that local origin.

Compatibility is deliberately opt-in:

| Deployment | Result |
| --- | --- |
| Existing `wt serve` / Fly edge or login node | unchanged |
| Existing local HTTP serve/roost | unchanged unless `--https` is added |
| OAuth, organization, or public shared roost | keeps external HTTPS; local CA mode is rejected |
| Single-user local serve/roost with `--https` | dual loopback listeners and local trust ceremony |

Hosted `WT_BASE_URL=https://...` remains authoritative whenever local HTTPS is
not selected.

## Security boundary

HTTPS protects browser-to-local-gateway traffic and supplies a conventional
secure browser origin. It does not replace Wingthing's browser-to-wing
application encryption, wing authentication, SSH authentication, or the egg
sandbox.

The CA private key has the power to issue another localhost certificate on this
one profile. Protecting the owning OS account and `WINGTHING_DIR` remains part of
the trust boundary. A compromised gateway still serves the browser JavaScript
and is therefore inside the client trust boundary even when terminal payloads
are application-encrypted.

## Regression gates

The automated battery covers certificate constraints, SANs, chain verification,
root reuse, leaf rotation, corrupt-state behavior, expiry behavior, permissions,
symlink refusal, atomic trust markers, failed trust commands, idempotence, all
platform command arguments, Linux NSS initialization, and the invariant that no
trust command receives a private-key path.

Listener tests cover unsafe-address refusal before key creation, default address
rewriting, loopback alias collisions, ordinary HTTP and HTTPS handler parity,
trusted and untrusted TLS clients, local passkey origins, hosted base URL
preservation, opt-in flags, and mode rejection.

The ordinary full repository gate and Docker-backed shared-roost browser battery
remain required. The shared-roost battery exercises multiple users, role paths,
ACL denial, persistent terminal replay, mobile views, and existing organization
API behavior without selecting local HTTPS.
