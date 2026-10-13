# Phone browser app

Open `/app/` on your personal portal's HTTPS origin. Home leads with the pinned
coordinator's recent native messages and reports, timestamps, file chips and
observed wake plan. Below it, sessions group by execution machine. Search spans
names, repositories and imported transcript text. Exact `(origin_wing_id,
session_id)` identities preserve collisions; direct and gateway routes merge
only with matching origin identities. A healthy direct route wins; a verified
personal gateway is the fallback. Failed sources show incomplete coverage.

Use Safari's Share → Add to Home Screen, or the Install Wingthing button in
browsers that offer it. The manifest includes PNG icons and standalone display.
The service worker performs network-only app navigation and returns a constant
disconnected page when the network fails. It stores no requests, responses,
credentials or session data in Cache Storage. Auth and APIs bypass the worker.
Existing owner-scoped browser transcript/receipt storage remains; this does not
promise a browser with no local data. An installed browser still needs to renew
its login after expiry.

Tap Open session to read a live transcript and send a correlated message. A
message whose delivery is uncertain keeps its original request ID for recheck;
it is never automatically submitted through another route. Permission attention
is shown honestly: providers currently lack a typed approval responder. In
Preview, take terminal control and enter the provider's requested decision, then
hand back. Hand-back leaves the agent running. Current preview eggs and wings
are required for the exclusive input lease; older runtime processes must be
re-created under the reviewed preview release to use takeover. See
[session-takeover.md](session-takeover.md). File chips open the existing direct
session file panel; gateway-only file download is unavailable and labeled.

## Personal tunn SSO

The personal roost has an opt-in verifier for tunn's existing signed app
assertions. It accepts **only one configured tunn account UUID**, regardless of
email text or app sharing. It maps that authenticated owner to the existing local
execution owner; it does not create a browser account or enroll shared users.
Use the exact HTTPS app hostname and issuer and a **public-only** Ed25519 JWKS
file. Fetch that file from the configured authority's public JWKS endpoint over
verified HTTPS, inspect the configuration, then supply:

```sh
export WT_TUNN_ORIGIN=https://wing.apps.example.com
export WT_BASE_URL="$WT_TUNN_ORIGIN"
export WT_TUNN_ISSUER=https://tunn.to
export WT_TUNN_ACCOUNT_ID=YOUR_CANONICAL_OWNER_UUID
export WT_TUNN_JWKS_FILE="$HOME/.config/wingthing/tunn-public-jwks.json"
wt roost start --foreground --addr 127.0.0.1:8080
```

This mode requires a personal loopback roost without OAuth, an organization, or
the local CA mode. Incomplete settings fail startup. Non-local Host requests
require `X-Tunn-Identity`: EdDSA signature, known key ID, exact issuer/audience,
matching subject/account UUID, verified mailbox, not-before, issued-at and an
expiry no more than 120 seconds after issue. Unsigned user/email headers confer
nothing. Foreign browser origins and cross-site fetches are rejected. Without
this explicit mode, the existing localhost-only Host and browser mutation gates
remain unchanged. The embedded wing connects over loopback with its existing
credential. Keep the publisher target on loopback.

The JWKS file is re-read for every external request. Rotate it atomically with a
fresh public JWKS; missing or invalid keys reject new requests. tunn's ingress
must continue to enforce current app ACLs, account state, cookie expiry and
stream revocation. The short assertion is not an independent revocation service.
The app's SSO responses use `private, no-store`.

Enable remote inventory only deliberately with `wt wing config set
phone_fleet=true` on the personal gateway, and keep existing remote identity pins,
client grants, path and passkey policy. CLI `wt search`, `wt session action` and
MCP `session_search`/`session_action` are the typed surfaces behind the same UI.

A later knock device credential authenticates the native app to **tunn**, in a
separate header on HTTPS and WebSocket upgrade. It replaces the browser cookie
at that ingress; tunn still checks account and hostname policy and signs the
same assertion. Wingthing's encrypted carrier, wing identity, pairing, grants
and approval gates remain. Native knock issuance and storage are not provided
by this PWA.

`make test-pwa`, `make test-pwa-web`, `make check` and the normal promotion gates
exercise this candidate using disposable state, fixture providers and fixture
SSH. Those results do not establish public tunn, a physical phone, a native
macOS sandbox, or real-provider acceptance.
