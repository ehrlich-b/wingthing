# Required external qualification

This lane runs on Linux. Linux PTY fixtures and native BoringSSL calls establish
the tested CLI/wing behavior; they do not establish macOS or physical iOS acceptance.

- 02/card-0,1,2,4,5 and 06/card-1,5: protected scoped-parent/provider launches need
  native macOS Seatbelt protection evidence. The existing host write-protection
  model refuses Linux; no protected state was moved into a writable workspace,
  no privileged fallback was introduced, and no confinement assertion was removed.
- 05/card-0 through 05/card-17: actual camera scan, local-network permission,
  device-only Keychain/Secure Enclave behavior, iPhone screens and both human
  confirmations, cellular/relay routes, notifications, session interaction and
  live physical-device revocation remain **REQUIRED-UNRUN**. No phone account token
  was used. The Go peer tests establish protocol/CLI prerequisites only.
- Real vendor authentication, native terminal mouse/modifier handling and provider
  readiness on the Mac remain **REQUIRED-UNRUN**, even where Linux fixture cards pass.

The attention reopen contradiction is a test dispute, not an unrun native gate.
See the root `TEST-DISPUTES.md`. Phone card fixtures also need correction before
they can evaluate the now-present CLI prerequisites; their unconditional physical
gate stays separate from successful supplemental CLI/wing tests.
