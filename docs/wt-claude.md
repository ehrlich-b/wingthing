# Claude across a Mac and an SSH wing

`wt claude [--name NAME] [-- claude args]` asks a running local wing to start a
persistent Claude parent egg, injects one scoped Wingthing MCP mailbox, and
attaches your terminal through the wing. The wing starts providers and owns
SSH connections. Closing a viewer or MCP connection leaves accepted worker
eggs running on their owning machines.

The protected parent mailbox currently requires macOS. An SSH worker wing can
run on Linux with its supported sandbox. Install a compatible Wingthing build
and authenticate the provider CLI on each execution machine beforehand. Each
machine needs its own existing workspace and personal credentials. Authorize
the `forge` SSH alias before proceeding.

## Start two independent wings

Use fresh state for this workflow. On the Mac:

```sh
export WINGTHING_DIR="$HOME/.local/share/wt-weekend"
wt wing start --local-only --paths "$HOME/repos"
```

Provision Forge's wing once:

```sh
ssh forge 'WINGTHING_DIR="$HOME/.local/share/wt-weekend" wt wing start --local-only --paths "$HOME/repos"'
```

Remember that already running wing from the Mac:

```sh
wt mcp connect add forge --ssh forge --wingthing-dir '~/.local/share/wt-weekend'
wt mcp connect ls
```

If the compatible remote binary is outside PATH, add
`--wt-binary /absolute/path/to/wt` to `connect add`. This command verifies and
remembers the remote; it does not install software or start a wing. No roost,
Wingthing login, relay, or WebRTC is needed.

## Start the parent and two workers

From an allowed Mac workspace:

```sh
cd "$HOME/repos"
wt claude --name weekend
# Native Claude flags go after --, for example:
# wt claude --name research -- --model sonnet
```

The parent uses the injected mailbox. Additional `--mcp-config` or
`--strict-mcp-config` arguments are rejected so the execution retains exactly
one MCP configuration. An absent wing returns startup guidance; this command
does not silently start a daemon.

Paste this into Claude:

```text
Use Wingthing MCP. Call wing_list and wingthing_capabilities; resolve local and
forge to wing IDs. On EACH wing, call agent_run with agent=claude and omit
timeout_seconds for no deadline. Use label=weekend-mac locally and label=weekend-forge
on forge, with this prompt:
"In your existing workspace, read the project instructions and inventory the
repositories read-only. Give me a short architecture map; do not modify files."
Use an advertised allowed workspace on each wing, never copy my Mac cwd to forge.
Show both durable run_id/session_id/wing_id receipts as soon as admitted.
Wait for both results; do not stop either worker if this terminal disconnects.
```

Each admission returns a receipt before the worker finishes. Save its
`wing_id`, `run_id`, `session_id`, and `idempotency_key`. If admission reports an
unknown outcome, retry the original request with the same key. A replacement
key asks for a separate run.

This scoped mailbox uses ordinary `agent_*` calls. Native MCP task augmentation
remains available through the authorized aggregate `wt mcp connect` transport.

## Disconnect and recover

After both receipts, detach with **Ctrl+B, then Q** and close the viewer. In a
new Mac terminal:

```sh
export WINGTHING_DIR="$HOME/.local/share/wt-weekend"
wt attach weekend
```

Tell the parent:

```text
Reconnect to forge and read both recorded runs with agent_status and
agent_result; do not relaunch them.
```

The parent keeps the same session. A replaced mailbox connection or SSH forward
can recover the same child IDs, transcripts, and semantic results. To observe a
worker, substitute the actual receipt's session ID:

```sh
wt attach --read-only forge:SESSION_ID
```

Read-only attachment does not claim input, resize, or change turn ownership.
With a running wing, `wt attach` checks the current client's `terminal.read` and
`terminal.send` grants; set `WT_MCP_CLIENT` when your wing requires a named
client. Without a wing, unnamed local CLI attachments retain direct access to
standalone eggs. A wing policy refusal never falls back to direct access.

Separately, exit the parent while workers are still running. A fresh authorized
MCP client using `wt mcp connect` can call `agent_status`, `agent_wait`, and
`agent_result` with the saved owning wing and run IDs. The parent's exit does
not cancel accepted workers.

Forge continues while Forge stays awake. A sleeping Mac pauses local execution;
an absolute worker deadline can expire during sleep. Keep the Mac awake in an
already configured docked or clamshell setup if both workers must compute
continuously.

## Scope and live grants

At launch the parent captures its local workspace, currently reachable permitted
remembered wings and their allowed workspaces, tool grants, and finite spawn
bounds. The wing records every admitted child's actual owning wing. Each call
intersects that snapshot with current grants. Foreign child IDs, ungranted
wings, revoked grants, and paths outside the captured workspace are rejected.
Start a new parent to use newly granted wings or wider paths; existing parents
retain their launch ceiling. Legacy bound mailboxes keep their original tools.

With a `clients.yaml` entry, explicitly grant remote stable wing IDs and
absolute paths on those remote hosts using `wings`. For example, replace the
ID and workspace below with values advertised by Forge:

```yaml
clients:
  default:
    grants: [capabilities.read, wing.read, terminal.read, terminal.send, terminal.start, agent.run, agent.read, agent.stop]
    bounds:
      max_sessions: 8
      max_spawns_per_hour: 60
    wings:
      FORGE_STABLE_WING_ID:
        - /home/me/repos
```

Keep `clients.yaml` private (`chmod 600`). Without configured clients, the
personal owner captures reachable remembered wings under their advertised
workspace policy. The receiving wing also checks its current grants and
canonical paths before execution.

The parent cannot access SSH keys, the unrestricted wing socket, or protected
wing state. The workspace mailbox remains a **same-owner workspace trust**
boundary: another process able to write that mailbox has its scoped authority.
It is not authentication between processes sharing a writable workspace.

## Verify the native path

On an unsandboxed Mac, the isolated fake-provider acceptance test runs built
`wt` processes, two local-only wings, fake SSH, fake Claude, and fake Codex:

```sh
nice -n 15 go test -p 2 ./cmd/wt -run '^TestBuiltWTClaudeScopedMailboxTwoLocalOnlyWings$' -count=1 -v -timeout=180s
```

A host that forbids nested `sandbox-exec` skips that native gate explicitly.
`TestBuiltClientScopedParentFakeProvidersSurviveDisconnect` in
`internal/localmcp` verifies the built client, mailbox, child runtime, and
recovery protocol through a declared fixture boundary; it does not prove OS
sandbox enforcement. The real Mac-plus-Forge workflow above remains the
provider compatibility gate.
