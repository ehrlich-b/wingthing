# Private Wingthing dogfood operator

Status: fresh-session and disconnected Terra/Sol proof passed September 5, 2026.
Use private builds only. Exact evidence and limitations are recorded in
`docs/dogfood-reconnect-progress.md`.

The source worktree is `/Users/ehrlich/repos/wingthing-dogfood-hardening`.
The separate executable is `~/.local/bin/wt-dogfood`. It uses the existing
profile at `~/.local/share/wingthing-dogfood/client`, never the default profile.
Provider credentials remain on their original execution machines.

## Fresh session

```sh
wt-dogfood review list --json
wt-dogfood review status JOB --json
wt-dogfood review result JOB --artifact patch --json
wt-dogfood review result JOB --artifact review --json
wt-dogfood review result JOB --artifact tests --json
```

Listing is owner-scoped and newest first. Follow `next_cursor` with
`review list --cursor CURSOR --json` when nonempty. `final_evidence_available`
means the terminal record contains a candidate, review, and both test records;
it is not a replacement for checking `status`, test exits, and artifact digests.

Each command checks the pinned private SSH connection, opens or restores the
loopback forward when needed, and fails with remediation when unavailable.
SSH control checks, forward repair, and startup have separate two-, four-, and
ten-second deadlines, including an unresponsive control socket. Health checks
are limited to two seconds. These bounds use the Mac's `/usr/bin/perl`; they do
not limit the lifetime of the command after a connection is established.
The master connection expires after five idle minutes. No hosted relay fallback
or provider credential transfer is attempted. The wrapper's target is fixed:
work-1 (`10.80.1.50`), wing `5003a9a0a5b64b2f8521db78`, roost
`http://127.0.0.1:17881`. Target overrides are rejected.

For native MCP, configure the executable as `wt-dogfood` with arguments
`mcp connect --client NAME`. Call `wingthing_capabilities` and `wing_list`, then
qualify each wing-owned operation with its explicit `wing_id`.

## Submit and leave

```sh
wt-dogfood review submit --spec job.json --json
```

The checked-in `docs/dogfood-confirmed-stages-job.json` is a complete bounded
hardening example for the existing pinned Wingthing replicas. Run it from the
durable source worktree. Its fixed request ID deliberately deduplicates reruns;
use a new request ID when preparing a genuinely new task. The separate
`docs/dogfood-lease-fixture.json` is a 930-second timer fixture, not coding work.

The spec pins both worker wing IDs and existing clean source paths, the full
source commit, exact allowed files, test argv, models, and deadlines. Work-2
(`10.80.1.6`, wing `50a0a378e41543e584476044`) implements; work-1 reviews and
owns the job. At most two revisions. Sources must be explicitly enabled in
each worker's `review-jobs.yaml`; no source fetch or synchronization is implied.
Do not put credentials in the spec. See `docs/review-job-dogfood.md` for its fields.

Keep the returned ID, or discover it through listing later. Closing the client
does not stop the VM coordinator. An interrupted coordinator reports
`interrupted`, not an automatic replay. Generated patches remain artifacts until
explicitly accepted; no automatic application, commit, merge, or deployment.

## Install a private build

From the durable worktree:

```sh
make web
scripts/build-dogfood.sh
scripts/install-dogfood.sh dist/dogfood/VERSION/wt-darwin-arm64
```

The installer preserves immutable versioned binaries under
`~/.local/share/wingthing-dogfood/builds/`, points the private entry to the chosen
one, and installs these instructions alongside it. It does not replace `wt`.
Confirm `wt-dogfood --version`. VM deployments require separate idle checks and
verification of `/proc/PID/exe` digests. Never restart a worker with active work.

No tags, releases, pushes, merges, production changes, credential-cache copying,
or general file synchronization are part of this private workflow.
