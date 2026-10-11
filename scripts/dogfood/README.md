# Weekend canary

Run from a dedicated checkout on branch `codex/dogfood-weekend-20261010`.
Requires Bash, Python 3.9+, and a locally built `wt`; build prerequisites are
Go and npm. It never resolves `wt` from PATH, installs a binary, logs in, or
reuses a real wing's state. Every invocation allocates a mode-0700 fixture under
this checkout's `.scratch`, uses fresh HOME/WINGTHING_DIR, and reuses the native
Codex, Claude and SSH fixtures in `internal/testprovider` and `internal/testssh`.
The fixture root is HOME: protected state and the copied controller binary sit
beside the writable workspace, TMPDIR and provider profile directories. State
outside HOME would be provider-writable under the current Seatbelt policy.

```sh
nice -n 15 make -j1 check
./scripts/dogfood/run.sh
```

The runner executes once; the coordinator schedules it. Stdout contains one
JSON record per check (13 including cleanup) and one runner receipt. Each has
`check`, `status`, `latency_ms`, and `error`. Any failure exits nonzero. Unconfigured live checks
and unavailable OS sandbox acceptance are explicit `skip` records; Linux mailbox
checks use `unsupported-on-linux`. Neither status is evidence that a flow passed.
`--require-all` makes both unavailable statuses fail.

The fixture checks cover local-only foreground and default-daemon startup without relay tokens,
stdio admission/wait/result, SIGKILL of an MCP client, SIGKILL/replacement of a wing while
a provider holds a native turn, terminal start/send/wait/stop, native Tasks
get/list/result/cancel including cancellation surviving a wing crash, and remembered fake SSH
execution after disconnect. The Mac scoped Claude check verifies strict MCP
configuration, refusal of protected files/control socket and foreign IDs,
two qualified child receipts, viewer exit, mailbox-client replacement and
native child result recovery. A separate negative check puts state inside the
writable workspace and requires the exact provider-writable refusal before any
egg or mailbox starts; it runs even when nested `sandbox-exec` is forbidden.
Linux refuses this protected-write boundary;
Mac hosts prohibiting nested `sandbox-exec` explicitly skip it. Existing tests
are unchanged. This harness's fixture runs request the documented trusted outer
boundary for deterministic provider tests; they do not establish OS confinement.

Logs live in `.scratch/dogfood-logs`: at most 24 JSONL receipts of 256 KiB each,
plus one lock, one atomic runner-state file and, for finite runs, one immutable
epoch metadata ledger of at most 4 MiB. A nonblocking lock avoids overlap, and a 12-minute watchdog
allows one minute for cleanup. Diagnostics in each error are capped at 4 KiB;
process output is continuously drained into bounded tails. Cleanup stops every
private egg endpoint, closes adapters/forwards and wings, and removes fixture
state only after verifying PID disappearance and child exits. Normal hosts also
verify descendant inventory; a host denial of `ps` is explicitly recorded in
the cleanup receipt. Failure retains state and exits nonzero.
Do not run two raw `canary.sh` invocations when a locked wrapper is intended.

## Forge handoff for the coordinator

Use only the isolated personal development host, an already-running systemd
user manager and a durable dedicated checkout. This package does not enable
lingering, change login policy, download dependencies, install `wt`, or activate
units during generation. Linux/systemd scheduling, restart survival and native
provider acceptance are **REQUIRED-UNRUN** until the coordinator returns evidence.
The chosen handoff end is **2026-10-13T12:00:00Z**; if it has passed, choose a new
explicit end and a new package directory. A deadline change in an existing
package is refused, so a manager restart or regeneration cannot extend it.

Stage independent offline Go 1.26.9/module/build and npm caches under `.scratch`
first; missing cached inputs are blockers. On Forge, from the dedicated checkout:

