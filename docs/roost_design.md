# Roost: Combined Relay + Wing Mode

## Terminology

Currently "serve", "relay", and "roost" are all synonyms for the same thing — the server process (`wt serve`). This doc proposes giving each term a distinct meaning going forward:

| Term | Meaning |
|------|---------|
| **relay** | The server: HTTP/WebSocket relay, skill registry, SQLite DB. `wt serve` is its command. "Relay" becomes the canonical name for this component. |
| **wing** | A client machine connected to a relay. `wt wing start` / `wt start`. Unchanged. |
| **roost** | A relay and a wing running together in one process. The self-hosted all-in-one mode. New concept. |

"Roost" = a relay that has a local wing living in it. You are both the server and a client of yourself.

## Problem

Self-hosters today must run two processes and keep them coordinated:

```
wt serve           # terminal 1 (or systemd unit 1)
wt start --local   # terminal 2 (or systemd unit 2)
```

This is unnecessary complexity when the relay and the wing are on the same machine. It also:
- Requires two service units
- Produces two log streams
- Means `wt start --local` can fail with "not logged in" if `wt serve` hasn't written the local token yet (race condition)
- Creates a confusing `--local` flag on both `wt serve` and `wt start`

## Solution: `wt roost`

A single command that starts the relay and a local wing in the same process, sharing a lifecycle.

```
wt roost
```

That's it. One command, one process, one log stream. Open `http://localhost:8080`.

### Flags

```
wt roost [--addr :8080] [--labels gpu,cuda] [--paths ~/repos,~/work]
         [--egg-config ~/.wingthing/egg.yaml] [--audit] [--debug]
         [--dev]
```

Relay flags (`--addr`, `--dev`) configure the relay side. Wing flags (`--labels`, `--paths`, `--egg-config`, `--audit`, `--debug`) configure the local wing. No `--local` needed — roost mode is always local by definition.

### Daemon mode

Like `wt wing start`, `wt roost` daemonizes by default and writes a pidfile. `wt roost stop` kills it. `--foreground` for debugging/systemd.

```
wt roost              # daemonize, write ~/.wingthing/roost.pid
wt roost stop         # kill daemon
wt roost status       # check if running
wt roost --foreground # run in foreground (for systemd ExecStart=)
```

## What Changes

### New command: `wt roost`

- Starts the relay server (same as `wt serve --local`)
- After the relay is listening, starts the wing in a goroutine
- Both share a single `signal.NotifyContext` — one signal kills both cleanly
- No `wt login` required — roost mode writes the local token as part of relay init, same as `wt serve --local` does today
- No wing.pid file (wing is not a separate daemon)

### Signal handling refactor

Currently `runWingForeground` installs its own signal handler via `signal.Notify`. In roost mode the relay server also has a `signal.NotifyContext`. Two consumers on the same signal = one handler wins randomly.

Fix: extract a `runWingWithContext(ctx context.Context, cancel context.CancelFunc, ...)` that omits signal setup entirely. The caller (roost or standalone wing) owns the signal context. The SIGHUP reload path stays in the wing loop but uses the passed-in cancel rather than its own signal channel.

### `wt serve` stays unchanged

`wt serve` remains the cloud/multi-node deployment command. It does not gain a `--wing` flag. The use cases are distinct:
- `wt serve` = relay only, used on Fly / multi-machine / headless servers where you don't want a local wing
- `wt roost` = self-hosted all-in-one, used on a developer's machine or a home server

### Deprecate `--local` on `wt start` / `wt wing start`

`wt start --local` becomes unnecessary when `wt roost` exists. Keep it for backwards compat but the help text points to `wt roost` instead.

## Implementation Sketch

```go
func roostCmd() *cobra.Command {
    // flags: addr, dev, labels, paths, egg-config, audit, debug, foreground
    // ...

    // Foreground path:
    //   1. ctx, cancel := signal.NotifyContext(SIGTERM, SIGINT)
    //   2. start relay server goroutine (same as serveCmd, --local implied)
    //   3. wait for relay ready (small poll or channel from relay)
    //   4. go runWingWithContext(ctx, cancel, localOpts...)
    //   5. <-ctx.Done() + graceful shutdown
}
```

The relay already writes the local device token in `--local` mode. The wing goroutine starts after the token is written, so no race condition.

### SIGHUP in roost mode

SIGHUP on the roost process reloads wing config only (labels, paths, egg config, audit, debug). Relay config is not hot-reloadable today anyway.

## Ergonomics Summary

| Scenario | Command |
|----------|---------|
| Self-hosted, developer machine | `wt roost` |
| Self-hosted, systemd unit | `ExecStart=/usr/local/bin/wt roost --foreground` |
| Cloud relay (Fly, multi-node) | `wt serve` |
| Separate wing on a remote machine | `wt start` (connects to a remote relay) |
| Local dev: relay only, no wing | `wt serve` |

## Open Questions

1. **`wt roost` vs `wt serve --wing`**: The new command approach is cleaner and matches the vocabulary. The flag approach avoids adding a command. Decision: new command, `wt serve` stays pure relay.

2. **Relay-side naming**: Should the relay server's internal references (`RoostURL`, `cfg.RoostURL`, `--roost` flag on `wt wing start`) be renamed? The flag `--relay` is more accurate for "where does this wing connect". Renaming is a config-breaking change — do it in a batch migration with a deprecation period.

3. **P2P interaction**: The p2p design (`docs/p2p_design.md`) describes wings connecting directly. A roost node is just a wing with an embedded relay — p2p peers see it as a normal wing. No special casing needed.

4. **Multi-wing on one roost**: A roost is still a relay, so other machines can connect their wings to it. `wt start --relay http://your-machine:8080`. This is the self-hosted household/team server use case.
