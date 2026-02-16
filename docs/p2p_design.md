# WebRTC P2P Data Channels for PTY Sessions

## Context

All PTY traffic currently round-trips through the Fly relay, even when browser and wing are on the same LAN. WebRTC data channels enable direct browser-to-wing communication, eliminating relay latency entirely for same-LAN users. The relay becomes a signaling server and fallback transport.

## Architecture

```
Today:    browser ←WS→ relay ←WS→ wing ←gRPC→ egg
With P2P: browser ←DataChannel→ wing ←gRPC→ egg  (relay only for signaling)
```

- **Signaling** flows through the existing encrypted tunnel (`tunnel.req/res`)
- **Data plane** uses WebRTC data channels — same E2E encryption (AES-GCM), just different transport
- **Control plane** stays on relay WebSocket — `pty.start`, `pty.attach`, `pty.kill`, passkey challenge/response
- One `RTCPeerConnection` per browser↔wing pair, one data channel per PTY session

## ICE Strategy — No STUN/TURN

Host ICE candidates only (local network interfaces). This gives same-LAN P2P. Cross-NAT stays on relay — no degradation from today. The relay knows each peer's public IP from headers; this could be injected as a candidate hint in v2.

## Migration Flow (Stop-the-World)

No seamless handoff. Clean stop → switch → restart. At no point are two writers active.

### Signaling (parallel with normal PTY operation)

1. Browser connects PTY over relay WebSocket (existing flow, unchanged)
2. After `pty.started` + E2E key derived, browser initiates WebRTC in background
3. Browser creates `RTCPeerConnection({ iceServers: [] })`, creates data channel
4. Browser waits for ICE gathering complete, sends final SDP offer (candidates embedded) via `sendTunnelRequest(wingId, {type: "webrtc.offer", sdp, session_id})`
5. Wing creates PeerConnection (pion/webrtc), waits for ICE gathering, returns final SDP answer (candidates embedded) via tunnel response — single round-trip, no trickle
6. Data channel opens. **Sits idle.** No messages flow on it yet.

### Handoff (stop-the-world)

7. Browser sends `pty.migrate` through **relay WebSocket** — this is a disconnect notice
   ```json
   {"type": "pty.migrate", "session_id": "abc123", "auth_token": "..."}
   ```
8. Relay forwards `pty.migrate` to wing (same as pty.input/resize forwarding)
9. Wing receives `pty.migrate` on its input channel. Checks:
   - Does PeerManager have an open DC for this session from matching `senderPub`?
   - **NO** → respond with error on WS ("no webrtc connection"), done
   - If wing is locked: is `auth_token` valid in passkeyCache?
   - If wing is NOT locked: skip token check (identity proven by tunnel signaling)
10. Wing takes **write lock** on SwappableWriter. Under this single lock:
    - Sends `pty.migrated` via relay WS — **this is the LAST WS message for this session** (output goroutine is blocked on the write lock, cannot sneak in a pty.output after this)
    - Swaps writer function pointer to DC write
    - Releases lock
11. Output goroutine unblocks, calls write() — goes to DC. First post-migration byte on DC.

### Browser flush guarantee

12. Browser has **two event streams** — WS `onmessage` and DC `onmessage`. DC messages are **buffered** until migration is confirmed:
    ```javascript
    var dcBuffer = [];
    var migrated = false;

    dc.onmessage = function(e) {
        if (!migrated) { dcBuffer.push(e.data); return; }
        processDCMessage(e.data);
    };

    // In WS onmessage switch:
    case 'pty.migrated':
        migrated = true;
        // All prior WS messages already processed (JS event loop guarantees in-order within a source)
        // No more session messages will arrive on WS (wing sent migrated under lock, then swapped)
        dcBuffer.forEach(processDCMessage);  // flush in order
        dcBuffer = [];
        S.dcActive[msg.session_id] = true;   // input now goes to DC
        break;
    ```
13. After flush: DC `onmessage` processes directly. Input goes to DC. WS stays open for signaling/fallback.

### Why this is safe

- Wing's write lock guarantees `pty.migrated` is the absolute last WS message for this session
- Browser's buffering guarantees no DC byte is consumed before all WS bytes are processed
- JS event loop guarantees in-order delivery within each source (WS and DC separately)
- Zero races, zero duplicates, zero reordering

## Auth Chain