```sh
mkdir -p .scratch/tmp .scratch/gopath
export TMPDIR="$PWD/.scratch/tmp" GOTMPDIR="$PWD/.scratch/tmp"
export GOCACHE="$PWD/.scratch/gocache" GOMODCACHE="$PWD/.scratch/gomod" GOPATH="$PWD/.scratch/gopath"
export GOPROXY=off GOSUMDB=off GOTOOLCHAIN=go1.26.9 GOFLAGS=-p=2 GOMAXPROCS=2
# Select the already-staged executable; an older Go launcher may demand sumdb verification.
dogfood_go_os=$(uname -s | tr '[:upper:]' '[:lower:]')
dogfood_go_arch=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
export PATH="$GOMODCACHE/golang.org/toolchain@v0.0.1-go1.26.9.$dogfood_go_os-$dogfood_go_arch/bin:$PATH"
go version
export npm_config_cache="$PWD/.scratch/npm" npm_config_offline=true PYTHONDONTWRITEBYTECODE=1
nice -n 15 make -j1 check
python3 -B scripts/dogfood/prepare-forge.py --repo "$PWD" --out "$PWD/.scratch/forge-package" --until 2026-10-13T12:00:00Z
bash -n .scratch/forge-package/install.sh .scratch/forge-package/remove.sh
systemd-analyze --user verify .scratch/forge-package/wingthing-dogfood.service .scratch/forge-package/wingthing-dogfood.timer
cat .scratch/forge-package/forge.env .scratch/forge-package/10-forge.conf .scratch/forge-package/manifest.json
```

The finite one-shot, with fixture providers only, is exactly:

```sh
( source .scratch/forge-package/forge.env; ./scripts/dogfood/run.sh )
python3 -B scripts/dogfood/status.py --logs .scratch/dogfood-logs --now "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --json
```

Review both scripts and the units before running this separate installation and
activation sequence on that host. Installation refuses edited package or
installed files before any write, reloads the manager, and leaves activation to
the coordinator. Repeating preparation with unchanged inputs preserves the
original epoch, bytes and mtimes; extra operator files are left alone. A changed
file is preserved with a clear refusal, requiring a fresh package directory or
an explicit operator reconciliation.

```sh
cat .scratch/forge-package/install.sh .scratch/forge-package/remove.sh
bash .scratch/forge-package/install.sh
systemctl --user enable --now wingthing-dogfood.timer
systemctl --user start wingthing-dogfood.service
systemctl --user list-timers wingthing-dogfood.timer
systemctl --user status wingthing-dogfood.service
systemctl --user show wingthing-dogfood.service -p Environment -p Nice -p UMask -p KillMode -p Result -p ExecMainStatus
```

The environment is stored in `wingthing-dogfood.service.d/10-forge.conf`, with
no transient `set-environment` dependency. Runs are scheduled every 45 minutes,
with one minute of slack, and a nonblocking checkout lock prevents overlap.
`Nice=15`, `UMask=0077`, `GOMAXPROCS=2`, `GOFLAGS=-p=2`, the 12-minute wrapper
watchdog and systemd control-group cleanup remain in force. The timer remains
armed after expiry, but invocations start no new work; remove it to stop wakeups.
Returned acceptance should include receipts/status and the exact unit
configuration, timer times and service result before/after an independently
coordinated manager restart. A fake systemctl/parser fixture proves argument and
rendering contracts only. No manager restart is performed by this worker.

Remove this exact unedited package while preserving receipts and retained state:

```sh
bash .scratch/forge-package/remove.sh
```

## Mac manual commands

```sh
cd /Users/ehrlich/repos/wingthing-forge-canary-20261010
# Use the same staged offline environment above.
taskpolicy -b nice -n 15 make -j1 check
./scripts/dogfood/run.sh
```

The coordinator repeats the last command every 30–60 minutes using its existing
scheduler. A sleeping Mac cannot run the canary; inspect timestamp gaps.

## Optional live acceptance

```sh
./scripts/dogfood/run.sh --real-codex --model gpt-5.6-luna
./scripts/dogfood/run.sh --ssh-target forge --ssh-wingthing-dir /ABSOLUTE/PERSONAL/STATE --ssh-wt-binary /ABSOLUTE/CHECKOUT/wt
```

The real Codex flag makes one bounded no-tools inference using a private copy of
an existing regular `CODEX_HOME/auth.json`; it performs no login and copies no
user configuration. Provider requests can consume quota. The optional SSH check
remembers an already running personal wing only in disposable local state, then
reads its inventory and capabilities; it does not start remote work. Supply the
exact remote build and state paths. Do not point either option at production.
Without these flags their checks remain explicit skips. `--wt` can select a
different build within this checkout, including a regression build.

