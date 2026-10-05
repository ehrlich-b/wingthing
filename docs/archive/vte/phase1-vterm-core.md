# Phase 1: VTerm Core + Scrollback + Egg Integration

## Goal

Build the VTerm wrapper (with scrollback via forked vt library), wire it into
the egg in dual-write mode, and prove it produces correct snapshots with
scrollback — all without touching the transport layer, wing, relay, or browser.
Phase 1 is pure addition. Zero risk to existing behavior.

## What Ships

1. Forked `charmbracelet/x/vt` with `ScrollOut` and `ScrollbackClear` callbacks
2. `internal/egg/vterm.go` — VTerm wrapper (SafeEmulator + scrollback ring + Snapshot)
3. `internal/egg/vterm_test.go` — unit tests
4. Dual-write in `internal/egg/server.go` — VTerm gets every byte alongside replay buffer
5. `go.mod` gains forked vt dependency
6. Audit recordings remain identical (raw PTY stream, V2 varint format)

## What Does NOT Change

- No wire protocol changes (gRPC, WebSocket, or tunnel)
- No wing.go changes
- No relay changes
- No browser changes
- The replay buffer stays as the attach/reconnect source of truth
- Audit recording stays raw PTY bytes (not VTE snapshots)

## Forked charmbracelet/x/vt

The fork adds three things to the library:

### 1. `ScrollOut` callback

Fires when lines scroll off the top of the scroll region, BEFORE they are
destroyed. Provides the line data (as `[]Line`) so the caller can capture
them for scrollback.

Patched into `Screen.DeleteLine()` — the single funnel point for all scroll-off
operations (`index()` on linefeed, CSI `S` scroll up, CSI `M` delete line at
top of scroll region).

### 2. `ScrollbackClear` callback

Fires when the terminal requests scrollback erasure:
- `ESC[3J` (Erase Scrollback) — currently incorrectly falls through to
  case 2 (Erase Display) in the upstream code
- `ESC c` (RIS / Full Reset)

The fork also splits the `'J'` handler's case 2 and case 3, which are
incorrectly merged with a `fallthrough` in upstream.

### 3. No other changes

The fork is minimal. ~20 lines of logic across 3 files (callbacks.go,
screen.go, handlers.go, esc.go). All existing behavior preserved. Designed
to be upstreamed.

## VTerm Wrapper Design

### `internal/egg/vterm.go`

```go
type VTerm struct {
    emu        *vt.SafeEmulator
    scrollback []string    // ring buffer of rendered lines scrolled off top
    sbHead     int         // next write position in ring
    sbLen      int         // current count (≤ sbCap)
    sbCap      int         // max scrollback lines (default 10000)
    mu         sync.Mutex  // protects scrollback ring (emu is already thread-safe)
    altScreen  bool        // true = suppress scrollback capture
    cols, rows int

    // State tracked via callbacks
    bell       bool
    title      string
}
```

### Callbacks Wired in NewVTerm

```go
emu.SetCallbacks(vt.Callbacks{
    ScrollOut: func(lines []uv.Line) {
        if v.altScreen { return }
        v.mu.Lock()
        defer v.mu.Unlock()
        for _, line := range lines {
            v.pushScrollback(line.Render())
        }
    },
    ScrollbackClear: func() {
        v.mu.Lock()
        v.sbLen = 0
        v.sbHead = 0
        v.mu.Unlock()
    },
    AltScreen: func(on bool) {
        v.altScreen = on
    },
    Bell: func() {
        v.bell = true
    },
    Title: func(title string) {
        v.title = title
    },
})
```

### Core Methods

**`NewVTerm(cols, rows, scrollbackCap int) *VTerm`**
- Creates SafeEmulator with cols×rows
- Registers all callbacks above
- Allocates scrollback ring of capacity scrollbackCap

**`Write(p []byte) (int, error)`**
- Calls `emu.Write(p)` — ScrollOut callback fires automatically for scroll-offs
- Returns len(p), nil

**`Resize(cols, rows int)`**
- Calls `emu.Resize(cols, rows)`
- Updates stored dimensions