For **locked wings** (passkey required):
1. Relay authenticated user (session/JWT) before PTY WebSocket upgrade
2. Wing verified passkey during `pty.start` → issued auth_token
3. Signaling (SDP/ICE) flows through E2E encrypted tunnel
4. `pty.migrate` includes auth_token → wing validates against `passkeyCache.Check(token, authTTL)`

For **unlocked wings** (no passkey):
1. Relay authenticated user (session/JWT)
2. Signaling went through authenticated tunnel → wing stored `senderPub → userID` from the offer
3. `pty.migrate` arrives on input channel → wing checks: `sessions[session_id].userID == peers[senderPub].userID`
4. No token needed — identity proven by tunnel exchange

Single code path. Auth token validation is additive when `wingCfg.Locked`:
```
if session doesn't exist → reject
if session.userID != peer.userID → reject
if wing.locked && !passkeyCache.Check(auth_token) → reject
→ migrate
```

## Fallback

- **DC dies (browser)**: `dc.onclose` → `S.dcActive[sessionId]` deleted → next input falls back to relay WS. Browser can also trigger `ptyReconnectAttach` through relay (existing battle-tested path).
- **DC dies (wing)**: `dc.OnClose` → `sw.Swap(relayWrite)` → next output goes through relay. When browser reattaches through WS, session resumes normally.
- **PeerConnection fails**: all DCs close → all sessions fall back → no user-visible disruption
- **Wing restart**: PeerManager destroyed → DCs close → browser uses existing relay reconnect logic
- **WebRTC offer fails** (cross-NAT, firewall): stays on relay, no migration attempted, no error shown

## Implementation

### Phase 1: Protocol types

**`internal/ws/protocol.go`** — Add two new message types:
```go
TypePTYMigrate  = "pty.migrate"   // browser → relay → wing
TypePTYMigrated = "pty.migrated"  // wing → relay → browser
```
Add structs:
```go
type PTYMigrate struct {
    Type      string `json:"type"`
    SessionID string `json:"session_id"`
    AuthToken string `json:"auth_token,omitempty"`
}
type PTYMigrated struct {
    Type      string `json:"type"`
    SessionID string `json:"session_id"`
}
```

### Phase 2: Relay forwarding (two one-line changes)

**`internal/relay/pty_relay.go`** line 295 — Add `ws.TypePTYMigrate` to browser→wing forward:
```go
case ws.TypePTYInput, ws.TypePTYResize, ws.TypePTYAttentionAck, ws.TypePasskeyResponse, ws.TypePTYMigrate:
```

**`internal/relay/workers.go`** line 451 — Add `ws.TypePTYMigrated` to wing→browser forward:
```go
case ws.TypePTYStarted, ws.TypePTYOutput, ws.TypePTYExited, ws.TypePasskeyChallenge, ws.TypePTYMigrated:
```

### Phase 3: Go WebRTC package

**NEW `internal/webrtc/peer.go`** — Thin wrapper around pion/webrtc:
- `PeerManager` — manages per-sender peer connections (`map[senderPub]*Peer`)
- `Peer` — wraps `*webrtc.PeerConnection`, tracks `senderPub` and `userID`
- `HandleOffer(senderPub, userID, sdp string) (answerSDP string, error)` — creates PC, waits for ICE gathering, returns final answer SDP
- `GetDC(senderPub, sessionID string) *webrtc.DataChannel` — look up open DC
- `SendToSession(senderPub, sessionID string, data []byte) error` — write to DC
- `Close()` — cleanup all PCs
- ICE config: `{ ICEServers: [] }` — host candidates only

**NEW `internal/webrtc/transport.go`** — Swappable write function:
```go
type SwappableWriter struct {
    mu sync.Mutex
    fn ws.PTYWriteFunc
}

// Write calls the current write function under lock
func (sw *SwappableWriter) Write(v any) error {
    sw.mu.Lock()
    fn := sw.fn
    sw.mu.Unlock()
    return fn(v)
}

// MigrateToDC atomically: sends pty.migrated on WS (last WS message), then swaps to DC.
// Output goroutine is blocked during this — guarantees ordering.
func (sw *SwappableWriter) MigrateToDC(sessionID string, relayWrite, dcWrite ws.PTYWriteFunc) error {
    sw.mu.Lock()
    defer sw.mu.Unlock()
    err := relayWrite(ws.PTYMigrated{Type: ws.TypePTYMigrated, SessionID: sessionID})
    if err != nil {
        return err
    }
    sw.fn = dcWrite
    return nil
}

// FallbackToRelay swaps writer back to relay (on DC close)
func (sw *SwappableWriter) FallbackToRelay(relayWrite ws.PTYWriteFunc) {
    sw.mu.Lock()
    sw.fn = relayWrite
    sw.mu.Unlock()
}
```

