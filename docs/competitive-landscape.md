# Competitive landscape

Status: independent research
Reviewed: 2026-08-09

Springboard: `local-first-architecture.md` (Herdr review) and
`native-sandbox-landscape.md` (agent-native sandboxes). This doc verifies those
findings and extends them into three categories they did not cover: remote
access, protocol standardization, and parallel-agent isolation.

## Herdr — verified

Codex's read holds up. Herdr is a real, Rust, terminal-native agent runtime and
multiplexer: background session server, clients attach and detach, named sessions
(`herdr session attach work`), `herdr --remote <host>`, and per-pane agent state
shown as `working` / `blocked` / `done` / `idle`. The runtime-not-dashboard framing
and the SSH-first remote story are accurate as described.

The strategic conclusion in that doc stands: do not race to be a less mature
Herdr. It owns the agent-aware multiplexer lane.

## The category codex missed: remote access is now contested

This is the most important finding, because it hits wingthing's strongest pillar.

**Anthropic ships Remote Control in Claude Code.** `claude remote-control` starts
a local server; you drive the session from claude.ai/code or the Claude mobile
app. Execution and filesystem stay local; the web/mobile UI is a window into it.
It has `--spawn worktree` for per-session git worktrees, `--capacity 32`,
`--sandbox`, QR-code pairing, push notifications, and a Trusted Devices beta with
biometric step-up on Team/Enterprise.

That is a large overlap with `wt wing`. The constraints are where wingthing still
differentiates:

| Constraint on Remote Control | Wingthing's position |
|---|---|
| Claude only | Agent-agnostic: seven agents today |
| Requires a claude.ai subscription; API keys not supported | No account required at all |
| **Disabled when `ANTHROPIC_BASE_URL` points anywhere but `api.anthropic.com`** | Provider substitution is a tested release gate (LiteLLM, Ollama, local models) |
| Transcript stored on Anthropic servers | Relay is a dumb pipe; E2E encrypted, relay cannot read |
| Not available on Bedrock, Vertex, Foundry | Transport-agnostic |
| Incompatible with Zero Data Retention policies | Self-hosted roost |
| Local process must keep running; ~10 min network outage kills it | Eggs are detached process sessions with PTY persistence |

The line that matters most: **Remote Control turns itself off if you point the
agent at an LLM gateway or a local model.** Anyone running through a proxy, a
self-hosted gateway, or Ollama cannot use it. That is precisely wingthing's
audience, and it is a durable architectural constraint rather than a temporary
gap.

**Independent tools in the same lane:** VibeTunnel (browser terminal proxy for
Mac, paired with Tailscale/ngrok/Cloudflare for reachability) and Omnara (YC S25,
mobile and voice-first, sessions that survive the laptop going offline).

**Implication:** "remote access to your agent" is no longer a differentiator on
its own. The defensible framing is the combination — *sandboxed, agent-agnostic,
self-hosted, E2E encrypted, works with local models.* The README and product copy
should lead with that combination, not with remote access alone.

## Protocols: ACP is the real story

Zed's **Agent Client Protocol** is JSON-RPC over stdin/stdout between a client
and a coding agent. Introduced August 2025, co-launched with JetBrains January
2026, headline feature of Zed 1.0 in April 2026, now adopted across JetBrains,
Google, GitHub and 25+ agents.

Why it matters here specifically: `session/load` replays a full conversation as
typed notifications, and `session/resume` reconnects without replay. That is a
protocol-level answer to the headless-to-TUI handoff problem and to "read the
conversation programmatically" — see `headless-handoff-design.md`.

`agent-support.md` gestured at this with Goose/ACP and Pi/JSONL-RPC as candidate
integrations. The correct conclusion is stronger: **ACP is becoming the standard
substrate, and wingthing should be an ACP client**, keeping PTY and stream-json
as declared fallbacks for agents that do not speak it.

MCP remains the complement, not the competitor: MCP connects agents to tools,
ACP connects clients to agents. Wingthing wants to be on both sides — an ACP
client driving agents, and an MCP server exposing its own runtime.

