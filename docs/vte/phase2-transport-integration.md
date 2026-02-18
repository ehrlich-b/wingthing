# Phase 2: Transport Layer Integration

## Goal

Switch the reconnect/attach path from replay-buffer snapshot to VTE snapshot.
Add cols/rows to `pty.attach` so the VTE resizes to the browser's dimensions
before generating the snapshot. Non-VTE sessions fall back to existing behavior.

## Prerequisites

Phase 1 complete: VTerm wrapper with scrollback exists, dual-write is active,
unit tests pass, integration tests against audit recordings pass.

## What Changes

| Component | Change |
|-----------|--------|
| `internal/egg/server.go` | Attach handler uses `vterm.Snapshot()`, accepts resize-before-snapshot |
| `internal/ws/protocol.go` | `PTYAttach` gains `Cols`/`Rows` fields |
| `cmd/wt/wing.go` | On reattach: resize egg before attach if cols/rows provided |
| `web/src/pty.js` | Include cols/rows in `pty.attach` message |
| Browser (pty.js) | **No rendering changes** — snapshot is valid ANSI, same `flushReplay` path |
| Relay | **None** — still forwarding encrypted blobs |

## Protocol Change: `pty.attach`

### Current

```json
{
  "type": "pty.attach",
  "session_id": "abc123",
  "public_key": "base64..."
}
```

### New

```json
{
  "type": "pty.attach",
  "session_id": "abc123",
  "public_key": "base64...",
  "cols": 80,
  "rows": 24
}
```

`cols` and `rows` are optional. If zero/absent, the egg skips the
resize-before-snapshot step and falls back to existing behavior (replay buffer
snapshot at current dimensions). This provides backwards compatibility with
older wing/browser versions.

## The Reattach Sequence

```
Browser: pty.attach { session_id, public_key, cols: 80, rows: 24 }
  → Relay: forwards to wing (encrypted)
  → Wing:
      1. Derive new E2E key from browser's pubkey
      2. Send pty.started (wing pubkey) so browser can derive key
      3. If cols/rows provided AND egg has VTerm:
           Send resize to egg (cols=80, rows=24)
      4. ec.AttachSession(sessionID)
        → Egg gRPC:
            a. sess.writeMu.Lock()
            b. If VTerm exists:
                 vteSnap := sess.vterm.Snapshot()    // at 80×24 now
                 replayPos := sess.replay.WritePosition()
               Else:
                 vteSnap, replayPos = sess.replay.Snapshot()
            c. sess.writeMu.Unlock()
            d. stream.Send(SessionMsg{Output: vteSnap})
            e. cursor := sess.replay.Register(replayPos)
            f. Live output loop: ReadAfter(cursor) → stream.Send
      5. Wing receives snapshot, sends to browser via sendReplayChunked
      6. Wing starts live output goroutine
  → Browser:
      1. Receives pty.started, derives E2E key
      2. Receives pty.output chunks (snapshot)
      3. flushReplay(): S.term.reset() → S.term.write(combined)
      4. Snapshot paints scrollback + grid at correct 80×24 dimensions
      5. Live output resumes
```

## Coordination: writeMu

The snapshot and replay cursor position must correspond to the same point
in the PTY stream. A new `writeMu sync.Mutex` on Session gates both the
dual-write in readPTY and the snapshot+position read in the attach handler:

```go
// readPTY hot path:
sess.writeMu.Lock()
sess.vterm.Write(data)
sess.replay.Write(data)
sess.writeMu.Unlock()

// Attach handler:
sess.writeMu.Lock()
vteSnap := sess.vterm.Snapshot()
replayPos := sess.replay.WritePosition()
sess.writeMu.Unlock()
```

## Fallback Behavior

Non-VTE sessions (sess.vterm == nil):
- Ignore cols/rows on attach
- Use existing `sess.replay.Snapshot()` for reconnect
- Existing sendReplayChunked handles 2MB replay as before
- All current hacks (findSafeCut, trackCursorPos, etc.) still active

This means we can ship Phase 2 incrementally — VTE sessions get the new
behavior, non-VTE sessions are completely unchanged.

## Browser Change (pty.js)

Minimal. In the `pty.attach` message construction, add the terminal dimensions:

```javascript
S.ptyWs.send(JSON.stringify({
    type: 'pty.attach',
    session_id: S.ptySessionId,
    public_key: pubKeyB64,
    cols: S.term.cols,
    rows: S.term.rows
}));
```

No rendering changes. The `flushReplay()` path is identical — it receives
encrypted pty.output chunks, decrypts, combines, resets terminal, writes.
Whether the bytes are raw replay or VTE snapshot is transparent to the browser.

## Wing-Side Changes

### Reattach handler (pty.attach case)

Add resize-before-attach when cols/rows are provided:

```go
case ws.TypePTYAttach:
    var attach ws.PTYAttach
    json.Unmarshal(data, &attach)

    // ... existing key derivation ...

    // NEW: resize egg before reattach if dimensions provided
    if attach.Cols > 0 && attach.Rows > 0 {
        ec.Resize(attach.Cols, attach.Rows)
    }

    // existing: ec.AttachSession, sendReplayChunked, etc.
```

### Replay chunking

VTE snapshot is 5-80KB vs 2MB replay. sendReplayChunked handles small
payloads correctly (single iteration). No changes needed.

## Files Modified

| File | Change |
|------|--------|
| `internal/egg/server.go` | writeMu, VTE-mode attach, WritePosition method |
| `internal/ws/protocol.go` | PTYAttach gains Cols/Rows fields |
| `cmd/wt/wing.go` | Resize-before-attach on reattach |
| `web/src/pty.js` | Include cols/rows in pty.attach message |

## What Comes After Phase 2

### Phase 3: Cleanup
- Remove `findSafeCut()`, `trackCursorPos()`, `agentPreamble()`, `buildTrimPreamble()`
- Simplify replay buffer (no more trimming logic — only needs bounded byte
  ring for live cursor reads)
- Or replace replay buffer entirely with simpler broadcast mechanism
- Remove VTE flag-gating, make it always-on

## Risks

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| VTE/xterm.js rendering mismatch | Medium | Low (self-heals on next frame) | Audit recording integration tests |
| Snapshot/cursor desync | Low | High (missing or duplicate output) | writeMu coordination |
| Resize-before-snapshot race | Low | Medium (grid reflows during snapshot) | writeMu serializes resize+snapshot |
| charmbracelet/x/vt crash on edge case | Low | High (egg crash) | recover() wrapper, fall back to replay |
