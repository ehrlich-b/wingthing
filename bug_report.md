# Bug Report: E2E Decrypt Errors on Long-Running PTY Sessions

## Symptoms

1. **"decrypt error, dropping frame: OperationError"** — repeated in browser console on every new PTY output event
2. **"The script has an unsupported MIME type ('text/html')"** — JS bundle served as HTML
3. New output IS visible (some frames decrypt), but decrypt errors fire alongside every successful frame
4. Happens on long-running sessions accessed via prod (wingthing.fly.dev)

## Root Cause: Duplicate PTY Goroutines After Wing Reconnect

When the wing-relay WebSocket drops and reconnects (common with Fly load balancing, deploys, or network hiccups), `reclaimEggSessions` creates **duplicate goroutines** for every active PTY session.

### The Race

1. Wing connects. `handlePTYSession` goroutine starts for session S, subscribes to egg output, encrypts with key K1.
2. Wing-relay WS drops (Fly hiccup, deploy, etc).
3. Wing auto-reconnects. `OnReconnect` → `reclaimEggSessions` runs.
4. `reclaimEggSessions` dials the egg again, creates a NEW subscriber, calls `RegisterPTYSession` which **overwrites** `ptySessions[S]` with a new input channel, then starts a NEW `handleReclaimedPTY` goroutine (gcm=nil, no key yet).
5. The egg's `readPTY` broadcasts output to **both** subscribers (old and new).
6. The OLD goroutine still has a valid `write` function — `writeJSON` grabs `c.conn` dynamically, so it writes to the NEW WebSocket. Its context is the long-lived parent context from `Client.Run`, not the per-connection context, so it never gets cancelled.
7. OLD goroutine: encrypts every frame with K1, sends to relay. NEW goroutine: drops (gcm=nil).

At this point, the browser still has K1, so everything looks fine. But:

8. Browser reconnects (page reload, tab restore, or session switch). `switchToSession` → `detachPTY` (clears `ephemeralPrivKey`) → `attachPTY` (generates new ephemeral key K2).
9. Relay forwards `pty.attach` to wing. It reaches the NEW goroutine's input channel (because `RegisterPTYSession` overwrote the map).
10. NEW goroutine derives K2, starts encrypting and sending.
11. OLD goroutine is still alive, still encrypting every frame with K1, still writing to the relay.
12. **Browser has K2. K2 frames decrypt fine (visible output). K1 frames fail → "decrypt error, dropping frame" on every output event.**

### Why It Persists

The old goroutine never exits:
- Its egg stream is still alive (egg broadcasts to all subscribers)
- Its `write` function works (captures the long-lived ctx, reads c.conn dynamically)
- It doesn't check write errors (just `continue` on failure)
- Nothing cancels it — no per-session context, no cleanup on reconnect

### Key Files

- `cmd/wt/wing.go:940` — `reclaimEggSessions`: creates duplicate goroutine + overwrites input channel
- `internal/ws/client.go:396` — `RegisterPTYSession`: overwrites `ptySessions[sessionID]` without checking/cancelling old entry
- `internal/ws/client.go:413` — `writeJSON`: reads `c.conn` dynamically (old goroutine uses new WS)
- `internal/egg/server.go:421-428` — `readPTY`: broadcasts to ALL subscribers (`sess.subs`)

### Fix Options

**Option A (simplest): Skip reclaim if session already tracked**
```go
// In reclaimEggSessions, before RegisterPTYSession:
c.ptySessionsMu.Lock()
_, alreadyTracked := c.ptySessions[sessionID]
c.ptySessionsMu.Unlock()
if alreadyTracked {
    log.Printf("egg: session %s already tracked, skipping reclaim", sessionID)
    ec.Close()
    continue
}
```
The old goroutine keeps running, its input channel stays in the map, pty.attach reaches it, re-key works.

**Option B (robust): Per-session cancel context**
Store a `context.CancelFunc` per session. On reconnect, cancel old goroutines before creating new ones. Ensures clean lifecycle.

**Option C (belt-and-suspenders): Both A and B**
Skip if tracked (fast path) + cancel context (safety net for edge cases like stale goroutines after egg crash).

---

## Secondary Bug: MIME Type Error on app.wingthing.ai

### Cause

In `internal/relay/server.go:149-157`, the `app.wingthing.ai` host handler serves `index.html` for ALL paths that don't match `/api/`, `/auth/`, `/ws/`, or `/app/`. This includes static asset paths like `/assets/index-CnCZcQ4D.js`.

When the browser loads the SPA HTML from `/`, it references Vite-hashed bundles at paths like `/assets/index-CnCZcQ4D.js`. These requests hit the SPA fallback and get `index.html` (Content-Type: text/html) instead of the JS bundle.

### Fix

Add an asset path check before the SPA fallback:
```go
// In ServeHTTP, app.wingthing.ai block:
if strings.HasPrefix(path, "/assets/") {
    s.mux.ServeHTTP(w, r) // serve from embedded dist/
    return
}
```
Or register `/assets/` alongside `/app/` in `registerStaticRoutes`.

---

## Reproduction Steps

1. Start `wt wing` connecting to a relay
2. Open a PTY session from the web UI
3. Force the wing-relay WebSocket to drop (e.g., restart relay, or wait for Fly to cycle)
4. Wing auto-reconnects, reclaims sessions
5. Switch away from and back to the session in the browser (or reload page)
6. Observe: new output visible but "decrypt error, dropping frame" on every output event
