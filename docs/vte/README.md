# VTE Integration — Design Overview

Server-side Virtual Terminal Emulator using a fork of `charmbracelet/x/vt`.
Replaces the 2MB replay buffer hacks with proper terminal state.

See also: `docs/vt_design.md` (the original design document with rationale).

## Key Dependencies

- **Fork of `charmbracelet/x/vt`** — adds `ScrollOut` and `ScrollbackClear`
  callbacks for scrollback capture. Plan to upstream once proven.
- `pty.attach` protocol change — adds `cols`/`rows` fields so the egg can
  resize the VTE before generating the snapshot.

## Phase Map

| Phase | Scope | Risk | Behavior Change |
|-------|-------|------|----------------|
| **1: VTerm Core** | Fork vt (add scrollback callbacks). VTerm wrapper with scrollback ring. Dual-write in egg. | Low — pure addition, no behavior change | None — VTerm runs silently alongside replay buffer |
| **2: Transport** | `pty.attach` carries cols/rows. Resize VTE before snapshot. Attach handler sends VTE snapshot. | Medium — changes reconnect path | Reconnect uses VTE snapshot instead of raw replay |
| **3: Cleanup** | Remove replay buffer hacks, simplify live delivery | Low (VTE proven by now) | Remove findSafeCut, trackCursorPos, etc. |

## What Stays The Same Throughout

- **Live output path**: raw PTY bytes → gRPC → wing → WebSocket → browser → xterm.js (pass-through, zero overhead)
- **Audit recording**: raw PTY bytes in V2 varint gzip format (unchanged)
- **Wire protocol**: same `pty.output` messages, same encryption, same chunking
- **Browser code**: no changes (VTE snapshot is valid ANSI that xterm.js consumes)
- **Relay**: no changes (still a dumb encrypted forwarder)

## The Reattach Model: Nuke and Repaint

On reattach, the browser does `S.term.reset()` (already does this today) which
destroys all xterm.js state — grid AND scrollback. Then the snapshot bytes
rebuild everything from scratch. There is no incremental sync. The VTE is the
single source of truth. Same model as tmux reattach.

No special client needed. The snapshot is valid ANSI bytes.

### Snapshot byte format

```
Section 1: Scrollback lines (oldest-first from ring buffer)
───────────────────────────────────────────────────────────
[rendered line 1]\r\n
[rendered line 2]\r\n
...
[rendered line N]\r\n

Section 2: Flush padding
────────────────────────
\n × (rows - 1)    ← push remaining visible content into xterm.js scrollback

Section 3: Grid repaint
───────────────────────
\x1b[H              ← home cursor
[Render()]          ← full grid with styles/attrs, row by row

Section 4: Cursor restore
─────────────────────────
\x1b[row;colH       ← restore cursor position
\x1b[?25h or ?25l   ← restore cursor visibility
```

The flush padding works because `pty.attach` now carries the browser's
`cols`/`rows`. The egg resizes the VTE to match before generating the
snapshot, so `rows-1` is exactly the right padding count. Non-VTE sessions
(legacy) fall back to the existing replay buffer behavior.

## Clears and Scrollback

| Sequence | What it does | VTerm scrollback action |
|----------|-------------|------------------------|
| `ESC[2J` | Erase display (grid only) | Nothing — grid content was already captured when it scrolled off |
| `ESC[3J` | Erase scrollback | **Clear our ring buffer** — user explicitly wants history gone |
| `ESC c` | Full reset (RIS) | **Clear our ring buffer** — nuclear reset |
| `ESC[?1049h` | Alt screen on | **Stop capturing** — alt screen content is ephemeral (vim, htop) |
| `ESC[?1049l` | Alt screen off | **Resume capturing** |

The forked vt library provides `ScrollbackClear` callback for ESC[3J and RIS.
VTerm tracks alt-screen state via the existing `AltScreen` callback and
suppresses `ScrollOut` captures during alt screen.

## Dimension Handling

`pty.attach` now includes `cols` and `rows`. On reattach:

1. Egg receives attach with browser dimensions
2. VTE resized to browser dimensions (grid reflows)
3. PTY resized to match (agent gets SIGWINCH)
4. Snapshot generated at correct dimensions
5. Padding uses correct row count
6. `Render()` output fits the browser exactly

Non-VTE sessions (no VTerm on the egg) ignore the new fields and use the
existing replay buffer behavior.

## Design Decisions Log

| Decision | Rationale |
|----------|-----------|
| VTE in egg, not wing | Egg is per-session, owns the PTY. Wing is multiplexer. |
| Fork charmbracelet/x/vt | Need ScrollOut callback for scrollback. ~20 lines of real logic. Plan to upstream. |
| Scrollback in Phase 1 | ScrollOut callback makes it trivial. No reason to defer. |
| `pty.attach` carries cols/rows | Enables resize-before-snapshot. Solves padding and grid dimension mismatch. |
| Keep replay buffer in Phase 2 | Replay cursor mechanism is the live data path. Replace in Phase 3. |
| writeMu for snapshot coordination | Prevents snapshot/cursor desync. Simple, low contention. |
| No browser changes | VTE snapshot is valid ANSI. Same wire format. `pty.attach` already has extensible JSON. |
| Scrollback lines stored as rendered strings | Compact. Width-locked but that's expected (same as real terminals). |
| Don't capture scrollback during alt screen | Alt screen is ephemeral (vim, htop, less). Not conversation history. |

## File Index

- `phase1-vterm-core.md` — Phase 1 detailed design (VTerm + scrollback + dual-write)
- `phase2-transport-integration.md` — Phase 2 detailed design (attach handler + dimensions)
- `../vt_design.md` — Original design document (rationale, library evaluation, architecture)
