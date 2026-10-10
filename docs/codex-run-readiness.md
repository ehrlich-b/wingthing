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

For a new Codex `agent_run`, the wing passes its admitted prompt and absolute
deadline to the egg. Before launching Codex, the egg persists the run and arms
both the execution deadline and a 30-second readiness bound. The egg
uses a private `0600` request file for the wing-to-egg handoff and deletes it
after reading. Codex receives no positional prompt. After process startup, the
egg waits for the rendered `OpenAI Codex` header and `Ask Codex to do anything`
composer, refuses known startup modals, and bracketed-pastes the reserved prompt
once under the input lease. This is a declared screen fallback for first-input
transport only: an empty TUI has no native readiness signal. Native readiness
still requires exact `SessionStart` and `UserPromptSubmit` identities and text.
The existing reservation and timers precede both provider launch and input.

Codex's external notify callback also carries prompt text in argv, so generated
run arguments disable it with `notify=[]`. Completion requires the matching
native Stop hook and a flushed `task_complete` record for that same turn, with
identical final text, in the exact transcript named by the native hooks. The
transcript must be under this provider home's `.codex/sessions`, its metadata
must match the bound thread, and symlinks are refused. No rollout inventory or
recency search occurs. A Stop hook alone cannot complete a run. Reading legacy
egg notify spools remains supported for already-running eggs.
Native `task_complete.error` records retain typed failure classification without
requiring a Stop hook, and their arbitrary diagnostic text is discarded.

Missing native admission ends as `provider_not_ready`; cleanup kills the same
provider process group. The error contains only an allowlisted classification
of the last rendered screen, with all arbitrary screen text discarded. A
native receipt cancels the startup timer while the absolute execution deadline
continues. Client exit and wing restart never resubmit the initial prompt.

The fake Codex models the first-turn hook boundary and can display an unexpected
startup modal. Tests cover absence of empty-startup hooks, completion before
socket readiness, exact result recovery after client exit and wing restart,
modal failure, diagnostic redaction and timer behavior under virtual time.

Codex run prompts accept up to 1 MiB of UTF-8 text; `session_prompt` retains its 64 KiB
bound. Wrapper RPC and private-file limits allow JSON escaping overhead. Large
Codex prompt and Stop hooks have a separate bounded record allowance; oversized
startup, foreign and malformed hooks still cannot bind a thread.

An isolated probe of installed Mac Codex 0.159.3 against a loopback Responses
stub confirmed empty-composer hook ordering, exact 1 MiB PTY submission, Stop payload and
the final `task_complete` record. The process regression sends a 1 MiB prompt
through built stdio, wing and egg, disconnects the client, restarts wing run
observers, and checks exact completion plus wrapper/provider argv. It checks
`/proc/*/cmdline` on Linux and `ps` where permitted. This Mac agent sandbox
blocks `ps`; those OS-listing checks and Linux provider acceptance remain
separate host gates. The probe used no real login or live model request.

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
