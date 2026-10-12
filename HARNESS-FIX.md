# Phone storyboard harness correction

Base: `71ef5dc5` (`codex/wt-fix-phone-20261011`). Working branch:
`codex/wt-phone-harness-20261011`.

The old `storyboardPhone` fixture created a standalone egg, then invoked pairing
administration without starting the main wing. Its `storyboardCommand` also
waited for `wt pair` to exit without supplying a phone or terminal approval.
Those are fixture failures: an egg is not the pairing authority, and pairing
is an interactive two-sided operation.

[The pairing protocol](docs/pairing/PROTOCOL.md), opening contract, explicitly
requires `wt pair` to contact the running main wing's local control endpoint.
“Invitation and transport” specifies the real-user sequence `wt wing start`
then `wt pair`, and accountless local operation. “PAKE and two-sided
confirmation” requires independent phone confirmation and terminal Enter.
“Administration and revocation” requires `wt devices` and revoke to contact
that same main wing.

## Fixture changes

- Start the actual built CLI with `wt wing start --paths <fixture workspace>`.
  Northstar already supplies a fresh isolated `HOME`, `WINGTHING_DIR`, PATH,
  temporary directory and disposable egg. No registration/account token is
  present, so this uses the ordinary accountless local startup path. Register
  `wt wing stop` cleanup before startup and run it before removing state.
  General wing readiness precedes phone discovery, so wait up to ten seconds
  for a successful authenticated, read-only `devices` request to the specific
  phone-control endpoint. This closes the startup race found by the first
  independent review. The existing CLI journey also uses this wait. The pair
  prerequisite test delays fixture Tailscale discovery by one second to
  exercise this ordering. No original card timeout is changed.
- Use the existing Go `internal/phonepair/testpeer --client` as the phone.
  Drive the real PTY `wt pair --show-link` command, decode its real invitation,
  perform PAKE proof, verify the returned emoji, confirm on the phone, check
  that this alone did not issue a grant, then send terminal Enter. Wait for
  successful CLI exit and drain its actual output before returning it to the
  unchanged card assertions. `--show-link` is the existing explicit fallback
  route; it does not fabricate QR/camera evidence.
- Recover the encrypted grant, authenticate, inventory the one existing
  blocked session, attach/read its marker, and detach. Check that the main
  wing PID is unchanged. The peer keeps its signing key and credential only
  in process memory; the wing persists the actual protocol device record.
- Card 6 now has a genuinely paired device before listing. Card 11's existing
  prerequisite explicitly expects an unpaired-device error. Pair and revoke
  through the real CLI first, verify reconnect is rejected, then run the
  original revoke command against that revoked device. Both calls and the
  real success/error outputs are recorded; no response text is synthesized.
- Keep non-CGO builds compiling through a driver that retains their explicit
  unsupported-pairing behavior. The native Go testpeer path requires CGO as
  specified by the protocol.

The original card file has exactly two fixture edits: add main-wing startup
and call the interactive phone command driver. Every original assertion,
expected string, command selection, timeout, test name and final fatal check
is byte-identical. The other three storyboard card files are unchanged. The
existing CLI journey assertions are also unchanged; an additional harness
prerequisite test provides a separately reportable executable result.

## Acceptance boundary

All 18 original phone cards end with an unconditional
`t.Fatal("REQUIRED-UNRUN: ... physical iPhone evidence required")` even after
all prerequisites succeed. That statement remains unchanged. Therefore raw
card outcomes must remain FAIL, and the physical obligations must be reported
as REQUIRED-UNRUN. A successful Go peer cannot establish native phone UI,
camera scan, device Keychain/Secure Enclave, cellular reachability, permission
flows or background notifications. The protocol explicitly preserves this
boundary. Backend evidence cannot close a physical-card bead.

Results and gate comparison: [evidence/harness/RESULTS.md](evidence/harness/RESULTS.md).
Independent assertion review: [HARNESS-REVIEW.md](HARNESS-REVIEW.md).
