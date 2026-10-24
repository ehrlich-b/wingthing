# Report shell task waits

The navigator answers “Which session needs me?” Plain shells stay Unknown,
regardless of output or silence. An opt-in script can report Working, Needs you
or Done; each label includes “reported”. These signals are task statements,
not native provider evidence, permission decisions or input authority.

For an existing plain shell session, copy its full immutable ID from
`wt session list --json`, then run on the host:

```sh
WT_SESSION_ID=FULL_SESSION_ID sh scripts/examples/reported-shell.sh
```

The example requires `wt` and Python 3. Set `WT_BIN` to a local build if needed.
It reports working, explicitly waits for Enter, acknowledges that wait, then
reports finished. Run it once per session. Three copies with three IDs can wait
independently. The next navigator refresh includes each waiting session in Needs
you and the attention filter; acknowledging one changes only its own session.

The adapter uses `wt session report ID --json` and the existing durable lifecycle
journal. Its optional `shell` object contains `session_id`, `revision`, `after`
(the preceding shell report's event sequence), and `state`: `working`, `waiting`,
`continue`, or `finished`. Start with revision 1, after 0, state working. Increment
revision once per transition and use the returned sequence for after. Retrying
the identical latest report returns the original event without appending it.
Names, ID prefixes, mismatched IDs, old revisions, stale acknowledgement cursors,
non-shell sessions, and reports following process exit or disappearance are refused.
A waiting task resumes only with an exact continue acknowledgement, or finishes.
Finished is terminal. A process exit removes a wait even if no finished report
arrived. Quiet, uninstrumented shells have no inferred task state. Existing
provider reports continue to describe intent without changing native state.