See [GAPS.md](../../GAPS.md) for unresolved bugs and acceptance limits. Timer
units are supplied here; this task does not install or activate them on either
machine, replace the coordinator's MCP binary, publish, or release anything.

## Local status and finite epochs

```sh
python3 -B scripts/dogfood/status.py --logs .scratch/dogfood-logs --now 2026-10-10T23:00:00Z --json
```

This command only reads local files. Exit 2 means no valid complete run exists;
exit 1 means failure, an expected 45-minute timestamp gap (with one minute of
slack), missing required coverage, or unknown evidence. Exit 0 means the required
fixtures for the reported platform passed within the inspected interval. Every
report sets `all_green: false`: optional provider/SSH skips, Linux unsupported
mailbox checks and outstanding real-host acceptance remain separate. A required
Mac sandbox skip and unavailable descendant inventory produce exit 1. The report
shows the last complete run, newest attempt, last cleanup, exact binary SHA-256,
Git commit and a SHA-256 of tracked checkout bytes at run start. Those are
independent identities, not a claim of reproducible binary build provenance.
Only the newest 24 raw receipts are read. Finite runs also consume the whole
epoch's bounded metadata, including rotated failures and exact identities.
Pre-ledger history without retained receipts remains unknown. Expectations stop
at the fixed expiry. See [EVIDENCE.md](EVIDENCE.md) for slot definitions, limits,
restart behavior, offline replay and outside-host acceptance.

The generated environment supplies `WT_DOGFOOD_EPOCH` and `WT_DOGFOOD_UNTIL` in
UTC. Restarting the user manager cannot change these values. At or after expiry,
`run.sh` starts no new canary and returns 3 for a clean finite epoch (accepted by
the generated service), or 1 if earlier failure/cleanup evidence remains. An
in-flight run may finish its bounded cleanup after expiry. A manual wrapper
without either variable runs once; a partial finite environment is refused.
An overlapping invocation is a skip only when no earlier failure is retained.
The ledger freezes host, start, expiry and cadence. Omitting or changing the
finite environment cannot bypass it. A full ledger refuses before a new canary
or raw-receipt rotation; it never prunes metadata or extends the schedule.

`runner-state.json` is atomically replaced under the file lock and retains the
latest result through receipt rotation. Cleanup failure, unknown cleanup after
interruption, and unreadable state refuse further runs. Preserve and inspect the
named receipt and retained fixture before restarting. After verifying that all
owned processes/endpoints have gone and resolving the failure, an operator may
archive the state and finite ledger outside the log directory and use a fresh
explicit epoch; keep the receipts and original ledger. A
legacy failed cleanup receipt still refuses admission, so archive that receipt
alongside the state only after resolution. Never delete unrelated or live state.

## Offline package fixtures

```sh
python3 -B -m unittest discover -s scripts/dogfood -p 'test_*.py' -v
bash -n scripts/dogfood/run.sh scripts/dogfood/canary.sh
```

`test_forge_package.py` runs a checkout-local fake canary and captures fake
systemctl arguments. It covers success, nonzero exit, shortened watchdog cleanup,
real concurrent wrappers, stale locks/state, retained cleanup failures, receipt
rotation, missing binaries, quoted paths, immutable preparation epochs, operator
edit refusal, resource environment, expiry and bounded status parsing. The CLI's
12-minute watchdog is unchanged; only the test API shortens it. Fixtures live
under this clone's `.scratch`, and failing tests retain their paths for diagnosis.
No fixture enables a timer or contacts a provider or host. The canary layout test
also checks private state placement and Darwin's socket path budget. Fixture
directory names adapt to the checkout's byte length and refuse paths leaving
fewer than three name bytes; this geometry check is not native socket acceptance.
The epoch probes cover full-history consumption, interrupted/damaged writes,
hard caps, changed builds, repeated civil time and conservative legacy import.

Run the separate full `make -j1 check` gate on a host that permits its process
inventory and native socket fixtures. A passing package fixture suite or
`make -j1 ops ACTION=build` is not a passing full gate. Keep full-suite failures
visible and return them to their product/CI owner; do not suppress tests to
promote this installation. Staged cache documentation is not repository product
documentation, but the current docs gate walks `.scratch` too. A denied `ps`,
long test socket path, or nested sandbox restriction remains an acceptance
blocker, even if this canary's supported fixture checks complete successfully.
