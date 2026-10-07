# SSH remote sessions

Use a configured OpenSSH host or alias and an existing workspace on that host.
The host must already have a compatible Wingthing binary and an authenticated
provider CLI. Wingthing uses the remote owner's configuration and credentials;
it does not copy a workspace or install a binary.

Register machines once to list their sessions together:

```sh
wt remote add work me@work1
wt remote add lab lab-alias --wingthing-dir /home/me/isolated-state
wt remote ls
wt session ps
wt session ps --json
wt attach work:review
wt remote rm lab
```

The registry is `~/.wingthing/remotes.yaml` (or the selected Wingthing state
directory), written atomically with mode 0600. Names use letters, digits, `-`,
and `_`; SSH targets cannot start with `-` or contain whitespace or control
characters. `--wingthing-dir` selects an absolute state path on the receiver,
with the same checks as `--remote-state`. Add/remove only change the registry.

`session ps` includes local sessions (`machine: "local"`) and every configured
remote (`machine` is its registered name). Queries run in parallel with a
three-second deadline per remote. An unavailable or incompatible receiver
produces one row with an `error` field in JSON and a diagnostic in the table;
other sessions still appear and the command succeeds. Receivers must support
the versioned, local-only `session ps --json` inventory contract; an old or
different contract reports the remote name and both versions. A plain
`wt attach SESSION` stays local; `NAME:SESSION` resolves only a registered name.
The existing `--remote SSH_TARGET` route remains available.

MCP `terminal_list` accepts `{"remote":"work"}` to list one configured remote,
with its existing principal filter. Omitting `remote` keeps the local-only
response. Connections bounded to local paths or conversations cannot select
a remote. No other MCP tool accepts this argument. This uses the SSH login
account's existing access and requires no Wingthing account or relay.

```sh
# Discover before creating anything. This command never attaches or launches.
wt --remote work1 --json

# Launch a sandboxed agent. --json starts detached and returns its exact ID.
wt --remote work1 egg codex --cwd /home/me/projects/example --name review --json

# Use the ID from the launch response for automation. Names work for people.
wt --remote work1 attach review
# Detach with Ctrl+B, then Q. The process keeps running on work1.

# Close the connection, then inspect and attach the same session again.
wt --remote work1 session ps --json
wt --remote work1 session read SESSION_ID --json
wt --remote work1 attach SESSION_ID

# Scripted input and a terminal-output condition do not require a TTY.
wt --remote work1 session send SESSION_ID --stdin --enter --json < prompt.txt
wt --remote work1 session wait SESSION_ID --contains 'expected text' --timeout 30s --json

# Explicit stop affects only this session and confirms its process exited.
wt --remote work1 session stop SESSION_ID --json
```

`session read` is a raw terminal snapshot. `session wait --contains` observes
terminal text; neither operation proves agent readiness or semantic completion.
Choose a headless agent run through MCP when a semantic final result is required.

An attachment has the same detach chord locally and over SSH. Closing its input
also detaches the client. The CLI waits for the server to acknowledge detach
after processing preceding input. Detachment, cancellation, and a transport interruption
do not request that the egg stop its process. If an attachment loses its stream
before receiving an exit event, the CLI reports an unconfirmed exit. SSH status
255 means the session's state is unknown. Reconnect and list sessions before
deciding whether another launch is needed. The CLI never retries a launch.

Bare `wt --remote work1` on a terminal attaches the sole session or offers a
picker. Add `--remote-cwd /existing/path` to restrict the picker; if there are no
matching sessions, it starts a sandboxed shell in that directory. Bare entry
without a terminal prints JSON and never starts a shell. Use explicit `--json`
for a read-only inventory even when running from a terminal.

All command paths, agent arguments, empty arguments, and whitespace are quoted
for the remote POSIX shell. Put the provider immediately after `egg` and its own
flags after `--`. A path in `--cwd` or `--config` belongs to the remote machine.
Use `--remote-binary /absolute/staged/wt` when its executable is outside PATH.
Ports, bastions, identities, and host-key policy remain OpenSSH configuration;
Wingthing does not relax them or accept SSH options in the host argument.

Preview uses a separate executable on both ends:

```sh
wt-preview --remote work1 --json
wt-preview --remote work1 --remote-binary /home/me/preview/wt-preview attach review
```

The default remote executable is `wt-preview` for a preview client. Every
preview request carries an executable channel check, including requests to an
explicit staged path. A stable or older executable fails before it can open
stable sessions or configuration. Install the preview artifact separately on
the remote host before using this route. Stable clients keep their legacy
remote argv so existing stable remote installations remain compatible; only
`--remote-state` adds a stable channel check (see below).

### Selecting remote state

`--remote-state /absolute/path` selects the receiver's state directory without
a wrapper script. Repeat it on every command for the same isolated runtime;
sessions in one state directory are invisible from another.

```sh
S="/home/me/wt preview/it's isolated"
wt-preview --remote work1 --remote-state "$S" channel --json
wt-preview --remote work1 --remote-state "$S" terminal --cwd /home/me/work --json -- /bin/sh
wt-preview --remote work1 --remote-state "$S" session ps --json
```

The client parses the flag before `--`, so an agent argument with the same
spelling is never consumed. It must be a non-empty POSIX absolute path and may
appear once. Like `--remote` and `--remote-binary`, it is rejected if it
contains NUL, CR, or LF anywhere. Every other byte, including tab and other
control bytes, is a legal POSIX path byte and reaches the receiver literally
inside single quotes. The client never resolves the path: `..`, symlinks, and
`~` are meaningful only on the remote host, and `~` is rejected as relative.