**`Snapshot() []byte`**
- Assembles reconnect payload:
  1. Scrollback lines (oldest-first from ring), each rendered + `\r\n`
  2. `\n` × (rows - 1) — flush padding to push remaining into scrollback
  3. `\x1b[H` — home cursor
  4. `emu.Render()` — current visible grid
  5. `\x1b[row;colH` — restore cursor position from `emu.CursorPosition()`
  6. `\x1b[?25l` or `\x1b[?25h` — restore cursor visibility
- Result is valid ANSI that any terminal can consume directly

**`Bell() bool`**
- Returns and clears bell flag

**`Title() string`**
- Returns current terminal title

### Scrollback Ring

Standard ring buffer. `pushScrollback(rendered string)` writes at `sbHead`,
wraps at `sbCap`. `scrollbackLines() []string` returns all lines oldest-first
(from `sbHead - sbLen` wrapping around to `sbHead - 1`).

Lines are stored as pre-rendered ANSI strings (from `uv.Line.Render()`). They
are width-locked to whatever the terminal width was when they scrolled off.
This matches real terminal behavior — scrollback doesn't reflow on resize.

## Dual-Write Integration

In `internal/egg/server.go`, Session struct gains:

```go
vterm *VTerm // always created alongside replay buffer
```

In `readPTY()`, after the existing `sess.replay.Write(data)`:

```go
if sess.vterm != nil {
    sess.vterm.Write(data)
}
```

In resize handler:

```go
if sess.vterm != nil {
    sess.vterm.Resize(int(p.Resize.Cols), int(p.Resize.Rows))
}
```

VTerm is created in `RunSession()`:

```go
sess.vterm = NewVTerm(int(cfg.Cols), int(cfg.Rows), 10000)
```

**No changes to Attach/Snapshot path.** The replay buffer stays as the
reconnect source. VTerm silently processes every byte alongside it.

## Verification Plan

### Unit Tests (`vterm_test.go`)

1. **Basic output**: Write "hello\r\n" → Snapshot contains "hello" in scrollback
2. **Scrollback capture**: Write 100 lines to a 10-row terminal → 90 lines in scrollback ring
3. **Scrollback ring wrap**: Write 20,000 lines with cap=10,000 → oldest 10,000 dropped
4. **ANSI colors**: Write colored text → Snapshot preserves SGR attributes
5. **Cursor positioning**: Write CSI H sequences → Snapshot restores cursor at right position
6. **Screen clear (ESC[2J)**: Scrollback preserved, grid cleared
7. **Scrollback clear (ESC[3J)**: Scrollback ring emptied
8. **Full reset (ESC c)**: Scrollback ring emptied, grid reset
9. **Alt screen**: Enter alt screen → scroll events don't capture to scrollback
10. **Resize**: Resize mid-session → Snapshot has new dimensions, old scrollback at old width
11. **Round-trip**: Feed Snapshot to a fresh VTerm → Render() matches original
12. **Multi-line scroll**: Single Write that scrolls 50 lines → all 50 captured

### Integration Test with Audit Recordings

Real audit.pty.gz files from production sessions:
1. Parse V2 varint frames
2. Create VTerm with recorded dimensions
3. Feed each output frame to VTerm.Write(), apply resize frames
4. Call Snapshot() at the end
5. Feed snapshot to a second VTerm → compare Render() output
6. Verify scrollback line count is reasonable

## Files Modified

| File | Change |
|------|--------|
| `internal/egg/vterm.go` | **NEW** — VTerm wrapper |
| `internal/egg/vterm_test.go` | **NEW** — unit + integration tests |
| `internal/egg/server.go` | Add vterm field, dual-write in readPTY, resize |
| `go.mod`, `go.sum` | Add forked charmbracelet/x/vt dependency |

## Sequence of Work

1. Fork charmbracelet/x/vt, add ScrollOut + ScrollbackClear callbacks
2. `go.mod` replace directive pointing to fork
3. Write `internal/egg/vterm.go`
4. Write `internal/egg/vterm_test.go`
5. Wire dual-write into server.go
6. `make check`
7. Manual test: start egg, verify no crashes or performance regression