**`go.mod`** — Add `github.com/pion/webrtc/v4`

### Phase 4: Wing integration

**`cmd/wt/wing.go`** — Changes:

1. Create `peerMgr` alongside `passkeyCache` (~line 540):
```go
peerMgr := webrtcpkg.NewPeerManager()
defer peerMgr.Close()
```

2. Track swappable writers + relay write functions per session:
```go
var sessionWriters sync.Map  // sessionID → *SwappableWriter
var sessionRelayWrites sync.Map  // sessionID → ws.PTYWriteFunc (original relay write)
```

3. Wrap write function in `OnPTY` callback (line 705):
```go
client.OnPTY = func(ctx context.Context, start ws.PTYStart, write ws.PTYWriteFunc, input <-chan []byte) {
    sw := webrtcpkg.NewSwappableWriter(write)
    sessionWriters.Store(start.SessionID, sw)
    sessionRelayWrites.Store(start.SessionID, write)
    defer sessionWriters.Delete(start.SessionID)
    defer sessionRelayWrites.Delete(start.SessionID)
    handlePTYSession(ctx, cfg, start, sw.Write, input, ...)
}
```

4. Add tunnel inner fields in `tunnelInner` struct (line 2517):
```go
SDP       string `json:"sdp,omitempty"`
```

5. Add case to tunnel dispatch switch (after line 2668):
```go
case "webrtc.offer":
    answer, err := peerMgr.HandleOffer(req.SenderPub, req.SenderUserID, inner.SDP)
    if err != nil {
        tunnelRespond(gcm, req.RequestID, map[string]any{"error": err.Error()}, write)
        break
    }
    tunnelRespond(gcm, req.RequestID, map[string]any{"sdp": answer}, write)
```

6. Handle `pty.migrate` in `handlePTYSession` input loop (alongside TypePTYInput, TypePTYResize, etc.):
```go
case ws.TypePTYMigrate:
    var mig ws.PTYMigrate
    json.Unmarshal(data, &mig)

    dc := peerMgr.GetDC(/* senderPub for this session */, mig.SessionID)
    if dc == nil {
        write(ws.ErrorMsg{Type: ws.TypeError, Message: "no webrtc connection"})
        continue
    }

    // Auth check
    if wingCfg.Locked {
        if _, ok := passkeyCache.Check(mig.AuthToken, authTTL); !ok {
            write(ws.ErrorMsg{Type: ws.TypeError, Message: "unauthorized"})
            continue
        }
    }

    dcWrite := func(v any) error {
        d, _ := json.Marshal(v)
        return dc.Send(d)
    }

    // Atomic: send pty.migrated on WS (last WS message), swap to DC
    sw.MigrateToDC(mig.SessionID, relayWrite, dcWrite)
```

7. Add `PushPTYInput` method to `internal/ws/client.go`:
```go
func (c *Client) PushPTYInput(sessionID string, data []byte) {
    c.ptySessionsMu.Lock()
    ch := c.ptySessions[sessionID]
    c.ptySessionsMu.Unlock()
    if ch != nil {
        select { case ch <- data: default: }
    }
}
```

8. Wire up DC `OnMessage` — when DC receives a message for a session, push to the same input channel:
```go
dc.OnMessage(func(msg webrtc.DataChannelMessage) {
    client.PushPTYInput(sessionID, msg.Data)
})
```

9. Wire up DC `OnClose` — swap writer back to relay:
```go
dc.OnClose(func() {
    if sw, ok := sessionWriters.Load(sessionID); ok {
        if rw, ok := sessionRelayWrites.Load(sessionID); ok {
            sw.(*SwappableWriter).FallbackToRelay(rw.(ws.PTYWriteFunc))
        }
    }
})
```

### Phase 5: Browser WebRTC module

**NEW `web/src/webrtc.js`**:

```javascript
var peers = {};  // wingId → { pc, dc, sessionId, dcBuffer, migrated }

// Called after pty.started + E2E key derived
export function initWebRTC(wingId, sessionId) {
    var pc = new RTCPeerConnection({ iceServers: [] });
    var dc = pc.createDataChannel('pty:' + sessionId, { ordered: true });
    var peer = { pc: pc, dc: dc, sessionId: sessionId, dcBuffer: [], migrated: false };
    peers[wingId] = peer;

    dc.onmessage = function(e) {
        var msg = JSON.parse(e.data);
        if (!peer.migrated) {
            peer.dcBuffer.push(msg);
            return;
        }
        S.dcMessageHandler(msg);
    };

    dc.onopen = function() {
        // DC is ready — tell wing to switch (via relay WS)
        if (S.ptyWs && S.ptyWs.readyState === WebSocket.OPEN) {
            var msg = { type: 'pty.migrate', session_id: sessionId };
            if (S.tunnelAuthTokens[wingId]) msg.auth_token = S.tunnelAuthTokens[wingId];
            S.ptyWs.send(JSON.stringify(msg));
        }
    };

    dc.onclose = function() {
        if (S.dcActive) delete S.dcActive[sessionId];
        delete peers[wingId];
        // Fallback: relay WS is still open, next input goes to WS
    };

    // Wait for ICE gathering complete, then send offer with all candidates embedded
    pc.onicecandidate = function(e) {
        if (e.candidate !== null) return;  // null = gathering complete
        sendTunnelRequest(wingId, {
            type: 'webrtc.offer',
            sdp: pc.localDescription.sdp,
            session_id: sessionId
        }).then(function(resp) {
            if (resp.error || !resp.sdp) {
                console.warn('WebRTC offer rejected:', resp.error);
                cleanupPeer(wingId);
                return;
            }
            pc.setRemoteDescription({ type: 'answer', sdp: resp.sdp });
        }).catch(function(err) {
            console.warn('WebRTC signaling failed:', err);
            cleanupPeer(wingId);
        });
    };

    pc.createOffer().then(function(offer) {
        return pc.setLocalDescription(offer);
    }).catch(function(err) {
        console.warn('WebRTC offer creation failed:', err);
        cleanupPeer(wingId);
    });
}

// Called from pty.js WS handler when pty.migrated received
export function completeMigration(wingId, sessionId) {
    var peer = peers[wingId];
    if (!peer || peer.sessionId !== sessionId) return;
    peer.migrated = true;
    // Flush buffered DC messages (arrived after wing swapped to DC)
    peer.dcBuffer.forEach(function(msg) { S.dcMessageHandler(msg); });
    peer.dcBuffer = [];
    S.dcActive = S.dcActive || {};
    S.dcActive[sessionId] = true;
}

export function sendViaDC(sessionId, msg) {
    if (!S.dcActive || !S.dcActive[sessionId]) return false;
    var wingId = S.ptyWingId;
    var peer = peers[wingId];
    if (!peer || !peer.dc || peer.dc.readyState !== 'open') return false;
    peer.dc.send(JSON.stringify(msg));
    return true;
}

export function cleanupPeer(wingId) {
    var peer = peers[wingId];
    if (!peer) return;
    if (peer.dc) try { peer.dc.close(); } catch(e) {}
    if (peer.pc) try { peer.pc.close(); } catch(e) {}
    if (S.dcActive) delete S.dcActive[peer.sessionId];
    delete peers[wingId];
}
```

### Phase 6: Browser integration

**`web/src/pty.js`** — Changes:

1. After E2E key derived in `pty.started` handler (line 229), initiate WebRTC:
```javascript
import('./webrtc.js').then(function(mod) {
    mod.initWebRTC(S.ptyWingId, S.ptySessionId);
});
```

2. Set up DC message handler:
```javascript
S.dcMessageHandler = function(msg) {
    if (msg.type === 'pty.output') { processOutput(msg.data, !!msg.compressed); }
    else if (msg.type === 'pty.exited') { /* same cleanup as WS handler */ }
    else if (msg.type === 'session.attention') { setNotification(msg.session_id); }
};
```

3. Handle `pty.migrated` in WS onmessage switch:
```javascript
case 'pty.migrated':
    import('./webrtc.js').then(function(mod) {
        mod.completeMigration(S.ptyWingId, msg.session_id);
    });
    break;
```

