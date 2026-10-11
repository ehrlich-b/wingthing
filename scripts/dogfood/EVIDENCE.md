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

## Whole-epoch consumer

`status.py` retains its last-raw-receipt fields and adds `epoch_evidence`. A
later green receipt cannot hide an earlier failed slot, required skip, unknown
completion/identity or missed slot. Reports distinguish structural completion
from fixture success: a failed run with all check records is completed and
failed. Counts are deliberately overlapping. `skipped_slots` counts required
unavailable checks; optional live/SSH skips appear per attempt instead.
A slot passes fixture coverage only if every attempt in it passes; a successful
retry does not erase the earlier failure. Duplicate attempts are counted but
receive no additional slot coverage. `fraction_of_due_slots` is successful point
samples divided by due slots, never uptime. No healthy interval is inferred.

Fixture, native checks inside fixture receipts, optional live checks and fresh
outside-host acceptance have separate fields. Each cohort joins host, expected
and observed binary hashes, tracked-source commit/hash and actual slots. A
changed binary or an unknown identity cannot borrow another cohort's success.

The sparse consumer probe has eight planned slots: slots 0, 2 and 7 are missing;
slot 3 fails then retries successfully; slot 4 has a required skip; slot 5 is
late; slot 6 is interrupted. Six attempts cover five unique slots, with five
completed attempts across four slots, one failed slot, one skipped slot, one
unknown slot and two fixture-success slots. Successful point coverage is 2/8,
or 25%. These literal expectations are independent of the consumer's counting
code and model a sleeping host without any real-time sleep.

## Persistence and restart

`epoch-ledger.ndjson` begins with a canonical header, then attempt and result
records. Each record carries a sequence number, the preceding SHA-256 and its
own SHA-256 over canonical JSON. This detects torn writes, edits, reordered
records and duplicates; it is integrity checking, not an authenticity signature.
The existing nonblocking `run.lock` protects header creation, admission,
appends, runner state and rotation. Creation uses an fsynced private temporary
file and a no-clobber link; appends and atomic runner-state replacement are
fsynced. One maximum-size result record is reserved before an attempt starts.

A durable start precedes rotation and state creation. A durable result precedes
marking runner state complete. The result joins the start by attempt ID and the
raw receipt by filename/SHA-256. Rotation refuses an unrepresented or edited
raw receipt. Status reports also compare retained raw bytes with metadata.
Readers retain a verified prefix on a torn/invalid tail, mark integrity FAIL,
and mark unobserved slots unknown. Writers refuse the whole damaged ledger.
No reader or wrapper repairs a tail, retries an interrupted attempt, overwrites
an earlier result or starts a new schedule. A missing state/raw receipt cannot
clear a pending start or failed cleanup already retained in the ledger.

A verified cleanup after a bounded watchdog failure permits the operator's
next ordinary invocation under the existing runner semantics. That invocation
is a separate attempt; the failed slot remains failed. Expiry returns 1 when
any retained runner failure remains, even if the last receipt passed, and never
starts new work. An unresolved cleanup/interruption holds admission regardless
of expiry. Resolve owned endpoints/processes outside the worker before any
operator reconciliation; preserve the original ledger and receipts.

## Finite offline acceptance packet

The metadata-only replay creates 448 slots for each of two hosts (896 host
slots), representing 14 days at 45-minute cadence. It models missing/late runs,
a failed retry, interrupted completion, required skips, unknown identity,
changed builds and a terminal failed cleanup. It invokes no canary or provider,
and every generated packet is labeled `synthetic_fixture_only: true`.

From a checkout with clone-local offline caches and temporary directories:

```sh
python3 -B scripts/dogfood/rehearse_epoch.py --out "$PWD/.scratch/epoch-replay"
python3 -B scripts/dogfood/epoch_oracle.py \
  --ledger .scratch/epoch-replay/forge-fixture/epoch-ledger.ndjson \
  --ledger .scratch/epoch-replay/mac-fixture/epoch-ledger.ndjson \
  --now 2026-10-24T00:00:00Z
python3 -B scripts/dogfood/ledger.py report \
  --logs .scratch/epoch-replay/mac-fixture --now 2026-10-24T00:00:00Z
python3 -B -m unittest discover -s scripts/dogfood -p 'test_*.py' -v
bash -n scripts/dogfood/run.sh scripts/dogfood/canary.sh
git diff --check
```

The ledger report intentionally returns 1 for this failure-bearing example.
`epoch_oracle.py` imports no ledger/runner/status code and independently checks
hash chains, UTC slot arithmetic, identity joins and ended-epoch counts. Its
successful exit validates arithmetic, not product acceptance. Every host/build
keeps its own cohort and failures; successful points on one host cannot cover
the other's missing slots. Replay generation refuses existing output, including
operator edits; unchanged inputs produce byte-identical ledger/packet files.

For one final inspection of a genuine ended epoch, supply its fixed expiry as
`--now` and read `status.py --logs /ABSOLUTE/PRIVATE/LOGS --now UTC --json` or
`ledger.py report` once. Preserve the original manifest, ledger, runner-state
and retained raw receipts and compute their digests before any reconciliation.
No polling agent is required. Raw cleanup failures remain under the existing
hold; other rotated failures retain check/exit/cleanup and identity metadata.

Outside-worker observations remain REQUIRED-UNRUN until returned:

- Native checkout-built canary on an isolated host: raw receipts, exact binary
  and tracked-source identities, private descendant/PID/endpoint inventory,
  cleanup result and retained fixture paths for failures.
- Actual finite job disconnect, recovery and Stop receipts on the candidate
  runtime, with qualified resource identities and native final status.
- Existing finite job behavior across disconnect/manager recovery: original
  epoch/end/cadence, actual timer times and service result, including expiry.
- Genuine unattended use over the fixed epoch; fixture examples cannot supply
  its absent intervals or receipts.

This row installs/activates no service or timer, changes no installed runtime
and performs no production or live-provider operation. Full browser, loopback,
built-CLI and native-process acceptance cannot run in the worker. Compilation,
fake systemctl calls and socket-path geometry do not pass those gates.

## UTC and civil-time boundaries

`test_epoch_boundaries.py` covers microseconds around start, cadence, slack and
exclusive expiry, including a one-microsecond epoch and cleanup finishing after
expiry. The repeated New York civil hour on 2026-11-01 maps the UTC slots
05:00, 05:45, 06:30 and 07:15 to 01:00 EDT, 01:45 EDT, 01:30 EST and 02:15 EST.
The third slot has `fold=1`; all four UTC slot identities remain distinct. The
independent ended-epoch oracle agrees on four completed point samples, and a
later report does not add slots. UTC `Z` and `+00:00` spellings canonicalize to
the same identity; local offsets and naive civil timestamps are refused.
