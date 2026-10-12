The acceptance tests remain unchanged and report their actual Go PASS/FAIL
results. REQUIRED-UNRUN below identifies native evidence that this Linux host
cannot supply; it does not convert a missing product feature into a pass.

All 18 phone cards still fail at the owner CLI prerequisite: `wt pair` is
absent, except cards 6 and 11 which require the absent `wt devices` command.
The existing iOS bearer/account client is not the storyboard's wing-specific
PAKE pairing and device credential protocol. No invitation, device grant,
camera acceptance or cryptographic acceptance is claimed here. Even after a
future owner CLI implementation, `storyboardPhone` deliberately fails with
REQUIRED-UNRUN until physical iPhone evidence is integrated. Printing a pairing
screen or inventing empty device records cannot satisfy the specification.

| Card | Remaining physical-phone/product obligation |
|---|---|
| 05/card-0 | Main wing identity, genuine invitation, six random words, 300-second lifetime |
| 05/card-1 | Camera scan reaches the exact endpoint and pin without a phone account |
| 05/card-2 | Transcript-bound emoji and two independent confirmations |
| 05/card-3 | Device-only key/credential storage and reviewed SPAKE2 encrypted grant |
| 05/card-4 | Wing-bound inventory/attention with session-only control rights |
| 05/card-5 | Encrypted pinned relay reach without phone login |
| 05/card-6 | Independent pairing pins, registries and grants on each machine |
| 05/card-7 | Expired/used invitation refused; fresh ID, secret and deadline |
| 05/card-8 | Free direct-route fallback, no credential sent on failed route |
| 05/card-9 | Pin mismatch hard stop without replacement or continuation |
| 05/card-10 | Emoji mismatch/cancel burns invitation and discards approvals |
| 05/card-11 | Durable revocation closes live access on exactly the owning wing |
| 05/card-12 | Explicit input with unknown delivery handling; reconnect never replays |
| 05/card-13 | Exact session stop confirmation/termination receipt; detach preserves work |
| 05/card-14 | Explicit notification consent and generic lock-screen content |
| 05/card-15 | LAN/tailnet direct pinned control stays free without relay/account |
| 05/card-16 | Permission Settings/rescan and private endpoint + public pin + code fallback |
| 05/card-17 | Original encrypted success recoverable with exactly one persisted grant |

For runs cards 1 and 5, routing and transport subcases pass. Their native
subcases require a scoped host mailbox, whose provider-write model and final
protected-write-target checker only support macOS Seatbelt. Linux deliberately
refuses this contract before starting a provider. The new diagnostic preserves
that reason and the selected policy rather than suggesting a generic policy
retry. Native macOS acceptance is **REQUIRED-UNRUN**. Linux scoped host-mailbox
support remains unavailable; no isolation fallback was added. Both card tests
therefore still fail, and neither corresponding bug is closed.

The real Linux shell smoke verifies ordinary sandbox launch and the new policy
metadata/inventory path. It does not establish the unsupported scoped mailbox,
macOS provider behavior, vendor authentication, or any physical phone behavior.
