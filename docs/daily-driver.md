# Daily work with wt

Start a local Claude parent in your checkout:

```sh
wt claude --cwd /path/to/project
```

A missing local wing starts automatically in local-only mode for the personal
client. Existing wing policy and named-client grants still apply. A remote wing
must already be running; wt does not install software or bypass receiving policy.
Launch failures identify the stage, selected/inherited policy paths and a doctor
command. An admitted session is not launched again automatically after attach
fails: use the exact reattach advice.

Press **Ctrl+B then q** (or Q) to detach. Press **Ctrl+B twice** to send a literal
Ctrl+B to the provider. Bracketed paste passes through literally in an attachment;
the navigator ignores it. Detach and leaving wt preserve running sessions.

Run bare `wt` in a terminal to find work across local and saved machines. Non-TTY
bare wt prints CLI help. Arrows or j/k move; Enter attaches the exact selected
machine/session ID. `/` filters machine, name, ID, agent, cwd or status. `m` selects
All, local, or a saved machine, including empty and offline ones. `i` shows full
metadata and route; `?` opens scrollable help. `R` reloads machines and refreshes
inventory. Saved profiles also reload every two seconds. Slow/offline queries run
independently with bounded retry backoff; local work stays usable.

Save an existing SSH destination, including its custom executable and state:

```sh
wt remote add forge my-ssh-alias --wt-binary '/opt/wingthing/wt' --wingthing-dir /home/me/.wingthing
wt remote ls
wt attach -- forge:FULL_SESSION_ID
wt remote rm forge
```

To change a profile, remove it and add the replacement, or atomically replace the
validated `remotes.yaml` under the selected state directory. Background SSH uses
batch mode without a TTY or authentication prompt; explicit foreground attach can
use the normal SSH authentication flow. Host-key checks remain enabled. Profile
replacement/removal cancels old polling and pending rename/stop/new/takeover
prompts. Late replies cannot restore removed machines. Malformed registries show
an error, retain last valid remote rows/routes as stale, and refuse remote actions
until fixed. Local attach still works. Invalid primary/security configuration
remains an error.

`n` creates on the selected machine: enter the agent (`claude`, `codex`, or blank
for a shell), then the cwd. Select an empty machine through `m` first. **Remote cwd
belongs to the receiving machine**; wt never resolves it against your local
filesystem. Claude from `n` uses the same scoped-parent path as direct `wt claude`.
`r` renames the captured exact target; `x` asks for confirmation before stopping
it. Cancelling a prompt does not mutate the session.

`v` observes without input or resize. `t` asks for preview takeover only when the
session reports input ownership; the receiving wing can deny it. Stable sessions
without that metadata do not offer takeover. An unknown transport outcome is not
a successful provider exit. Reconnect advice preserves exact route/state/binary;
retry is an explicit user action.

Native wheel input follows provider mouse mode; with mouse mode disabled, normal
terminal history/scrollback remains available. Use your terminal's selection
modifier when the provider captures the mouse (the modifier varies by terminal).
Browser copy behavior remains separate. Kernel PTY fixtures cover mouse bytes and
history; a physical trackpad and selection modifier still need a GUI canary.

`!` filters native blocked, unread and failed work. `b` enables an optional bell
for this dashboard instance; it starts off. `B` means native input is requested;
viewing it does not clear blocking. `I*` and the `U` count mark an unread foreground
response; details or a successful attachment acknowledge the response shown.
The provider or descendants may continue after a response completes. `?` status
means missing/unknown lifecycle evidence or disabled hooks, never a guess from
screen text. Connection freshness is separate: stale rows are cached inventory.

`h` opens recent runs with qualified wing/run IDs, including terminal records that
outlive the coordinator. `/` filters, `N` explicitly loads the next bounded page,
`i` shows details, and `o` opens a bounded result. Enter attaches only a verified
live session; otherwise it opens metadata. Esc returns to sessions. A result or
archive is not a live session. Recover durable IDs and bounded results explicitly;
Offline inspection is a separate read of an explicit, completed, hash-verified
snapshot inside your workspace. Inspect the implemented commands before choosing them:

```sh
wt agent recover --json
wt run-inspect --help
```

See [testing](testing.md) for the offline daily-loop gates,
[SSH routes](ssh-remote-sessions.md) for transport limits, and
[Claude parent conversations](wt-claude.md) for child work.
Phone/tunn setup belongs to the other lane's eventual guide; the existing
[browser overview](../README.md#4-browser-visibility-self-hosted-roost) is the current reference, not a
phone deployment recipe. Real provider, real SSH and physical Mac GUI canaries
are separate from isolated fixture acceptance.
