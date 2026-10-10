# Codex run startup and validation

The empty interactive Codex 0.159.3 TUI does not emit `SessionStart` until
the first turn begins. Wingthing previously required that hook's exact thread
identity and idle state before sending its first prompt. That circular wait
left an admitted `agent_run` pending. The startup update modal was a separate
blocker.

This was reproduced against the installed Mac binary with an isolated scratch
`HOME` and `CODEX_HOME`, trusted generated hooks and a local HTTP stub. An
empty composer emitted no hook. Launching with the documented `[PROMPT]`
argument emitted `SessionStart` (`source: startup`) followed by
`UserPromptSubmit` with exact thread, turn and prompt identities. No live model
request or real Wingthing state was used. The startup overrides also passed
the installed binary's `--strict-config` check in this probe.

## Confirmed CLI and documentation facts

- `codex --help` in 0.159.3 supports `[PROMPT]`, `--no-daemon`, config overrides
  and `--dangerously-bypass-hook-trust`. Wingthing does not use that broad trust
  bypass; it trusts only its generated hook hashes.
- `codex features list` reports `hooks` stable, enabled by default. The
  [hook documentation](https://learn.chatgpt.com/docs/hooks) describes
  `codex_hooks` as a deprecated alias. Hook definitions can be inline in
  `config.toml` or in adjacent `hooks.json`, including the `CODEX_HOME` layer.
- Documented events: `SessionStart`/`SubagentStart` at session creation;
  `UserPromptSubmit`, tool hooks, compaction hooks, `SubagentStop` and `Stop`
  during turns; `Interrupt` on interruption; `SessionEnd` on session closure.
  An empty TUI window is not evidence that a native session has started.
- [External notify](https://learn.chatgpt.com/docs/config-file/config-advanced#notifications)
  currently supports only `agent-turn-complete`, with thread/turn identity,
  input messages and final assistant text. It cannot report startup readiness.
- The [configuration schema](https://learn.chatgpt.com/docs/config-schema.json)
  defines `check_for_update_on_startup=false`, acknowledgement notices and
  per-project trust. These session overrides suppress update, full-access,
  migration and trust UI for the selected working directory. No provider
  config files or global hook trust are modified. Existing authentication
  remains necessary; unexpected authentication or startup UI fails closed.
- `codex debug --help` offers `models`, `app-server` and `prompt-input`;
  `debug app-server --help` offers `send-message-v2`. No debug command exposes
  a documented empty-TUI readiness signal.

Verify: 0.162.1 behavior on Linux, hook trust compatibility there, live subscribed
completion/notify, and startup controls for any newer modal. The local stub
probe establishes first-turn hook ordering, not live provider success.

## Runtime contract

For a new Codex `agent_run`, the wing passes its admitted prompt and optional
absolute deadline to the egg. Omitted or zero `timeout_seconds` means no
execution deadline; positive values must be at least 10 seconds, with no upper
cap. Before launching Codex, the egg persists the run, arms any execution
deadline, and retains a 30-second readiness bound. The normal TUI
receives the prompt through argv; Wingthing sends no prompt keystrokes. The egg
then reads its own fresh hook and notify spools from their beginning, including
events that arrived before the socket became available. Completion still
requires matching native thread, turn and prompt receipts and final notify.

Missing native admission ends as `provider_not_ready`; cleanup kills the same
provider process group. The error contains only an allowlisted classification
of the last rendered screen, with all arbitrary screen text discarded. A
native receipt cancels the startup timer while any absolute execution deadline
continues. Unbounded runs still stop through `agent_stop` and end when the
provider or egg host exits. Client exit and wing restart never resubmit the
initial prompt.

The fake Codex models the first-turn hook boundary and can display an unexpected
startup modal. Tests cover absence of empty-startup hooks, completion before
socket readiness, exact result recovery after client exit and wing restart,
modal failure, diagnostic redaction and timer behavior under virtual time.

## Coordinator canary

After `nice -n 15 make check`, run outside the agent sandbox:

```sh
./scripts/real-codex-canary.sh gpt-5.6-luna
```

The script uses this checkout's `wt`, fresh state and workspace under `.scratch`,
and a private scratch copy of an existing file-backed Codex login. It copies no
user configuration or MCP servers. It starts a local-only wing, admits a
no-tools prompt through stdio, closes that client, reconnects through a new
stdio client, waits once and prints the full result. It then stops that exact
egg and isolated wing and removes scratch state after verified cleanup. It
never installs, logs in, enrolls a relay, or changes the source provider home.

## Local gates

`go vet -p 2 ./...`, the focused Codex unit/process tests and the canary script's
local JSON-RPC fixture passed. The web checks in `nice -n 15 make check` passed.
Full localmcp and cmd/wt suites passed; the egg and wingsession suites hit these
environment failures, also reproduced with a source overlay of `8510874`:

- `TestClaudeLifecycleRefusesHostSettingsOutsideFinalPolicy`: the platform temp
  path can make its socket 104 bytes, above Darwin's 103-byte limit.
- `TestDeadlineKillsProcessGroupAndReportsSurvivors`: sandbox restrictions deny
  `/bin/ps`, preventing descendant inventory.
- `TestStopUnreachableOwnedSessionTerminatesVerifiedPID`: the same `/bin/ps`
  restriction prevents the fixture from verifying its PID.

The full local-only restart fixture also intermittently retains the stopped
shell egg in its inventory assertion. This reproduced with the baseline test
and a baseline-built binary; it passed during `make check` on the changed code.
These exceptions do not establish live provider or process-containment success.
