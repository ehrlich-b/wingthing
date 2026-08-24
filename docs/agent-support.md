# Supported agent evidence

Status: vacation branch verification snapshot  
Verified: 2026-08-08

“Supported” is not a boolean. Wingthing tracks several independent contracts:

1. **Catalog:** discovery, interactive command, unattended/resume flags, sandbox
   storage, network, and credential requirements.
2. **Adapter:** exact headless argv/stdin contract and output parsing.
3. **Synthetic PTY:** relay start/output/input/exit behavior under the agent name.
4. **Real startup:** the actual published CLI emits PTY output inside the Linux
   sandbox, even from a credential-free temporary home.
5. **Live completion:** a real provider/model returns a known answer through the
   Wingthing adapter and sandbox.
6. **Orchestration:** the real adapter works through MCP, loops, and dependency
   swarms rather than only when invoked directly.

## Current matrix

| Agent | Headless contract unit test | Synthetic PTY lifecycle | Real WSL sandbox startup | Live model completion on this branch | Notes |
|---|---:|---:|---:|---:|---|
| Claude Code | yes | yes | yes, 2.1.185 | yes, Qwen3 4B via LiteLLM | Real `Write`, direct and through Wingthing |
| Codex | yes | yes | yes, 0.147.0 | yes, Qwen3 8B via LiteLLM | Real command execution, direct and through Wingthing |
| Cursor Agent | yes | yes | yes | not authenticated in canary | Uses Cursor's current `agent` executable |
| Gemini CLI | yes | yes | yes, 0.54.4 | yes, Qwen3 8B via LiteLLM | Native Gemini protocol; real `write_file`, direct and through Wingthing |
| Hermes Agent | yes | yes | yes, v0.20.0 | yes, local Qwen3 8B | Real `terminal`, direct and through Wingthing |
| Ollama | yes | yes | yes, v0.32.6 + Qwen3 4B | yes, local | 12/12 exact structured `write_file` calls; FunctionGemma 270M rejected at 0/12 |
| OpenCode | yes | yes | yes, v1.18.15 | yes, local Qwen3 8B | Real `write`, direct and through Wingthing; MCP/DAG/loop also exercised |

Real WSL startup currently also covers sandbox auto-mounts, writable agent state,
network profile injection, environment, PTY allocation, and first-output timing.
The full Linux battery covers namespace capability detection, sensitive-path
denies, home write isolation, seccomp, support bundles, doctor, trace mode, and
clear negative behavior when enforcement is unavailable.

NewPC's local Ollama canary keeps `qwen3:4b` (2.5 GB) and `qwen3:8b` (5.2 GB),
both running on its RTX 5080. The 4B model is the cheap native adapter and
structured-call canary; the 8B model handles the larger harness system prompts
and tool schemas. In 12 temperature-zero trials across three file
paths and contents, it returned exactly one correctly named structured
`write_file` call with exact arguments every time. The canary safely dispatched
one validated call and read back `Hello World!`. FunctionGemma 270M was also
tested because its size is appealing, but produced zero exact successes in 12
trials, including missing and duplicated calls. That result is retained as
comparison evidence, but the failed model was removed from NewPC so it cannot
be mistaken for the supported default.

The strongest release result is now the fresh-home provider-substitution
matrix: 17/17 assertions passed in one NewPC WSL run. Claude Code, Codex,
Gemini, Hermes, and OpenCode each created the exact artifact both directly and
through Wingthing; Ollama produced and dispatched an exact structured tool call; and
the same run semantically verified MCP discovery, raw and versioned prompts,
an early-stopping loop, and a dependency-ordered swarm. A separate OpenCode
swarm run also returned independent `ALPHA` and `BETA` worker results and a
downstream `ALPHA+BETA` reduction. That attempt exposed OpenCode's shared
SQLite lock, so Wingthing enforces a per-agent concurrency cap of one while
still running different agents in parallel.

A three-iteration real loop also proved the bound and result injection. The
free model repeated `STEP1` after the runtime supplied that result to iteration
two, so the test correctly records model non-compliance rather than claiming an
orchestrator failure.

## What the ordinary suite proves

The current branch contains more than 600 Go tests across more than 70 test
files. A fresh `make coverage` reports 26.7% aggregate statement
coverage. That aggregate is dragged down by process-heavy CLI and relay entry
points; the new semantic cores are substantially better covered: agent catalog
80.1%, MCP 72.1%, prompt manager 74.7%, orchestrator 80.6%, sandbox 66.2%, and
store 64.4%. Coverage is a map of remaining risk, not proof that a published
agent still accepts an invocation.