4. In `detachPTY()` and `disconnectPTY()`, add cleanup:
```javascript
import('./webrtc.js').then(function(mod) { mod.cleanupPeer(S.ptyWingId); });
S.dcActive = {};
```

**`web/src/terminal.js`** — Change `sendPTYInput` (line 245):
```javascript
import { sendViaDC } from './webrtc.js';

export function sendPTYInput(text) {
    if (!S.ptySessionId) return;
    clearNotification(S.ptySessionId);
    e2eEncrypt(text).then(function (encoded) {
        var msg = { type: 'pty.input', session_id: S.ptySessionId, data: encoded };
        if (sendViaDC(S.ptySessionId, msg)) return;
        if (S.ptyWs && S.ptyWs.readyState === WebSocket.OPEN) {
            S.ptyWs.send(JSON.stringify(msg));
        }
    });
}
```

**`web/src/pty.js`** — Change resize handler (line 251):
```javascript
S.term.onResize(function (size) {
    if (!S.ptySessionId) return;
    var msg = { type: 'pty.resize', session_id: S.ptySessionId, cols: size.cols, rows: size.rows };
    if (sendViaDC(S.ptySessionId, msg)) return;
    if (S.ptyWs && S.ptyWs.readyState === WebSocket.OPEN) {
        S.ptyWs.send(JSON.stringify(msg));
    }
});
```

## Files Summary

| File | Action | What |
|------|--------|------|
| `go.mod` | modify | Add `pion/webrtc/v4` |
| `internal/ws/protocol.go` | modify | Add `TypePTYMigrate`, `TypePTYMigrated` + structs |
| `internal/webrtc/peer.go` | new | PeerManager, HandleOffer, GetDC, SendToSession |
| `internal/webrtc/transport.go` | new | SwappableWriter with MigrateToDC / FallbackToRelay |
| `internal/relay/pty_relay.go` | modify | Add `TypePTYMigrate` to browser→wing forward (one-line) |
| `internal/relay/workers.go` | modify | Add `TypePTYMigrated` to wing→browser forward (one-line) |
| `cmd/wt/wing.go` | modify | PeerManager setup, wrap OnPTY write, tunnel webrtc.offer, pty.migrate in input loop |
| `internal/ws/client.go` | modify | Add PushPTYInput method, add TypePTYMigrate to routing |
| `web/src/webrtc.js` | new | RTCPeerConnection, DC management, migration, sendViaDC |
| `web/src/pty.js` | modify | Initiate WebRTC after pty.started, pty.migrated handler, DC message handler, cleanup |
| `web/src/terminal.js` | modify | sendPTYInput prefers DC over relay |

## Data Channel Message Format

Same JSON as relay WebSocket. Same E2E encryption. Messages on DC:

| Type | Direction | Notes |
|------|-----------|-------|
| `pty.output` | wing → browser | Same format as relay (encrypted, optional gzip) |
| `pty.input` | browser → wing | Same format as relay (encrypted) |
| `pty.resize` | browser → wing | Same format as relay (plaintext cols/rows) |
| `pty.exited` | wing → browser | Same format as relay |
| `session.attention` | wing → browser | Bell notification |

Note: `pty.migrate` and `pty.migrated` flow through **relay WS**, not DC.

## Edge Cases

- **Replay on reattach**: stays on relay path (large chunked data, not latency-sensitive). DC only carries live I/O.
- **DC message size limit**: WebRTC SCTP default ~16KB. Live PTY output chunks are typically 1-4KB. If a chunk exceeds limit, fragment or fall back to relay for that message.
- **Multiple sessions, same wing**: v1 = one DC per session per wing. Each migrates independently.
- **Wing restart**: PeerManager destroyed → DCs close → browser uses existing relay reconnect logic.
- **Cross-NAT** (no STUN): WebRTC offer fails → stays on relay, no migration, no error shown. Totally invisible.

## Verification

1. `make check` — tests pass, builds clean
2. `make serve` in terminal A, `./wt stop && ./wt start --debug` in terminal B
3. Open browser to localhost:8080, start a session
4. Browser console: look for "WebRTC: migrated session <id>"
5. Type in terminal, verify I/O works over DC
6. Kill wing process → verify fallback to relay reconnect
7. Restart wing → verify re-migration to WebRTC
8. Test with wing on different machine (same LAN) → verify ICE connects via host candidates
9. Test from different network (no STUN) → verify relay stays active, no errors
