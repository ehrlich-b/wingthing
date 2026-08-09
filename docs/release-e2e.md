# Live release E2E

The release smoke test answers one deliberately small question: can a real
model, behind a real supported harness, make a real tool call through
Wingthing and leave the intended result in the intended working directory?

The success signal is the exact 12-byte file `Hello World!`. A zero exit code,
a startup banner, a completion marker, or prose that merely describes a tool
call does not pass.

## Matrix

`make test-provider-swap` runs these opt-in cases:

| Harness | Provider path | Direct control | Through Wingthing | Assertion |
|---|---|---:|---:|---|
| Claude Code | Anthropic Messages -> LiteLLM -> Ollama Qwen3 4B | yes | yes | Claude `Write` creates exact file |
| Codex | OpenAI Responses -> LiteLLM -> Ollama Qwen3 8B | yes | yes | Codex command execution creates exact file |
| Gemini CLI | Gemini generateContent -> LiteLLM -> Ollama Qwen3 8B | yes | yes | Gemini `write_file` creates exact file |
| Hermes | custom OpenAI endpoint -> Ollama Qwen3 8B | yes | yes | Hermes `terminal` creates exact file |
| OpenCode | OpenAI-compatible provider -> Ollama Qwen3 8B | yes | yes | OpenCode `write` creates exact file |
| Ollama | native chat/tool API, plus Wingthing text adapter | yes | yes | exact structured call is safely dispatched; adapter returns marker |

The same fresh-home run also drives Wingthing as an MCP server and verifies
14-tool discovery, a raw `prompt_run`, a saved/versioned prompt render and run,
an early-stopping `prompt_loop`, and a dependency-aware `swarm_run`, all with
the local Ollama adapter. The default matrix therefore has 17 independently
reported assertions. MCP discovery also compares the catalog's
`provider_substitution` and `release_canary` declarations with the harnesses in
this matrix. Adding a provider-substitutable agent to the catalog without adding
its live release canary makes the gate fail.

Cursor Agent remains in the ordinary real-startup battery. It can select models
inside its vendor boundary but does not expose the same arbitrary local/provider
substitution contract, so this matrix does not claim that Qwen is running inside
it. A `--model` flag alone does not qualify: the release matrix is for a harness
whose provider endpoint can be redirected to a non-vendor or local
implementation without patching that harness.

## Prerequisites

The smoke host needs:

- branch-built `wt`
- real `claude`, `codex`, `gemini`, `hermes`, `opencode`, and `ollama` executables
- Ollama listening on loopback with `qwen3:4b` and `qwen3:8b`
- LiteLLM listening on loopback with `test/live/litellm-config.yaml`

The pinned LiteLLM environment is intentional. LiteLLM 1.95.0 currently
declares a FastAPI range whose newest resolution is incompatible with its
proxy import, so the canary pins FastAPI 0.136.3 in
`test/live/litellm-requirements.txt`.

Example service setup:

```bash
ollama pull qwen3:4b
ollama pull qwen3:8b
python3 -m venv /opt/wingthing-agent-canaries/litellm-venv
/opt/wingthing-agent-canaries/litellm-venv/bin/pip install -r test/live/litellm-requirements.txt
/opt/wingthing-agent-canaries/litellm-venv/bin/litellm \
  --config test/live/litellm-config.yaml --host 127.0.0.1 --port 4000
```

The provider shapes follow the upstream supported extension points: Claude
Code's gateway environment, Codex custom model providers using the Responses
wire API, Hermes's custom OpenAI-compatible provider, and OpenCode's local
Ollama provider.

## Running on NewPC

NewPC keeps real binaries behind `*-real` canary names so the synthetic Linux
suite can continue to own the ordinary command names. Run the matrix from the
WSL checkout with explicit paths:

```bash
WT_SMOKE_WT_BIN=/usr/local/bin/wt \
WT_SMOKE_CLAUDE_BIN=/usr/local/bin/claude-node \
WT_SMOKE_CODEX_BIN=/usr/local/bin/codex \
WT_SMOKE_GEMINI_BIN=/usr/local/bin/gemini-real \
WT_SMOKE_HERMES_BIN=/usr/local/bin/hermes-real \
WT_SMOKE_OPENCODE_BIN=/usr/local/bin/opencode-real \
WT_SMOKE_OLLAMA_BIN=/usr/local/bin/ollama-real \
make test-provider-swap
```

Run `python3 test/live/provider_swap_smoke.py --phase direct` to isolate
provider/harness failures or use `--phase wingthing` to retest only the
Wingthing boundary. On failure the script retains its isolated workspace and
per-case logs under `/tmp`; on success it removes only that self-created
workspace. It never uses the daily Wingthing database, deploys a roost, tags a
release, or touches Slide.

## Compatibility controls

These are part of the verified contract, not hidden test indulgences:

- `WT_PROVIDER_BASE_URL` tells Wingthing that a swapped provider is the network
  boundary. A loopback value selects local-only networking and avoids injecting
  a cloud domain proxy into the harness.
- Codex receives `--skip-git-repo-check` because Wingthing tasks may run in any
  directory. When and only when Wingthing supplies the outer sandbox, its
  adapter disables Codex's inner sandbox; nested Linux `bwrap` namespaces fail.
- The Codex canary disables reasoning. LiteLLM 1.95.0 otherwise maps Codex's
  structured Responses reasoning object into Ollama's scalar `think` option and
  fails before inference.
- Gemini CLI 0.54.4 uses its native Gemini `generateContent` protocol against
  LiteLLM. Its fresh-home settings explicitly select `gemini-api-key` auth;
  auto-selecting the newer internal `gateway` type currently fails the CLI's
  own auth validator before the otherwise valid local request is sent.
- `WT_HERMES_TOOLSETS=terminal` narrows Hermes's large tool catalog for a small
  local model. Hermes also requires a declared 64K runtime context for reliable
  tool use.
- OpenCode receives `--auto` only inside Wingthing's outer sandbox. Its prompt
  names the real `write` function; a vague request can make a small model print
  plausible JSON without issuing a tool call.

This is a promotion gate, not a claim that every task will succeed with an 8B
model. It proves that Wingthing preserved cwd, environment, provider routing,
sandboxing, harness tool dispatch, process completion, and the resulting
filesystem effect for one deterministic task.
