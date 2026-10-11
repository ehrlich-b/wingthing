# Finite canary evidence contract

An epoch is the immutable half-open UTC range `[epoch, until)`. Its cadence is
2700 seconds, with 60 seconds of slack and a 120-second initial boot allowance.
Slot `i` starts at `epoch + i * 2700`; the number of slots is the ceiling of the
range length divided by the cadence. The end is exclusive, including during
restart and civil-time changes. This is an observation contract for the existing
runner and timer; it adds no scheduler or catch-up work. Actual timer drift and
Mac sleep remain visible as late or missing slots.

Every actual attempt belongs to the slot containing its start, even when late.
Two attempts in one slot are duplicates/retries; neither fills a previous slot.
A green receipt is a point observation, never a healthy time interval. A missing
new-epoch slot is missing; unavailable pre-ledger history is unknown. Slots in
the future are pending. At expiry the fixed final slot is due, even if shorter
than the grace allowance. An in-flight attempt may finish cleanup after expiry.

The ledger records bounded metadata: host, slot, actual start/end, check status,
native-completion check status within the canary, cleanup, exit code, raw receipt
SHA-256, observed binary SHA-256 and independent tracked-source commit/hash.
Source identity does not attest reproducible binary provenance. Optional live
checks remain separate from deterministic fixture checks and outside-host
acceptance. Provider output, prompts, arbitrary errors, environment values and
credentials are excluded. Reports always retain `all_green: false`.

Limits are 4096 scheduled slots, 4096 encoded bytes per record and 4 MiB total
ledger bytes. A full ledger refuses admission before a canary or rotation; it
does not truncate, recycle, repair, extend the epoch or retry. The wrapper keeps
the existing 24 raw receipts of at most 256 KiB apiece. Metadata must be durable
before a raw receipt is eligible for rotation. Failed cleanup and interrupted
starts hold admission under the existing runner-state semantics.
