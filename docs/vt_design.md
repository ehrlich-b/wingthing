# Virtual Terminal Emulator (VTE) — "Pseudo-Pseudo Terminal"

## Context

Wingthing currently streams raw PTY bytes through a 2MB bounded replay buffer (`internal/egg/server.go`). On reconnect, the entire buffer is replayed to xterm.js in the browser. This works but requires dirty hacks:

- `findSafeCut()` searches for sync-frame boundaries to avoid mid-sequence trims
- `trackCursorPos()` parses CSI sequences to re-inject cursor position after trim
- `agentPreamble()` re-injects mode sequences (hide cursor, bracketed paste, sync updates)
- The 2MB replay still doesn't capture true screen state — it's a raw byte tail, not a snapshot

The goal: **put a userspace terminal emulator between the PTY and the network**, exactly like tmux sits between the PTY and your terminal. On reconnect, paint the current screen state (50-80 lines) instead of replaying 2MB of historical bytes. The wing becomes "tailscale + tmux on the web."

## Prior Art & Competitive Position

**Nobody is doing this combination.** The landscape breaks into tools that solve one or two pieces:

| Tool | Remote Access | Session Persistence | Web Terminal | VTE in Middle |
|------|:---:|:---:|:---:|:---:|
| tmux/screen | - | ✓ (VTE) | - | ✓ |
| tmate | SSH | ✓ (tmux fork) | read-only | ✓ |
| Mosh | UDP | ✓ (SSP protocol) | - | ✓ |
| sshx | ✓ | - | ✓ | - |
| ttyd/GoTTY | - | - | ✓ | - |
| Codespaces | ✓ | ✓ | ✓ | - (container) |
| E2B | API only | ✓ | - | - |
| **Wingthing (now)** | ✓ | replay buffer | ✓ | - |
| **Wingthing (goal)** | ✓ | ✓ (VTE snapshot) | ✓ | ✓ |

**Mosh's SSP** is the closest prior art — it maintains terminal state on both ends and syncs diffs over UDP. But it's CLI-only, no web. Wingthing would be Mosh's architecture delivered to a browser with E2E encryption and agent sandboxing.