`make test` exercises the adapter parsers and exact current invocations for all
seven agents without requiring credentials or spending model tokens. It also
tests MCP initialization, tool schemas, strict argument decoding, task ID
uniqueness, graph validation, dependency output injection, per-agent concurrency
limits, versioned prompt storage/provenance, process-group cancellation, stderr
propagation, store concurrency, and runtime sandbox configuration.

`make test-integ` exercises the browser/wing PTY protocol for every cataloged
agent using deterministic simulated endpoints.

The Linux E2E binary runs capability-driven tests. Unsupported kernel features
are explicit skips; an expected security feature that is available but broken
is a failure. Real CLI tests are also explicit skips unless their dedicated
`*-real` canary executable is installed.

The distinction matters: a mocked PTY test proves Wingthing routing, not that an
upstream vendor preserved its flags. A real startup proves the CLI launches,
not that credentials and billing work. A deterministic paid/free model canary
proves one configured provider path, not every account or model.

The opt-in provider-swap promotion battery is `make test-provider-swap`; its
setup, exact assertions, compatibility controls, and NewPC invocation are in
[release-e2e.md](release-e2e.md). Direct controls run beside the Wingthing path
so a provider regression is distinguishable from a routing, sandbox, cwd, or
adapter regression.

On NewPC, the complete fresh-home run passes 17/17 assertions. Its Wingthing
phase is 11/11: Claude Code, Codex, Gemini, Hermes, OpenCode, and Ollama through
Wingthing, followed by MCP discovery, raw prompt, saved prompt, loop, and
swarm. Direct controls pass for all five provider-substituted harnesses plus
Ollama's structured tool API. The catalog now declares provider substitution
and release-canary coverage explicitly, and MCP discovery makes the live test
fail if that declared set diverges from the executable matrix.

## CI gap and policy

Before this branch, CI ran only `make check`; the Linux sandbox and relay E2E
suites were outside the required checks. This branch adds separate integration
and privileged Linux-sandbox jobs so protocol and enforcement failures are
visible without weakening the fast unit job. Published-agent and paid-model
canaries remain opt-in because they require credentials, downloads, and upstream
availability.

No release should claim an agent based only on executable discovery. The
promotion bar should be:

- exact contract unit test
- catalog/profile test
- synthetic PTY lifecycle
- real credential-free startup on Linux/WSL
- at least one opt-in live completion for the headless path
- if marketed for meta-orchestration, one MCP prompt and one dependency-flow
  canary

## Next agent candidates

Prioritize agents with stable non-interactive or protocol modes. Candidate does
not mean promised support.

| Priority | Agent | Useful contract | Why it fits |
|---|---|---|---|
| 1 | GitHub Copilot CLI | `copilot -p`, JSON output, resume | Large installed base; current CLI exposes programmatic output and session controls |
| 1 | Qwen Code | `qwen -p`, stream JSON, resume, run budgets | Strong headless contract and explicit budget controls |
| 1 | Aider | `aider --message`, yes-always mode | Mature git-centric workflow and simple one-shot invocation |
| 2 | Goose | CLI/API plus ACP | Protocol-native integration may preserve more semantics than terminal scraping |
| 2 | Pi | JSONL RPC mode plus print/JSON modes | RPC is a better meta-layer substrate than treating a TUI as text |
| 2 | Amp | `amp -x` | Very small headless surface; validate auth and output stability |
| 3 | Crush | `crush run`, experimental client/server | Promising but the client/server contract should stabilize first |

The adapter abstraction should evolve from a name switch into declared
capabilities: interactive PTY, headless text, structured events, resume,
cancellation, usage reporting, tool approval, and native protocol. Wingthing can
then select the richest supported path per agent and fall back to PTY without
lying about semantic equivalence.

## Reproducing the isolated WSL canaries

The vacation machine is Windows `NewPC`, reached through the existing `ngn`
inventory. WSL is Ubuntu 24.04.3. Branch-built `wt` and `run-tests` binaries are
copied through `C:\Users\ehrli\wingthing-wsl-test`; dedicated upstream binaries
live behind `/usr/local/bin/*-real`. Gemini and OpenCode packages live under
`/opt/wingthing-agent-canaries/npm`; Hermes code/data lives under
`/opt/wingthing-agent-canaries/hermes-home`.

Ollama 0.32.6 is installed as an enabled WSL systemd service and listens only
on its default loopback endpoint. Its installed smoke models are `qwen3:4b` and
`qwen3:8b`:

```bash
ollama pull qwen3:4b
ollama pull qwen3:8b
ollama list
```

The normal `ehrli` WSL user can reach the service and model; root ownership is
not required for ordinary CLI/API use. The opt-in Linux E2E test skips when the
service or pinned model is absent.

Tests create temporary homes and Wingthing state under `/tmp`. They do not use
the daily-driver Wingthing database, reclaim running production eggs, deploy a
roost, tag a release, or change Slide infrastructure.
