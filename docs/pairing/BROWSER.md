# Join a machine from a browser

On a registered personal machine, run `wt pair --show-link`. Open the trusted
app's `#pair` page and paste the full invitation. The browser shows the machine,
requested inventory-only access, four PAKE emoji with labels and the full wing fingerprint.
Compare all of them with the terminal, select **Match · Join**, and press Enter
in the terminal. **They differ** and **Cancel** stop the exchange.

This first browser slice reads machines and sessions from the wing's `inventory`
operation. The relay marks the browser route, and the wing forces inventory-only
scope even when a client omits scope; wider requests are refused before terminal
approval. The wing records this scope and rejects input, launch, actions, terminal
attachment and remote fleet selectors. `wt devices` shows the granted scope;
`wt devices revoke <name-or-device-id>` ends live access. **Forget** removes this
origin's saved access. Failed commits remove only this tab's own incomplete
candidate. Concurrent tabs preserve each other's candidates and saved pins;
an incomplete record from a previous page can be removed with **Forget incomplete
pairing** directly in the error shown when joining again.

The page opens before account lookup and uses `/ws/browser-phone` on the trusted
app origin. The relay checks the exact configured app Origin, ignores cookie
authority and forwards only bounded opaque `phone.box` envelopes to wings that
advertise browser scope enforcement. Older wings must be updated and restarted
before browser pairing; the page shows **Machine needs a newer wingthing** when
the relay refuses an older wing. The existing
native `/ws/phone` Origin gate and hosted wing admission remain in effect.
Unregistered/LAN-only invitations are unavailable in this slice. Registered
loopback HTTP relays work in the browser's secure localhost context; hosted app
delivery uses HTTPS. Invitation relay metadata is never an arbitrary browser
connection destination.

Each wing gets a non-exportable P-256 WebCrypto signing key and a credential in
origin-scoped IndexedDB, indexed by wing, pin and device ID. Invitation secrets
and ephemeral transport/PAKE keys stay in memory. Initial storage must pass a
write/read/key-use check. After both people approve, the wing keeps a prepared
grant in memory; only the browser's encrypted storage commit admits the device.
Expiry, cancellation and restart discard uncommitted grants. The browser marks
its saved record ready only after commit acknowledgment. An incomplete record
cannot reopen access. Imported invitation fragments are removed from history.

A lost cancellation after a commit can leave an orphaned wing device record;
remove it with `wt devices revoke`. The browser removes its local candidate and
clears inventory on access refusal. A storage failure stops browser completion.
Same-origin hostile code can still use a non-exportable key and read the stored
credential; this is not hardware-backed storage. App delivery and origin security
remain trusted. Site-data deletion requires pairing again. Carrier encryption
has no forward secrecy, and revocation cannot erase previously cached content.

Local verification: `make test-browser-join-go`, `make check-browser-pake` and
`make test-browser-join`. The browser target needs Playwright and local Chromium;
set `PLAYWRIGHT_MODULE` or `CHROMIUM_PATH` when they are outside normal locations.
Its loopback roost uses a short temporary state root and a disposable
repository-local HOME, removes the temporary wing state when it exits, and never contacts a
real relay or provider. Screenshots and proof default to `.scratch/browser-join`;
`WT_JOIN_OUT` and `WT_JOIN_SHOTS` select evidence destinations. It checks both
390×844 and 1440×900, a real terminal session, wider scope requests, real wing-side
five-minute start/commit expiry, re-pairing, wrong pin/secret, storage failures,
revocation, denial, ciphertext replay, concurrent tabs, interrupted-page recovery,
older-wing refusal and cancellation races. Local fixtures do
not establish hosted production acceptance.
