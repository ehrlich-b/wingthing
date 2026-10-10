# Weekend canary

Run from a dedicated checkout on branch `codex/dogfood-weekend-20261010`.
Requires Bash, Python 3.9+, and a locally built `wt`; build prerequisites are
Go and npm. It never resolves `wt` from PATH, installs a binary, logs in, or
reuses a real wing's state. Every invocation allocates a mode-0700 fixture under
this checkout's `.scratch`, uses fresh HOME/WINGTHING_DIR, and reuses the native
Codex, Claude and SSH fixtures in `internal/testprovider` and `internal/testssh`.

```sh
nice -n 15 make web
nice -n 15 go build -p 2 -buildvcs=false -o wt ./cmd/wt
./scripts/dogfood/run.sh
```

The runner executes once; the coordinator schedules it. Stdout contains one
JSON record per check and one runner receipt. Each has `check`, `status`,
`latency_ms`, and `error`. Any failure exits nonzero. Unconfigured live checks
and unavailable OS sandbox acceptance are explicit `skip` records. A skip is
never evidence that its flow passed. `--require-all` makes skips fail.

The fixture checks cover local-only foreground and default-daemon startup without relay tokens,
stdio admission/wait/result, SIGKILL of an MCP client, SIGKILL/replacement of a wing while
a provider holds a native turn, terminal start/send/wait/stop, native Tasks
get/list/result/cancel including cancellation surviving a wing crash, and remembered fake SSH
execution after disconnect. The Mac scoped Claude check verifies strict MCP
configuration, refusal of protected files/control socket and foreign IDs,
two qualified child receipts, viewer exit, mailbox-client replacement and
native child result recovery. Linux refuses this protected-write boundary;
Mac hosts prohibiting nested `sandbox-exec` explicitly skip it. Existing tests
are unchanged. This harness's fixture runs request the documented trusted outer
boundary for deterministic provider tests; they do not establish OS confinement.

Logs live in `.scratch/dogfood-logs`: at most 24 JSONL receipts of 256 KiB each,
plus one lock file. A nonblocking lock avoids overlap, and a 12-minute watchdog
allows one minute for cleanup. Diagnostics in each error are capped at 4 KiB;
process output is continuously drained into bounded tails. Cleanup stops every
private egg endpoint, closes adapters/forwards and wings, and removes fixture
state only after verifying PID disappearance and child exits. Normal hosts also
verify descendant inventory; a host denial of `ps` is explicitly recorded in
the cleanup receipt. Failure retains state and exits nonzero.
Do not run two raw `canary.sh` invocations when a locked wrapper is intended.

## Forge commands for the coordinator

On Forge itself, in its dedicated checkout (no deployment or installed wt change):

```sh
nice -n 15 make web
nice -n 15 go build -p 2 -buildvcs=false -o wt ./cmd/wt
./scripts/dogfood/run.sh
mkdir -p "$HOME/.config/systemd/user"
cp scripts/dogfood/wingthing-dogfood.{service,timer} "$HOME/.config/systemd/user/"
systemctl --user set-environment "WT_DOGFOOD_REPO=$PWD"
systemctl --user daemon-reload
systemctl --user enable --now wingthing-dogfood.timer
systemctl --user start wingthing-dogfood.service
systemctl --user list-timers wingthing-dogfood.timer
```

`set-environment` must be repeated after a user-manager restart. To persist it,
the coordinator can supply an `Environment=WT_DOGFOOD_REPO=/absolute/checkout`
service drop-in. These files do not enable lingering or change host login policy;
the user manager must already remain running for unattended weekend checks.
The timer runs every 45 minutes, with up to one minute scheduling slack. Inspect
the newest JSONL receipt after each run; service failure remains visible through
`systemctl --user status wingthing-dogfood.service`. Stop scheduling with
`systemctl --user disable --now wingthing-dogfood.timer`.

## Mac manual commands

```sh
cd /Users/ehrlich/repos/wingthing-dogfood-20261010
nice -n 15 make web
nice -n 15 go build -p 2 -buildvcs=false -o wt ./cmd/wt
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
