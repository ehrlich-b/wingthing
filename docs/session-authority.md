In bare `wt`, select a session and press `i` to inspect its workspace, provider
state, filesystem grants and denials, and network scope. Scroll with `j`/`k` to
read the complete policy on a narrow terminal.

New egg sessions save a private `session.authority.json` from the runtime's
prepared policy. Local inventory reads this bounded, regular metadata file;
saved SSH inventory carries the same projection from the owning machine. It
includes provider profile directories, prefix/overlay mounts, explicit read/write,
write and rename denials, protected write targets, domain policy and forwarded
loopback ports. It excludes arguments, environment values, credentials and
transcript content. This is presentation evidence, never launch or resume
authorization. The display does not prove native confinement or provider readiness.

Legacy sessions and unavailable, malformed or linked policy files show the
session cwd and explicit unknown authority fields. The navigator does not load
today's workspace policy and attribute it to an already-running session. A
policy projection with a different workspace is also unavailable.

An outer-boundary session explicitly displays full OS-user authority and
unrestricted Wingthing networking; unenforced sandbox mounts and denials are
omitted. Linux's ordinary HOME write isolation leaves reads and writes outside
HOME under OS-user authority except explicit denials. The display distinguishes
that mode from a Linux allowlist jail. A workspace path alone is not a sandbox.

Scoped host-mailbox launches still require the existing macOS Seatbelt
write-protection contract. Unsupported hosts now receive a typed diagnostic
with the selected policy path and an instruction to use a supported host while
retaining that policy. No grants or sandbox restrictions are changed.