With the flag, the remote command becomes
`WINGTHING_DIR='…' WINGTHING_PREVIEW_DIR='…' 'wt-preview' '--expected-channel' 'preview' …`
from a preview client, or
`WINGTHING_DIR='…' WINGTHING_PREVIEW_DIR='…' 'wt' '--expected-channel' 'stable' …`
from a stable client. The command is single-quoted for the remote POSIX login
shell, and shells such as csh or fish do not accept the assignment prefix.
Without the flag, the remote command is byte-identical to earlier releases:
stable sends no channel check and no assignments.

The receiver therefore needs to be a release that understands
`--expected-channel` and checks the state directory's channel marker. Stable
receivers released before this check (v0.147.0 and earlier) reject
`--expected-channel` as an unknown flag during argument parsing, before they
read `WINGTHING_DIR` or open any state. Upgrade the remote stable `wt` before
using `--remote-state` from a stable client. A preview receiver always needs
the separate preview artifact.

A compatible receiver applies its own checks before creating or opening
anything. A preview receiver refuses the remote owner's stable `~/.wingthing`,
an alias of it, a stable-marked directory, and an unmarked non-empty directory.
A stable receiver refuses a preview-marked directory when a command loads its
configuration. `channel --json` is read-only and reports the receiver's
`release_channel`, `executable`, `version`, and exact lexical `state_dir`. On a
preview receiver it runs only after the state checks pass, so it can confirm a
selection before launching. On a stable receiver it reports the path without
checking the directory's channel marker; the first command that loads
configuration applies that check. After SSH status 255, reconnect with the same
`--remote-state` and list sessions before relaunching.

The flag selects only the receiver's state directory. It does not change
`HOME`, provider credentials, sandbox policy, or which account SSH logs in as.
It does not copy or synchronize a workspace, memory, or credentials between
hosts. `--cwd` still names an existing path on the remote host. Selecting a
different state directory does not separate users or hosts: processes still
run as the SSH login account, and the egg sandbox is the only enforcement
boundary.

Stable eggs retain their existing shared-input behavior. Preview eggs permit
one writer for both input and resize. A second writer receives an ownership
error before submitting input; taking control requires an explicit action.

```sh
wt-preview --remote work1 attach SESSION_ID --read-only
wt-preview --remote work1 attach SESSION_ID --takeover
wt-preview --remote work1 session ps --json
```

Read-only attachments keep receiving replay and live output without claiming
input or resize. Takeover revokes the previous stream and advances an input
epoch. Detach or connection loss releases the writer; none of these operations
stop the provider process. Public status includes `writer_id`, `writer_owner`,
`input_epoch`, and reader count. A private attachment token binds unary resize
to the current writer; public status alone cannot authorize a resize.

The egg owns this lease across CLI, browser, and MCP. Browser attachment IDs
bind resize and detach to their acknowledged connection, including encrypted
tunnel and direct-channel requests. An old tab cannot resize or detach its
replacement. The daemon keeps a separate observer for lifecycle and attention
while a browser is detached. Existing spectator policy still determines whether
a browser may observe; this feature does not enable new access.

The local acceptance fixture replaces SSH alone. It drives the actual CLI,
separate sandboxed egg processes, private Unix endpoints, replay, two exact
session identities, detach and reattach, stdin forwarding, and stop cleanup.
Run it with `make gate GATE=integration`. `make gate GATE=input` additionally drives the
actual encrypted browser bridge against a disposable preview egg, with two
observers, MCP/browser takeover, stale resize/detach rejection, connection loss,
an unchanged provider PID, and lifecycle delivery after detach. It replaces the
browser DOM and network relay with typed message queues. A physical-host check
remains necessary for SSH, installed binary and sandbox compatibility; a live
browser pass remains necessary for the UI affordances.

On Linux, `make gate GATE=integration` drives `--remote-state` end to end through the same
kind of fake SSH transport. It uses the stable and preview builds as both
clients and receivers, and runs sandboxed `/bin/sh` eggs that the fixture owns.
The selected state path contains spaces and an apostrophe. The fixture checks:

- without the flag, the exact legacy stable and preview command strings and
  default state are unchanged; with it, both channels send the exact quoted
  assignments and `--expected-channel`;
- `channel --json` is read-only and reports the lexical selected state;
- two preview sessions run only in the selected state, and `session ps`
  reports `wingthing-sandbox` isolation from each running egg's metadata;
- two read-only readers observe output without claiming the writer;
- detach by name, ID, and input EOF leaves sessions running;
- SSH 255 before and after remote execution reports unknown state, without
  retry. Reconnect inspection finds the input delivered after execution;
- reconnect preserves exact IDs, names, and egg PIDs;
- a preview client fails before writes against a stable receiver, stable
  state, or a stable alias, and a stable receiver fails before writes against
  preview state;
- the stable sentinel egg keeps its PID, and the sentinel file and every binary
  keep their hashes.

Launches never fall back to `--unsandboxed`. The fixture prints a JSON receipt;
on failure it names the step, exact argv, stderr, and egg logs. Cleanup stops
every egg under the fixture's temporary tree, including any it did not record,
before removing the tree. The fixture does not exercise real OpenSSH or sshd,
receivers older than this release, macOS Seatbelt, or separate hosts.

The existing work1/work2 SSH route was inspected separately. Their installed
stable binary rejected the new `_remote` entry command; it was not replaced.
Physical preview acceptance needs an admitted temporary workspace and a
separately staged preview executable, selected with `--remote-binary` and
`--remote-state`; a wrapper script is no longer needed. The flag does not
isolate remote `HOME`. The preview channel check still reaches the real binary.
The cloud route has no verified SSH identity; WSL admission remains separate.
No test adds keys, trusts unknown hosts, opens SSH listeners, changes services,
or alters a shared host's access policy.
