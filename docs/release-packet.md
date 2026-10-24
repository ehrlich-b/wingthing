# Held release packet

Decision: **HOLD — no publication, deployment, tag push or public announcement is authorized.**
The packet is an operator decision input. Integrity checks and local publication
rehearsal do not qualify a release for users. Do not promote a held packet.

`candidate.json` binds the full source commit, locally reserved unique stable
version, four platform hashes, build logs, test commands and logs, original
assessment disposition, and supported feature scope. `SHA256SUMS`, the exact
four executable files, the installer snapshot and this document travel together.
Files are sealed read-only and existing staging destinations are refused. This
protects against accidental replacement; it is not signing or independent trust.
Keep a trusted digest of the packet and bootstrap keys separately before any
distribution. Never rebuild under a previously distributed version.

## Prepare locally

Use a clean, committed checkout. Build web assets with `nice -n 10 make web`
first. Record each required test with its actual command, for example:

```sh
export HOME="$PWD/../home"
nice -n 10 python3 scripts/pm/release_packet.py --record-test race \
  --output ../test-receipts -- go test -race -p 2 ./internal/relay/... ./internal/sandbox/... ./internal/egg/... ./internal/wing/...
nice -n 10 python3 scripts/pm/release_packet.py --stage --version v0.150.1 \
  --output ../release-packet --receipt ../test-receipts/race.json
export PACKET="$PWD/../release-packet"
nice -n 10 python3 scripts/pm/release_packet.py --verify "$PACKET/candidate.json" --inspect --negative-fixtures
nice -n 10 python3 scripts/pm/release_packet.py --verify "$PACKET/candidate.json" \
  --inspect --rehearse-publish-rollback --local-only
```

Use an unused output directory and version. Local tag absence is only a local
reservation; check the authoritative release registry and both owned stores
before approval. Include receipts named `make-check`, `android`, `browser-join`,
`race`, `source-tests`, `hygiene`, `prepublish` using repeated `--receipt` flags.
Failures remain in the packet and block release. Logs and receipt source commits
must match; no result may be copied from another source commit.

If supplied, `--assessment FILE` consumes JSON containing `commit`,
`disposition`, `original` and `sha256`; the original document is copied intact.
A PASS asserted for an ancestor is insufficient. Human assessment of the
document and signer is still required; hashing an assertion does not authenticate
it. Without the original exact-commit disposition the packet remains held.
The original bytes must themselves identify `commit` and `disposition` in JSON,
or have exactly one `Commit: FULL_SHA` and `Disposition: PASS|FAIL|PENDING` line.
Relabeling an ancestor's document in envelope metadata fails verification.

The strict command below exits nonzero for any missing receipt or qualification.
`--inspect` reports integrity and every blocker while exiting zero for a valid
inventory. It cannot approve a release. The rehearsal copies actual artifact
bytes into two disposable local stores, rejects version overwrites, selects the
candidate, then restores disabled distribution. No previous verified version
was supplied; this does not exercise a previous binary, HTTP service, signatures,
origin failover or daemon/session survival.

```sh
nice -n 10 python3 scripts/pm/release_packet.py --verify "$PACKET/candidate.json"
```

## Operator decision

Native Mac, real-provider, hosted, owned-origin, installer-recovery,
durable-journey and quickstart qualifications are **PENDING**. Cross-builds
are artifact inventory only, never evidence of native execution or parity.
The feature list describes implemented source behavior, not a claim that any
provider/platform combination has been qualified. No model calls or hosted
operations are needed to inspect the packet. Collect independently validated
supplier receipts for this source and these hashes before reconsidering HOLD.
The current checker deliberately refuses promotion claims supplied as bare JSON.

These inventory binaries use `CGO_ENABLED=0`. Phone/browser pairing is
**unavailable** in these bytes; the manifest records that build limitation and
excludes pairing from supported features. Native browser-join source tests use
CGO and do not qualify pairing in the inventory binaries. A future native build
profile needs a new unique version, matching artifacts and native receipts
before pairing can be claimed. This limitation independently blocks promotion.

The release operator owns version reservation and publication; a security
approver owns trust/bootstrap and downgrade disposition; native Mac and provider
executors own their runtime receipts; the service operator owns deployed identity,
backup/restore, account recovery and hosted acceptance. These roles are not yet
assigned to named operators. Missing owners are unresolved blockers.

Required decision inputs: all product gates on this commit; original exact-head
PASS disposition; native runtime and bounded authorized provider results; owned
primary/independent-secondary signed feed, expiry, redirect, trust transition,
retention and primary-loss evidence; install/recovery/update interruption and
session receipts; a verified previous release and its security suitability.
Legacy inaccessible updaters require installer/manual recovery, not an assurance
that they can discover a new feed. The current packet does not close these gaps.

## Staged publish, verify and rollback