## Sandboxing: our design is validated, our lead is narrowing

Anthropic open-sourced **sandbox-runtime** (`anthropic-experimental/sandbox-runtime`,
on npm and PyPI) — the sandbox behind Claude Code. It uses `sandbox-exec` with
generated Seatbelt profiles on macOS and bubblewrap on Linux, and it sandboxes
arbitrary processes, not just agents.

Its network design is the notable part: **the network namespace is removed from
the bubblewrap container so all traffic must go through the proxies**, and the
Seatbelt profile allows only localhost proxy ports. That is exactly the fix
recommended for our Linux gap — keep `CLONE_NEWNET`, force traffic through a
proxy — now confirmed as the approach a vendor shipped rather than a theory.

The competitive read is two-sided:

- **Validated:** our macOS approach and the proposed Linux fix match what
  Anthropic converged on independently.
- **Narrowing:** per-agent sandboxing is becoming table stakes. `native-sandbox-
  landscape.md` is right that the wingthing layer gets thinner over time.

What stays ours, per that doc's own matrix: resource limits (cgroups v2 +
prlimit), deep seccomp, env var allowlists, audit, and agent-aware auto-drilling.
Plus the framing no agent vendor will do: *one sandbox policy across every agent
and every non-agent process.*

## Parallel agents: worktree plus isolation is the settled pattern

**Container-use** (Dagger) is an MCP server giving each agent its own container
*and* its own git worktree — supported from Claude Code, Cursor, and Goose. Claude
Code's own `--spawn worktree` does the worktree half natively.

The orchestration tier above it is crowded: Conductor, Vibe Kanban, Claude Squad
(tmux + worktrees + TUI), agent-deck, agent-manager, Gastown.

**Implication:** the TODO item "facilitate worktrees" is not a nice-to-have; it
is the expected shape of parallel agent work, and wingthing is unusual in already
having the *isolation* half (eggs) without the *worktree* half. Combining them —
worktree per session, overlay diff per session, sandbox per session — is a
differentiated version of a pattern the market has already validated. The overlay
upper dir already present in `setupOverlayHome` makes the review step nearly free.

## Where this leaves positioning

Three of wingthing's four pillars now have credible competition:

- Remote access → Claude Code Remote Control, VibeTunnel, Omnara
- Agent-aware terminal → Herdr
- Sandbox → sandbox-runtime, container-use, and every agent's native sandbox

The intersection is still unoccupied. Nobody else offers a sandboxed,
agent-agnostic, self-hosted runtime with persistent terminals, an E2E-encrypted
relay the operator cannot read, audit, and a model-facing API — that works with
local models and needs no vendor account.

That is a real position, but it is an *intersection* position, which means the
product copy has to name the combination explicitly. Leading with any single
pillar now invites an unfavorable comparison to a better-funded tool that does
that one thing.

## Sources

- [Herdr](https://herdr.dev/), [concepts](https://herdr.dev/docs/concepts/), [persistence and remote](https://herdr.dev/docs/persistence-remote/)
- [Claude Code Remote Control](https://code.claude.com/docs/en/remote-control)
- [Agent Client Protocol](https://agentclientprotocol.com/protocol/session-setup), [Zed ACP](https://zed.dev/acp)
- [anthropic-experimental/sandbox-runtime](https://github.com/anthropic-experimental/sandbox-runtime)
- [Claude Code sandboxing docs](https://code.claude.com/docs/en/sandbox-environments)
- [Dagger container-use](https://dagger.io/blog/agent-container-use/), [Zed on container-use](https://zed.dev/blog/container-use-background-agents)
- [VibeTunnel](https://vibecodinghub.org/tools/vibetunnel), [Omnara](https://remote.omnara.com/)
- [awesome-cli-coding-agents](https://github.com/bradAGI/awesome-cli-coding-agents), [open-source agent orchestrators](https://www.augmentcode.com/tools/open-source-agent-orchestrators)