Other notable gaps:
- **tmate** has the VTE (it's a tmux fork) but web access is read-only
- **sshx** has web + E2E encryption but sessions are ephemeral (no VTE state)
- **Coder** has remote + web + persistence but via heavy container infra, not a VTE
- **Eternal Terminal** has reconnect persistence but CLI-only, no VTE snapshot model
- **E2B** has sandboxed agents but API-only, no human-accessible web terminal

## Library Choice: `charmbracelet/x/vt`

After evaluating all Go VTE libraries, **`charmbracelet/x/vt`** is the clear choice:

| Library | Grid State | Full VT | Thread-Safe | Serialization | Maintained | Stars |
|---------|:---:|:---:|:---:|:---:|:---:|:---:|
| **charmbracelet/x/vt** | ✓ | ✓ | ✓ `SafeEmulator` | ✓ `Render()` | ✓ (Feb 2026) | 266 |
| rcarmo/go-te | ✓ | ✓ | - | ✓ `Display()` | ✓ (Feb 2026) | 2 |
| cliofy/govte | ✓ | ✓ | - | partial | ✓ (2025) | 7 |
| hinshun/vt10x | ✓ | ✓ | ✓ | - | stale (2023) | 46 |
| go-vte variants | parser only | ✓ | - | - | stale | <1 |

**Why charmbracelet/x/vt wins:**

1. **`SafeEmulator`** — thread-safe wrapper with mutex, exactly what we need for concurrent gRPC reads + PTY writes
2. **`Render()`** — serializes current screen state as ANSI-coded string. This IS the snapshot.
3. **`Touched()`** — returns dirty lines since last call. Enables future diff-based streaming.
4. **`Resize(w, h)`** — handles terminal resize with content reflow
5. **Callbacks** — `Bell`, `Title`, `AltScreen`, `CursorPosition`, `CursorVisibility` etc. We can hook bell detection directly instead of scanning for `\x07`
6. **Full VT parser** — CSI, OSC, DCS, APC, PM, SOS. Handles vim, htop, anything.
7. **Charm ecosystem** — 32 contributors, 1372 commits, actively maintained. Same team as bubbletea/lipgloss.
8. **Custom handlers** — `RegisterCsiHandler`, `RegisterOscHandler` etc. for agent-specific extensions.

**Key API for our use case:**
```go
emu := vt.NewSafeEmulator(cols, rows)   // create
emu.Write(ptyData)                       // feed PTY output
snapshot := emu.Render()                 // serialize screen → ANSI string
emu.Resize(newCols, newRows)            // terminal resize
cell := emu.CellAt(x, y)               // per-cell access
pos := emu.CursorPosition()            // cursor state
```

**No built-in scrollback** — we'll add a thin scrollback ring buffer that captures lines as they scroll off the top of the emulator grid.

## Architecture

```
                        EGG (per-session process)
┌─────────────┐     ┌──────────────────────────────┐     ┌──────────┐
│ Agent child  │────▶│ PTY master ──▶ VTE ──▶ raw   │────▶│ Wing     │
│ (claude,etc) │◀────│             ◀── input ◀──    │◀────│ WebSocket│
└─────────────┘     │                               │     └──────────┘
                    │  SafeEmulator: rows × cols    │         │
                    │  Scrollback: ring buffer       │         │
                    │  Cursor, attrs, modes, title   │         │
                    └──────────────────────────────┘         │
                                                              ▼
                                                        ┌──────────┐
                                                        │ Browser  │
                                                        │ xterm.js │
                                                        └──────────┘
```

**The VTE lives in the egg process**, right after the PTY read. It replaces the current `replayBuffer` as the source of truth.

### Data flow (connected — zero change to hot path)

1. Agent writes to PTY slave
2. Egg reads PTY master (`readPTY`)
3. **VTE processes bytes** — `emu.Write(data)` updates grid, cursor, attributes
4. Raw bytes forwarded through to wing → relay → browser (pass-through, no added latency)
5. xterm.js renders as before

### Data flow (reconnect — the big win)

1. Browser sends `pty.attach`
2. Wing calls egg's `Attach` gRPC
3. **Egg calls `vterm.Snapshot()`** → scrollback ring buffer lines + `\x1b[2J\x1b[H` + `emu.Render()` (current grid)
4. Egg begins buffering raw PTY bytes into a temp catch-up buffer
5. Snapshot (~5-50KB for screen + scrollback) sent to browser
6. Browser signals snapshot consumed (or egg uses a reasonable delay)
7. Egg flushes catch-up buffer (raw bytes produced since step 3)
8. Live raw pass-through resumes

The catch-up buffer handles the transition window: the agent keeps producing output while the browser is consuming the snapshot. Without it, bytes generated during snapshot transfer would be lost. The buffer is small in practice — agent output during ~100ms of snapshot transfer.

### What the VTE replaces

| Current | VTE-based |
|---------|-----------|
| `replayBuffer` (2MB ring of raw bytes) | `SafeEmulator` screen + scrollback ring |
| `findSafeCut()` (heuristic trim) | Not needed — VTE always has clean state |
| `trackCursorPos()` (regex cursor parse) | `emu.CursorPosition()` |
| `agentPreamble()` (mode re-injection) | VTE tracks all mode state via callbacks |
| `buildTrimPreamble()` | `emu.Render()` emits correct full state |
| BEL scanning in wing.go | `Bell` callback on emulator |

**Why each hack is fundamentally broken (and why the VTE fixes it):**

- **`trackCursorPos()`** — hand-rolled CSI parser that only catches absolute `CUP` sequences (`\x1b[row;colH`). Misses relative moves (up/down/forward/back), carriage returns, newlines, tabs, backspaces — all of which move the cursor. Works "well enough" because Claude uses absolute positioning, but is fundamentally incomplete. The VTE processes *every* cursor-moving sequence; `emu.CursorPosition()` is always exact.

- **`agentPreamble()`** — hardcoded `\x1b[?25l\x1b[?2004h\x1b[?2026h` for Claude. Re-injected after trim because trimming loses mode sequences from session start. If Claude changes modes mid-session (shows cursor, disables bracketed paste), the preamble re-injects stale modes. Also agent-specific — every new agent needs its own hardcoded sequence. The VTE tracks all mode state via callbacks; `Render()` emits correct current modes regardless of agent.

- **`findSafeCut()`** — searches up to 64KB forward in the raw byte ring for a "safe" boundary (sync-frame end, erase-line, CRLF) to avoid cutting mid-escape-sequence. If nothing found, cuts at minOffset and hopes for the best. This entire problem class doesn't exist with the VTE — there is no byte buffer to cut. `Render()` always produces complete, valid ANSI output.

- **`buildTrimPreamble()`** — band-aid on top of the other band-aids: glues agent preamble + last known cursor position into bytes prepended after a trim. The VTE's `Snapshot()` is always complete and self-contained.

## Implementation Plan

**This ships as an experimental feature** behind a flag (`--vte` or `WT_VTE=1`), alongside the existing replay buffer. Dual-write mode lets us A/B test and fall back if needed. The replay buffer stays until VTE is proven stable in production.

### Phase 1: Scrollback Wrapper + Integration Scaffold

Since `charmbracelet/x/vt` provides the VTE but not scrollback, we need a thin wrapper.

**New file: `internal/egg/vterm.go`**

```go
// VTerm wraps charmbracelet/x/vt SafeEmulator with scrollback.
type VTerm struct {
    emu        *vt.SafeEmulator
    scrollback []string      // ring buffer of lines that scrolled off top
    sbHead     int           // ring buffer write position
    sbSize     int           // current number of lines in scrollback
    sbCap      int           // max scrollback lines
    mu         sync.Mutex    // protects scrollback (emu is already thread-safe)
    bell       bool          // set by Bell callback
    title      string        // set by Title callback
}

func NewVTerm(cols, rows, scrollbackLines int) *VTerm
func (v *VTerm) Write(p []byte) (int, error)      // feeds emu, captures scroll-offs
func (v *VTerm) Resize(cols, rows int)
func (v *VTerm) Snapshot() []byte                  // scrollback lines + emu.Render()
func (v *VTerm) Bell() bool                        // check and clear bell flag
func (v *VTerm) Title() string
```

**Scrollback capture strategy:** Use the emulator's damage tracking. When `ScrollDamage` is reported (lines scrolling off the top), capture those lines from the grid before they're overwritten. Alternatively, use a simpler approach: on each `Write`, check if `Touched()` includes the top row and capture it before the write advances.

**Snapshot format:** The snapshot sent on reconnect is:
1. Scrollback lines rendered as ANSI text (last N lines, capped at configurable limit like 500 lines)
2. `\x1b[2J\x1b[H` (clear screen, home cursor)
3. `emu.Render()` (current visible screen with all attributes, cursor position, modes)

This is valid VT100 that xterm.js consumes directly — no new protocol, no JSON, no parsing on the browser side.

### Phase 2: Wire into Egg Session

**Modify: `internal/egg/server.go`**

- Add `vterm *VTerm` field to `Session` struct
- In `RunSession()`: create `NewVTerm(cols, rows, 10000)` alongside the existing replay buffer
- In `readPTY()`: after reading from ptmx, call `sess.vterm.Write(data)` in addition to `sess.replay.Write(data)`
- Dual-write mode: both VTerm and replay buffer get every byte. This lets us A/B test.
- Add `--vte` flag (or env var) to switch `Snapshot()` between replay buffer and VTerm

**Modify: `Session` gRPC handler** (same file, ~line 890+):
- When `Attach=true` received and VTE mode is on:
  - Call `sess.vterm.Snapshot()` instead of `sess.replay.Snapshot()`
  - Send snapshot as single message (no chunking needed — it's 5-50KB vs 2MB)
  - Register cursor at end for live updates (same as before)

**Modify: resize handler:**
- Call `sess.vterm.Resize(cols, rows)` alongside PTY resize

### Phase 3: Remove Replay Buffer

Once VTE is proven stable:

- Remove `replayBuffer` struct and all its methods (`Write`, `ReadAfter`, `Snapshot`, `Register`, `Unregister`)
- Remove `findSafeCut()`, `trackCursorPos()`, `agentPreamble()`, `buildTrimPreamble()`
- Remove cursor-based reader machinery and backpressure logic
- Simplify the gRPC `Session` handler — snapshot is just `vterm.Snapshot()`, live data is raw pass-through
- The live output path stays the same: raw bytes from PTY → gRPC stream → wing → relay → browser

### Phase 4: ghostty-web Evaluation (Parallel Track)

Evaluate `coder/ghostty-web` as xterm.js replacement in the browser:
- xterm.js-compatible API (designed as drop-in replacement)
- WASM-compiled Ghostty VT parser (same code as native desktop Ghostty)
- GPU-accelerated rendering
- Better VT compliance than xterm.js

This is independent of the server-side VTE work and can happen in parallel.

## Mental Model: Three Layers of Terminal State

Understanding where state lives is critical to this design.

### The VT protocol is a fixed grid with no memory

The VT100/xterm protocol defines one thing: a fixed grid (e.g. 80×24). When a line scrolls off the top, the protocol says it's gone. There is no scroll position, no scrollback buffer, no history in the protocol itself.

### Scrollback is a display-side invention

When you scroll up in ghostty/iTerm/xterm, you're reading from your terminal emulator's *private local memory*. The PTY doesn't know you scrolled. The shell doesn't know. Your terminal emulator intercepted lines as they scrolled off the grid and saved them — this is entirely a client-side feature, not part of the VT protocol.

### Applications maintain their own state independently

TUI apps like Claude Code (built on bubbletea) maintain their own internal model of the full conversation. When Claude Code compacts output, it re-renders a summary from its internal state. When you hit Ctrl+O for detailed mode, Claude Code re-renders the *full conversation* fresh through the PTY — that content isn't coming from terminal scrollback, it's being transmitted right now from the application's memory.

### What this means for wingthing

On reconnect, the browser's scrollback is gone (tab closed, different device). The VTE in the egg only has the current grid. So the VTerm wrapper must do what a local terminal emulator does: catch lines as they scroll off the grid top and save them in a ring buffer. `Snapshot()` = saved scrollback + current grid. On reconnect, xterm.js receives this and re-populates both its scrollback and visible screen.

The VTerm's scrollback ring buffer (default 10,000 lines) gives the reconnecting user the same scroll-up experience they'd have if connected locally — bounded by the ring buffer size, not by a 2MB raw byte heuristic.

### Error recovery comparison

The fundamental difference between raw-byte replay and VTE snapshot isn't about which one renders more correctly on the happy path — it's about failure modes:

- **Raw byte replay failures are cumulative and unrecoverable.** A bad trim, missed bytes, or mid-sequence cut leaves xterm.js in a broken parse state. The only fix is "replay more bytes and hope."
- **VTE snapshot errors are correctable on the next frame.** If the VTE and xterm.js disagree on an edge case, the worst case is one frame of slightly wrong reconnect. The next raw byte from the live stream corrects it, because xterm.js is still parsing the live feed independently.

## Key Design Decisions

### Why `charmbracelet/x/vt` (not custom VTE)?
- Full xterm compliance out of the box — handles vim, htop, everything, not just Claude
- Actively maintained by Charm (bubbletea/lipgloss team)
- Thread-safe `SafeEmulator` — no custom locking needed for the grid
- `Render()` gives us serialization for free — no custom serialize code
- Damage tracking (`Touched()`, `ScrollDamage`) enables future diff-based streaming

### Why VTE in the egg (not the wing)?
- The egg is the session owner — it already reads the PTY
- VTE state is per-session, eggs are per-session processes
- Wing is a multiplexer over many sessions — shouldn't hold per-session terminal state
- Keeps the wing's hot path simple (just forward encrypted bytes)

### Why still pass through raw bytes to connected clients?
- Zero added latency for the live path
- xterm.js already parses VT sequences — no point double-parsing for live data
- The VTE is only consulted for reconnect snapshots
- This is exactly how tmux works: passthrough when attached, snapshot on reattach

### Why no delta encoding for v1?

The live path has no deltas today — and doesn't need them. The VT protocol is a raw byte stream processed in order. xterm.js receives raw bytes, parses them, updates its grid, paints pixels. That's how every terminal works. No terminal does delta encoding on the wire.

Delta encoding (Mosh-style: diff the grid every 16ms, send only changed cells/lines) is a future optimization for lossy/high-latency connections. It requires `Touched()` to identify dirty lines, per-line rendering via `CellAt(x, y)`, and a custom browser-side protocol (xterm.js can't consume diffs — it expects a byte stream). This is a Phase 5+ project, not needed for the core VTE win.

For v1: raw pass-through live, snapshot on reconnect. That eliminates all the replay buffer hacks and gives instant reconnect.

### Why include scrollback in the snapshot (not on-demand fetch)?
- Simpler protocol — no new message type needed
- Snapshot = scrollback lines + screen state, all as ANSI text
- 500 lines of scrollback ~ 40-80KB compressed — well within WebSocket frame size
- On-demand fetch can be added later if scrollback gets large

## Files Modified

| File | Change |
|------|--------|
| `internal/egg/vterm.go` | **NEW** — VTerm wrapper (SafeEmulator + scrollback + Snapshot) |
| `internal/egg/vterm_test.go` | **NEW** — round-trip tests, scrollback tests |
| `internal/egg/server.go` | Add VTerm to Session, dual-write in readPTY, VTE-mode Attach |
| `go.mod` | Add `github.com/charmbracelet/x` dependency |

Phase 3 (cleanup) also touches `internal/egg/server.go` to remove the old replay buffer code.

No changes to wing, relay, browser, or WebSocket protocol. The snapshot is just bytes — the existing gRPC and WebSocket plumbing carries it unchanged.

## Verification

1. **Unit tests** (`vterm_test.go`):
   - Write known ANSI sequences → verify `Render()` output reproduces them
   - Write scrollback-inducing output → verify scrollback capture
   - `Snapshot()` round-trip: feed snapshot to a fresh VTE → compare grid state
   - Resize during active session → verify grid reflow

2. **Integration test with real output**:
   - Capture actual Claude Code PTY output (from audit recordings)
   - Feed to VTerm → call Snapshot() → feed snapshot to xterm.js-headless → compare

3. **Manual test**:
   - `make check` (tests + build)
   - `make serve` + connect wing + start egg session
   - Run Claude, interact for a while, disconnect browser tab
   - Reconnect — verify instant accurate repaint
   - Compare visual result against the old replay-buffer behavior

4. **Regression**: Measure live typing latency before/after — should be identical (pass-through path unchanged)

5. **Memory**: Long-running session → verify VTerm memory stays bounded (grid + scrollback cap)

## Sequence of Work

1. `go get github.com/charmbracelet/x` — add dependency
2. `internal/egg/vterm.go` — VTerm wrapper with scrollback + Snapshot()
3. `internal/egg/vterm_test.go` — unit tests
4. Wire into `internal/egg/server.go` — dual-write mode alongside replay buffer
5. Add flag to switch Attach to VTE snapshots
6. Test manually with real sessions
7. Once stable: remove replay buffer code (Phase 3)
