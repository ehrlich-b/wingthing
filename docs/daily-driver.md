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

Run `wt` in a terminal to open the work list. wing stays pinned first on its machine.
Sessions that need a reply come first. Each row shows its state, agent, machine
and age. Unread responses have a `*` in the Unread column.

Arrows or j/k move; PageUp/PageDown page; Home/End select the first/last row.
Enter opens the selected terminal. `/` searches session names, agents, machines,
projects and states. Escape restores the previous search and selection. `!`
filters work that needs attention. `m` selects a machine. `i` opens full details.
`.` opens session actions. `?` opens help. `q` leaves sessions running.

`R` reloads machines and refreshes inventory. Work needing a reply rises as
machines load. Once you select work, rows stay in place until the next refresh. Slow machines do not block local work. Cached rows say
Last seen; their date stays fixed when a retry fails. Reconnect before attaching.
The wide view adds recent messages from the selected session and wing at 120
columns and 30 rows. Smaller terminals use a detail area below the list. Messages
load through the same saved route; they do not acknowledge unread responses.

`wt` keeps your terminal's colors. Secondary text is dim, the selected row
is bold, and only state cells are colored. `NO_COLOR=1 wt` keeps state words
and selection markers.
Borders use box drawing when the locale supports Unicode. `WT_TUI_ASCII=1 wt`
uses ASCII borders. Bare `wt` without a terminal prints CLI help.

Save an existing SSH destination, including its custom executable and state:

```sh
wt remote add my-server my-ssh-alias --wt-binary '/opt/wingthing/wt' --wingthing-dir /home/me/.wingthing
wt remote ls
wt attach -- my-server:FULL_SESSION_ID
wt remote rm my-server
```

Group the work list by adding `context: home` to local `wing.yaml`, and
`context: work` to entries in `remotes.yaml`. For a new remote, use
`wt remote add studio my-ssh-alias --context work`. Context headings show how many
sessions currently need your reply; `c` cycles all contexts, then each named
context, including Other for machines without a context. Work stays grouped by
machine inside each context. The existing structured `context` service block in
`wing.yaml` keeps its original meaning. To group a wing that also uses that
service block, add `machine_context: home` alongside it. Both settings survive
configuration saves.

`wt session list --json` lists local chat-history sessions with a `context` on
every row (`""` when unset). `wt session ps --json` includes context for local and
saved SSH machines, using the saved remote's context.

On the personal gateway, `phone_fleet_exclude_contexts: [work]` refuses phone
inventory, search, actions, launches and attachments to those remote contexts.
A remote inventory or search that includes an excluded context is refused as a
whole. Exclusions also revoke existing phone attachments. SSH navigation remains
available; adding a context never opts a machine into phone delegation.

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

Interactive local launches (`wt codex` and `wt egg <agent>`) negotiate the same
native terminal presentation as reattachment. They preserve provider mouse and
bracketed paste modes, then join the output stream and reset presentation on
detach or provider exit. Piped output keeps the legacy presentation.

Native wheel input follows provider mouse mode; with mouse mode disabled, normal
terminal history/scrollback remains available. Use your terminal's selection
modifier when the provider captures the mouse (the modifier varies by terminal).
Browser copy behavior remains separate. Kernel PTY fixtures cover mouse bytes and
history; a physical trackpad and selection modifier still need a GUI canary.

`b` enables a bell for this navigator instance; it starts off. Needs you comes
from a native input request. Reading details does not clear it. Details or a
successful attachment acknowledge the unread response shown. A response can end
while the provider keeps running. Unknown means missing lifecycle evidence or
disabled hooks; the navigator does not infer state from terminal text.

`h` opens recent runs. Rows show outcome, output availability and cleanup.
`/` searches loaded pages; `N` loads the next page. `i` opens details and `o`
reads a bounded result. Enter checks an exact live session before attaching;
otherwise it opens the receipt. Escape returns to sessions. A saved result is
read only. Unknown outcomes retain the original request and are never replayed.

Inspect the recovery and offline commands before using them:

```sh
wt agent recover --json
wt run-inspect --help
```

See [testing](testing.md) for the offline daily-loop gates,
[SSH routes](ssh-remote-sessions.md) for transport limits, and
[Claude parent conversations](wt-claude.md) for child work.
See the [browser overview](../README.md#4-browser-visibility-self-hosted-roost)
for browser setup. Real provider, real SSH and physical terminal checks remain
separate from isolated fixture tests.

## Persistent shells and scripted session control

The CLI also exposes persistent shells, arbitrary commands, and raw terminal
operations:

```bash
wt terminal --name work                # persistent sandboxed shell
wt terminal --name api -- npm run dev  # persistent arbitrary command

# Ctrl+B Q detaches without stopping the process
wt attach                              # list live sessions
wt attach work                         # reattach by name or ID

wt session ps --json
wt session read api --json
wt session send api r --enter --json
wt session wait api --contains ready --json
wt session rename api frontend --json
wt session fork api --name experiment --json  # Claude only; keeps the source intact
wt session kill frontend --json
```

Terminal snapshots are ANSI state. Wingthing does not infer that an agent is
done because a string appeared on screen. Use `agent_run`, `agent_wait`, and
`agent_result` when the caller needs semantic task state.

## Coordinator reports

### Coordinator home and session activity

The web Home leads with explicitly pinned coordinator sessions. Open a pinned
thread to read and message it; the terminal is available from that same session.
The Activity tab shows native state changes, human input requests, reported
updates, artifacts and links. Opening a thread only reads; it never grants
approval. Artifact access retains the session's existing file permissions.

A coordinator can publish its current intent through the owner-scoped
`session_report` MCP tool (qualified by `wing_id` on direct connections), or
from the CLI:

```sh
wt session report SESSION --json <<'JSON'
{"goal":"Finish the release review","update":"Checks passed; results are ready","next_action":"Review the results","presence":"asleep","last_wake":"2026-10-12T22:00:00Z","next_wake":"2026-10-12T23:00:00Z","artifacts":[{"label":"Results","path":"results.md"}],"links":[{"label":"Documentation","url":"https://wingthing.ai/docs"}]}
JSON
```

Reports persist in the session journal and survive browser reloads. Goal,
update and next action are required; wake times and up to eight artifacts and
links are optional. Native work and process exit override reported presence.
Missing or stale heartbeat evidence is unknown; a missed reported wake is
overdue. Wake times describe a plan: reporting does not create a timer or
change provider readiness, grants, approval gates or scheduling policy.

