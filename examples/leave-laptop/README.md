# Leave and return to work

The recording answers: “Will my work keep going when I close its viewer?”
Run a harmless job, leave the terminal, return to the same execution, then finish
and inspect its result. The script records the real built CLI and egg, with no
agent login, relay, SSH connection, cloud service or installed binary change.

From the repository root, after `make web`:

```sh
nice -n 10 go build -p 2 -buildvcs=false -o ./wt ./cmd/wt
nice -n 10 python3 examples/leave-laptop/record.py --wt ./wt --output-parent /absolute/scratch
```

Supply an existing private scratch directory with a short absolute path (Unix
socket paths have a platform length limit). Each run makes a new mode-0700
`j.*` directory. It retains `journey.cast` (Asciinema v2), `terminal.txt`,
`commands.json`, `receipt.json`, launch/away/ended inventory, progress, result,
and isolated HOME/state. Play the recording with `asciinema play journey.cast`
or read the plain transcript. No player is required to run the assertions.
Recordings contain only fictional work and the generated workspace paths.

The storyboard uses these existing commands:

1. `wt terminal --unsandboxed --json` admits one local command egg.
2. `wt attach SESSION`, then `start` begins eight finite work steps.
3. Ctrl+B then q detaches the viewer. Progress advances with no viewer present.
4. A fresh `wt attach SESSION` returns to the same session and job PID. `status`
   confirms all eight steps; this is reattachment, not provider continuation.
5. `finish` writes `result.txt`. The attached client observes the terminal exit,
   active inventory loses the session, and the script retains the result and
   the failed active-only read after exit. It never launches a replacement.

The script asserts each transition and exits nonzero on a mismatch. Timeouts
are bounded. On failure it stops only its recorded session and keeps diagnostics;
it never deletes the generated directory. Discard it yourself when finished.
Caller provider credentials, Wingthing state and remote selectors are excluded
from the subprocess environment. The copied job accepts only the scripted
instructions and times out if abandoned.

## Existing building blocks

The [homepage recording](../../docs/homepage-demo.md) already illustrates the
production browser's qualified multi-machine session inventory, progress and
one targeted instruction using eight fictional sessions. Reuse that script for
the 390/1440 px browser illustration; its encrypted fixture is distinct from
this real local terminal recording. The [worktree example](../worktrees/README.md)
uses the same persistent-terminal commands and isolated environment approach.

An existing segregated host or VM can own the outer execution boundary while
an egg owns a persistent PTY. This fixture explicitly uses the existing
[`--unsandboxed` / `outer-boundary` contract](../../README.md#sandbox), as the
worktree example does. It creates no VM or security boundary and proves no
confinement. Run it only with this trusted synthetic job.

## What this does not establish

Closing a viewer preserves work on an awake execution computer. Closing or
sleeping the computer that runs the job can stop progress; durability here does
not mean host reboot recovery. To leave an actual laptop, place execution on
an already available awake remote wing using the
[personal remote wing setup](../../patterns/personal-remote-wing/INSTRUCTIONS.md).
That guide's browser portal lives on the browser computer, so a powered-off
portal also removes that route until restored.

Native Claude/Codex continuation, provider completion receipts and archived
conversation recovery require their own lifecycle bindings and credentials;
this command job exercises none of them. An ended command has no native provider
session to resume. Active-only `session read` is unavailable after exit; this
example's retained result and recording are not a generic completed-run UI.
The [durable run inventory](../../docs/run-inventory.md) and
[offline inspector](../../docs/run-inspect.md) cover separate semantic run records.
Physical phone pairing, cellular routing, browser takeover, and real provider
work across disconnect remain separate acceptance work. The homepage's
in-memory replies also cannot establish any of those native behaviors.
