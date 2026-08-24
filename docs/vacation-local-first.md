# Vacation local-first development window

Status: active branch policy  
Branch: `feature-local-first-terminal-routing`  
Window: 2026-08-08 through approximately 2026-08-20

## The contract

`main` is frozen while the local-first runtime work advances on the feature
branch. During this window:

- do not merge the branch to `main`
- do not create version tags or GitHub releases
- do not run Fly, production, or Slide deployment workflows
- do not replace an installed production `wt` binary
- do make coherent local commits and take large internal steps on this branch
- do test with repo-built binaries, isolated state, and localhost services

This is a deployment freeze, not a development freeze. Compatibility work,
migrations, and cleanup can accumulate here and be reviewed as a whole after
the vacation window.

## Safe local loop

Build and run the branch binary directly from the repository:

```bash
make build
export WINGTHING_DIR="$PWD/.local-state"
./wt terminal --name work
```

`.local-state/` is ignored by git. A temporary directory is also suitable for
tests. Keeping a separate `WINGTHING_DIR` prevents the branch binary from
reclaiming or mutating sessions owned by an installed daily-driver build.

Useful dogfood commands:

```bash
./wt terminal --name shell
./wt terminal --name server -- npm run dev
./wt egg claude --name local-first

./wt attach
./wt attach --select
./wt attach server

./wt session ps --json
./wt session read server
./wt session send server r --enter
./wt session wait server --contains ready --timeout 20s
./wt session rename server frontend
./wt session kill frontend
```

The branch also has a local LLM control surface:

```bash
WINGTHING_DIR="$PWD/.local-state" ./wt mcp stdio
```

It exposes terminals, named/versioned prompt templates, prompt runs, bounded
loops, and bounded dependency swarms over MCP stdio. MCP clients inherit the
authority of the local user that starts the server, so continue to use isolated
`WINGTHING_DIR` and test homes while dogfooding. See
`docs/agent-meta-layer.md` for the object and safety model.

`wt session read` intentionally returns the terminal's ANSI snapshot. It is a
raw terminal primitive, not a chat transcript. Agent-aware state and structured
events belong in the layer above it.

## Work tracks

The branch can make major progress in this order:

1. Local runtime ergonomics: names, shells, arbitrary commands, navigation,
   scriptable read/send/wait operations.
2. A wing-owned control endpoint indexing every egg, replacing client-side
   directory discovery without changing the per-egg failure boundary.
3. Agent meta-access: typed local MCP tools, durable prompt runs, bounded loops,
   dependency-aware swarms, and eventually named/versioned prompt assets.
4. Agent awareness: explicit `working`, `blocked`, `done`, `idle`, and `unknown`
   events, preferring agent hooks over screen scraping.
5. Collaboration primitives: viewer/controller separation, a visible control
   lease, takeover, attribution, and grants independent of organizations.
6. Transport convergence: run the same client protocol over local sockets,
   SSH stdio, direct links, P2P, and optional relay.
7. Local/shared deployment cleanup: capability-driven clients and a clearly
   named shared-roost appliance mode, without making it the personal topology.

## Promotion gate after the window

Nothing moves toward `main` or production merely because it works locally.
Promotion requires an explicit post-vacation decision plus:

- review of CLI and metadata compatibility
- migration behavior for eggs started by older binaries
- full `make check` and cross-platform sandbox tests
- native terminal lifecycle and reconnect end-to-end tests
- collaboration authorization and audit review for any shared-session changes
- a staged deployment plan, with Slide separate from public hosted services

No tag or deployment is part of this branch's development workflow.
