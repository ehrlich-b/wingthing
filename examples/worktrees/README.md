# Two worktrees, two persistent terminals

This offline example creates a synthetic Git repository, two branches (`left`
and `right`), two linked worktrees, and a persistent `/bin/sh` session in each.
It needs Bash, Git, Python 3, and a built Wingthing binary on Linux or macOS.
It uses no provider, account, relay, daemon, or network service.

Build `wt` using the repository's usual build prerequisites (`make check` also
builds it). From the repository root, with web assets already built:

```bash
go build -p 2 -o wt ./cmd/wt
export WT_BIN="$PWD/wt"
root=$(bash examples/worktrees/demo.sh start)
printf 'Demo directory: %s\n' "$root"
bash examples/worktrees/demo.sh status "$root"
bash examples/worktrees/demo.sh attach "$root" left
```

Inside the left terminal, run:

```sh
pwd
cat result.txt                 # left
printf 'left changed\n' > result.txt
```

Press **Ctrl+B**, release it, then press **q** to detach. The shell keeps
running. Attach to the other worktree:

```bash
bash examples/worktrees/demo.sh attach "$root" right
```

`pwd` now ends in `/right`, and `cat result.txt` still prints `right`. Detach
again, then reattach with `attach "$root" left`: the left file still contains
`left changed`, and the same shell is running. To see both branches and their
exact paths outside the sessions:

```bash
git -C "$root/repo" worktree list
bash examples/worktrees/demo.sh status "$root"
```

Each session starts with explicit `--cwd`, `--detach`, and `--json` flags. The
bootstrap writes `result.txt` and the physical CWD to `cwd.txt` in its worktree
before replacing itself with an interactive shell. Sessions are standalone
terminals; they need no project discovery refresh or running wing.

The example runs these trusted local shells with `--unsandboxed`. Commands you
type have your ordinary host permissions. Every `wt` invocation gets an empty
environment plus a small set of system variables and a fresh home/state under
the demo root, so it does not load caller credentials or remote configuration.
Git uses its own fresh home, no system/global configuration, templates or hooks.

Clean up after detaching:

```bash
bash examples/worktrees/demo.sh cleanup "$root"
```

Cleanup stops only the two recorded session IDs, then removes the generated
root, including its repository, branches, worktrees and terminal state. It
refuses a directory without its matching ownership marker. It never prunes
worktrees in your repository or stops your normal Wingthing daemon. Keep the
same built `wt` available until cleanup. Changes made inside the demo root are
disposable; copy anything you want to keep before cleaning up.

`start [scratch-parent]` accepts an existing scratch parent; it always creates
a new private child directory and prints only that path to stdout. Start can
be run multiple times, with each result managed independently. On a failed
start it attempts scoped cleanup and reports the root if cleanup needs a retry.
Use a short scratch path if Wingthing reports the OS Unix socket path limit.

Run the integration check against the real built binary:

```bash
WT_BIN="$PWD/wt" bash examples/worktrees/test.sh
```

The check creates two demos in scratch and uses real PTYs to attach, send
commands, detach with Ctrl+B q, and reattach to the same shell PID. It verifies
Git branches, physical CWD files, distinct output, and that cleanup leaves the
other demo plus a caller-owned repository, file and live terminal intact.
All temporary fixtures are local and removed on exit.