The following are **future separately authorized operator commands**, not actions
performed during packet preparation. Provision the existing owned-origin supplier
and obtain approved destinations first. Never substitute GitHub/GitLab fallback
or a new signing design when that supplier is absent. Source tags and CI artifacts
alone do not demonstrate what users can install.

First validate packet integrity and trusted hashes, then stage exact bytes:

```sh
: "${PACKET:?}" "${STAGING:?}" "${VERSION:?}"
(cd "$PACKET" && sha256sum -c SHA256SUMS)
test ! -e "$STAGING/$VERSION"
mkdir -p "$STAGING/$VERSION"
cp -p "$PACKET"/artifacts/wt-* "$STAGING/$VERSION/"
cp -p "$PACKET"/candidate.json "$PACKET"/install.sh "$STAGING/$VERSION/"
# Match manifest paths for checksum verification of the staged bytes.
mkdir "$STAGING/$VERSION/artifacts"
cp -p "$PACKET"/artifacts/wt-* "$STAGING/$VERSION/artifacts/"
cp -p "$PACKET/SHA256SUMS" "$STAGING/$VERSION/"
(cd "$STAGING/$VERSION" && sha256sum -c SHA256SUMS)
```

After the security operator supplies signed metadata and verified trust material,
publish immutable objects to both approved destinations before selecting a feed:

```sh
: "${APPROVED_PRIMARY:?}" "${APPROVED_SECONDARY:?}"
rsync -a --ignore-existing "$STAGING/$VERSION/" "$APPROVED_PRIMARY/$VERSION/"
rsync -a --ignore-existing "$STAGING/$VERSION/" "$APPROVED_SECONDARY/$VERSION/"
```

Refuse a preexisting version with different hashes before these commands; never
use overwrite/delete flags. The owned-origin supplier must provide the signed
feed promotion and rollback commands and independent HTTPS fetch verification.
Those exact commands are **blocked: supplier not supplied**, and must be added
to the authorized action sheet before publication. Check both origins' downloaded
bytes against trusted hashes and expiry/signer/redirect policy before feed
selection. Do not run the installer's unsigned feed as a substitute for that work.

For a manual installation on a qualified host, verify the trusted bundle first,
select the matching binary (`wt-linux-amd64`, `wt-linux-arm64`,
`wt-darwin-amd64`, `wt-darwin-arm64`), and check its version and command contract:

```sh
: "${ASSET:?matching artifact name}" "${INSTALL_DIR:?writable destination}"
"$PACKET/artifacts/$ASSET" --version
sh scripts/check-release-contract.sh "$PACKET/artifacts/$ASSET"
mkdir -p "$INSTALL_DIR"
# Use a new side-by-side name; retain the previous executable and state.
test ! -e "$INSTALL_DIR/wt-$VERSION"
install -m 0755 "$PACKET/artifacts/$ASSET" "$INSTALL_DIR/wt-$VERSION"
"$INSTALL_DIR/wt-$VERSION" --version
```

Do not advertise this as qualified fresh-install or legacy recovery evidence.
The staged installer is a snapshot, not an executed or independently qualified
installation path. A shell-only help/contract check makes no provider call.

## Daemon replacement and downgrade

Copying or installing a CLI executable does not replace an already running wing
or roost. The installer leaves the daemon running. Current update orchestration
validates saved daemon arguments before replacement and handles daemon restart;
that is a different operation from the side-by-side install above. Neither
method has a session-survival receipt in this packet. Drain or explicitly stop
active work, save outputs, back up state, and plan a maintenance interruption.
Never promise child-process survival across daemon restart or host reboot.

Before intentional replacement, capture the active listener, local-only/relay
mode and start arguments from your service configuration. With the existing CLI
in the same state environment, run `wt wing stop` (or `wt roost stop` for a roost).
Verify the actual PID is gone; a status error alone is not proof. Stop/isolate
the service at its service manager or disposable host boundary if shutdown fails.
Then start the chosen binary with the captured arguments. Preserve loopback for
`--local-only`; LAN phone pairing requires the explicitly printed
`--listen 0.0.0.0` command and a deliberate exposure decision.

Rollback first disables distribution or restores only a previous **verified and
security-approved** signed selector using the supplier's rollback action. Retain
the candidate's immutable bytes for diagnosis. Do not rewrite its version.
Restore the retained executable side by side only after stopping the daemon and
checking state-schema compatibility and backups. No previous verified artifact
or deploy image is supplied here, so binary/deployment rollback is pending.
Do not reverse identity migration 008 or restore disabled OAuth credentials.

**Downgrade weakens security:** older builds may ignore current native sandbox
policy fields, permit broader host access, accept plaintext terminal output or
bind local-only listeners more broadly. Do not launch untrusted workloads or
reuse credentials under such a build. If the prior version ignores current
policy, stop both wing and roost, stop affected workloads, and isolate the host
from relay/LAN/Internet at an operator-controlled boundary. Keep distribution
disabled until a policy-enforcing build is qualified. A restored feed or a CLI
version change does not change code already running in an old daemon.
