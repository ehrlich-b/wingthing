# Linux phone backend acceptance

Start from integration `3a0b7fa8`, which includes PAKE and machine pairing.
The imported card suite is unchanged and deliberately cannot satisfy physical
phone acceptance; see `TEST-DISPUTES.md`.

Run the complementary backend journey:

```sh
GOFLAGS=-p=4 nice -n 15 go test -count=3 -run '^TestPhoneStoryboardWingJourney$' ./cmd/wt
```

It builds `wt` and `internal/phonepair/testpeer` and starts an accountless main
wing inside disposable HOME/WINGTHING_DIR directories. It exercises the CLI
invitation and hidden fallback link, persistent wing identity, six words and
five-minute deadline, LAN connection, read-only fixture Tailscale IP/MagicDNS
advertisement, both human approval bits, dropped-response recovery, unique
device listing, missing-device diagnosis, encrypted challenge/signature
connection, blocked inventory, attachment, input/output, denied configuration,
detach, exact-session stop, live revocation, denied reconnect and terminal
cancellation. A colliding session on another wing must remain untouched and
the original wing daemon must retain its PID. Providers are gRPC fixtures.

`testpeer --client` accepts one JSON object per stdin line and emits one result
per line. Initialize with `{"Op":"init","Invitation":<expanded invitation>,
"Endpoint":<isolated direct HTTP origin>}`; then use `start`, `prove` (with
`Name`), `confirm`, `status`, `connect`, or a device RPC (`SessionID` and `Input`
when applicable). All network messages use the encrypted carrier, then the
PAKE/device envelope. Responses bind invitation/wing identity or connection
and sequence. Credentials and signing keys remain in memory; output removes
the raw credential. This test transport uses an explicit direct origin, has no
redirects, cookies or proxies, and never retries or replays a session command.
The original no-argument server mode remains available for Swift fixtures.

Existing protocol tests complement the CLI journey with expiry, wrong secret,
wrong emoji, single-use claims, transcript pinning, replay, bounded admission,
registry permissions/persistence and in-flight revocation. Go endpoint tests
verify direct advertisement; Go relay tests verify accountless opaque forwarding,
owner policy, exact-wing reply routing and rejection of account headers. These
are fixture checks, not proof of real Tailscale or cellular reachability, iOS
route preference, camera, Keychain, Secure Enclave or notification behavior.
