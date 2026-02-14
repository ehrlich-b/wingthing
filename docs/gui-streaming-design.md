# GUI Streaming Design: H.264 Over WebSocket

## Core Principle

The relay is a dumb pipe. GUI streaming adds a new payload type alongside PTY bytes — encoded video frames flowing through the same WebSocket, same E2E encryption, same relay routing. The relay never decodes a frame.

```
Today:  egg PTY → text chunks → ws → relay → ws → browser xterm.js
GUI:    egg capture → H.264 frames → ws → relay → ws → browser <video>
```

## Why This Matters

Terminal relay is competing with tmux + tailscale. GUI streaming creates a new category: secure remote access to ANY agent, visual or terminal, from anywhere. Watch Claude Code, Codex, Cursor, VS Code — sandboxed on your hardware, streamed to your browser with E2E encryption.

The combination of sandbox + GUI streaming + relay doesn't exist anywhere else.

## Architecture

```
EGG SESSION                           RELAY              BROWSER
┌─────────────────────────┐
│ Agent (VS Code/Codex)   │
│         │                │
│    ┌────▼─────┐          │
│    │ Capture  │          │         ┌─────────┐     ┌──────────────┐
│    │ (native) │          │         │  Relay   │     │  MSE +       │
│    └────┬─────┘          │         │  (dumb   │     │  <video>     │
│         │ raw frames     │         │   pipe)  │     │              │
│    ┌────▼─────┐          │  ws     │         ws     │  ┌────────┐  │
│    │ Encoder  ├──────────┼────────▶├────────────────▶│  │VideoTag│  │
│    │ (H.264)  │ fMP4     │gui.frame│         │     │  └────────┘  │
│    └──────────┘ segments │         │         │     │              │
│                          │         │◀────────┼─────│  gui.input   │
│    ┌──────────┐◀─────────┼─────────┤gui.input│     │  (kb/mouse)  │
│    │ Input    │          │         │         │     │              │
│    │ Injector │          │         └─────────┘     └──────────────┘
│    └──────────┘          │
└─────────────────────────┘
```

### Data Flow

1. **Capture** — platform-native API grabs the agent's window pixels (no chrome)
2. **Encode** — H.264 encoder (hardware on macOS, software on Linux) produces NAL units
3. **Mux** — Go fMP4 muxer wraps NAL units into fragmented MP4 segments
4. **Transport** — fMP4 segments travel as `gui.frame` messages over existing WebSocket
5. **Decrypt** — browser decrypts with session E2E key (same as PTY frames)
6. **Playback** — Media Source Extensions feed fMP4 into a `<video>` element, hardware-decoded

### What Changes

| Component | Change |
|-----------|--------|
| Relay | Nothing. New message types pass through as opaque payloads. |
| Wing WebSocket | New `gui.*` message handlers alongside `pty.*` handlers. |
| Egg session | Parallel capture pipeline alongside PTY (or instead of, for GUI-only agents). |
| Browser | New `<video>` + canvas player alongside xterm.js terminal. |
| E2E encryption | Same AES-GCM wrapping. Identical to PTY frame encryption. |

## Capture Layer

Platform-specific, behind a common Go interface:

```go
// internal/gui/capture.go
type Frame struct {
    Width, Height int
    Stride        int
    Pixels        []byte // BGRA (X11) or NV12 (ScreenCaptureKit)
    Timestamp     int64  // nanoseconds, monotonic
}

type Capturer interface {
    Start(windowID uint64, fps int) error
    Frames() <-chan Frame
    Stop()
}
```

### macOS — ScreenCaptureKit + VideoToolbox (Phase 1)

ScreenCaptureKit is Apple's compositor-level capture API (macOS 12.3+). It captures specific windows without chrome, delivers frames in encoder-native formats, and runs entirely on the GPU when paired with VideoToolbox.

**cgo bridge** (~150 lines ObjC in `capture_darwin.h`):

- `SCShareableContent.getWithCompletionHandler` — enumerate windows
- `SCContentFilter` targeting a specific `SCWindow` — no title bar, no shadow
- `SCStreamConfiguration`:
  - Pixel format: `kCVPixelFormatType_420YpCbCr8BiPlanarFullRange` (NV12)
  - This is VideoToolbox's native input format — zero conversion, zero copy
  - Configure width/height to match window, or downscale for bandwidth
  - `minimumFrameInterval` = 1/fps (e.g. 1/10 for 10 fps)
- `SCStream` delivers `CMSampleBuffer` frames via delegate callback
- Extract `CVPixelBuffer`, pass pointer to Go via cgo callback

**Permissions:** Requires Screen Recording entitlement. macOS shows a one-time system prompt (same as OBS, Zoom, Discord). No ongoing permission dialog.

**Zero-copy pipeline:** ScreenCaptureKit delivers NV12 `CVPixelBuffer` on GPU → VideoToolbox encodes directly from GPU buffer → H.264 NAL units out. The pixels never touch main memory in the common case.

### Linux — X11 via XShm (Phase 4)

X11 shared-memory capture is the practical choice for Linux. It covers VS Code, Codex, Electron apps, and anything running under XWayland.

**cgo bridge** (~100 lines C in `capture_linux.h`):

- `XShmCreateImage` — allocate shared-memory capture buffer
- `XCompositeRedirectWindow` — capture window even when occluded/minimized
- `XShmGetImage` — grab window contents into shared memory (fast, no socket copy)
- Returns BGRA pixel buffer
- Convert BGRA → NV12 via libyuv (SIMD-accelerated) or simple C loop before encoding

**Headless Linux:** For servers and containers, run `Xvfb` (virtual framebuffer) inside the egg namespace. VS Code, Codex, and all Electron apps work on Xvfb. This is standard practice for CI/GUI testing.

```yaml
# egg.yaml
display: xvfb    # launch Xvfb inside egg, set DISPLAY
```

### Wayland — The Hard Path (Phase 5+)

Wayland's security model explicitly prevents the kind of programmatic window access that X11 allows. There is no universal "give me that window's pixels" API. The landscape in 2026:

#### PipeWire + xdg-desktop-portal (Universal, Interactive)

The only approach that works across all major Wayland compositors (GNOME/Mutter, KDE/KWin, Sway, Hyprland, COSMIC).

**How it works:**
1. D-Bus request to `org.freedesktop.portal.ScreenCast`
2. User selects window via system picker dialog (REQUIRED — no programmatic selection)
3. Compositor provides a PipeWire stream of the captured content
4. Consume frames from PipeWire via `libpipewire` (C library, cgo-able)

**The problem:** The user interaction requirement. Every capture session needs a human to click a picker dialog and select the window. This is by design — Wayland's security model won't budge on this.

**Mitigation for wingthing:** Since the user initiates the egg session (they click "start" in the web UI or run `wt egg`), the picker dialog appears once at session start. Not ideal, but workable. The dialog runs on the Linux desktop where the egg is running — the remote user doesn't see it.

**For headless/server Linux:** PipeWire isn't relevant — there's no compositor. Use Xvfb (above).

**cgo integration:**
- `godbus/dbus` for the D-Bus portal calls (pure Go, no cgo needed)
- `libpipewire` for stream consumption (cgo, `pkg-config: libpipewire-0.3`)
- Frames arrive as DMA-BUF fds or shared memory — feed to encoder

#### ext-image-copy-capture-v1 (Modern, Limited Coverage)

The standardized Wayland protocol for screen/window capture. Merged to staging in August 2024. Supports capturing individual windows ("toplevels") without user interaction.

**Compositor support (2026):**

| Compositor | Support |
|-----------|---------|
| Sway | Yes |
| Labwc | Yes |
| Wayfire | Yes |
| COSMIC | Yes |
| Hyprland | Planned |
| GNOME (Mutter) | Unknown |
| KDE (KWin) | Unknown |

This is the future, but GNOME and KDE support is unclear. Not viable as the sole Wayland strategy today.

**Potential use:** For wlroots-based compositors (Sway, Labwc, Wayfire), this could provide non-interactive capture. Implement as an optional backend behind the Capturer interface, auto-detected at runtime.

#### XWayland Escape Hatch

VS Code and all Electron apps run under XWayland on Wayland desktops. In theory, you could use X11 capture (XShmGetImage) on the XWayland window.

**Reality:** This does NOT work reliably. X11 capture APIs can only see other XWayland windows, not native Wayland windows. Security isolation between XWayland and native Wayland prevents cross-protocol access. KDE has an "XWayland Video Bridge" workaround, but it's fragile and compositor-specific.

**Not recommended** as a capture strategy.

#### Wayland Strategy Summary

| Environment | Strategy | User interaction? |
|-------------|----------|-------------------|
| Headless server/container | Xvfb + X11 capture | No |
| Sway/wlroots desktop | ext-image-copy-capture-v1 | No |
| GNOME/KDE desktop | PipeWire portal | Yes (once per session) |
| Any Wayland + Electron app | Xvfb fallback inside egg | No |

The pragmatic v1 path:
1. **macOS:** ScreenCaptureKit (primary target, Bryan's daily driver)
2. **Linux headless:** Xvfb + X11 capture (servers, containers, CI)
3. **Linux desktop Wayland:** PipeWire portal (universal but interactive)
4. **Linux desktop wlroots:** ext-image-copy-capture-v1 (bonus, non-interactive)

For sandboxed eggs on Linux, Xvfb is almost always the right answer. The egg runs inside a namespace with its own display server — no dependency on the host compositor.

## Encoding Layer

### macOS — VideoToolbox (cgo, zero external deps)

Apple's hardware encoder framework. Present on every Mac since 2012. No external libraries, no licensing concerns — it's a system framework.

```go
// internal/gui/encode_darwin.go
//go:build darwin

/*
#cgo LDFLAGS: -framework VideoToolbox -framework CoreMedia -framework CoreFoundation
#include "encode_darwin.h"
*/
import "C"
```

**cgo bridge** (~200 lines C in `encode_darwin.h`):

- `VTCompressionSessionCreate` with `kCMVideoCodecType_H264`
- Properties:
  - `kVTCompressionPropertyKey_RealTime = true`
  - `kVTCompressionPropertyKey_ProfileLevel = kVTProfileLevel_H264_High_AutoLevel`
  - `kVTCompressionPropertyKey_AverageBitRate` — target bitrate
  - `kVTCompressionPropertyKey_MaxKeyFrameInterval` — IDR interval in frames
  - `kVTCompressionPropertyKey_AllowFrameReordering = false` — no B-frames, reduces latency
- Feed `CVPixelBuffer` from ScreenCaptureKit directly — zero copy
- Callback delivers `CMSampleBuffer` containing H.264 NAL units
- Extract NALs from `CMBlockBuffer`, copy to Go `[]byte`

**Performance:** Hardware-accelerated, near-zero CPU. The Apple Silicon media engine handles encoding entirely off the main CPU cores.

### Linux — OpenH264 via cgo (statically linked)

Cisco's BSD-licensed H.264 encoder. MIT-compatible. Lower quality than x264 but legally clean for distribution.

```go
// internal/gui/encode_linux.go
//go:build linux

/*
#cgo pkg-config: openh264
#cgo LDFLAGS: -lopenh264 -lm -lpthread -lstdc++
#include <wels/codec_api.h>
*/
import "C"
```

**cgo bridge** (~150 lines C in `encode_linux.h`):

- `WelsCreateSVCEncoder(&encoder)`
- `SEncParamExt` config: `iUsageType = SCREEN_CONTENT_REAL_TIME` (optimized for code editors)
- `iRCMode = RC_BITRATE_MODE`, target ~800 Kbps (compensate for lower efficiency vs x264)
- `iSpatialLayerNum = 1`, single layer, no SVC complexity
- `uiIntraPeriod = fps * 2` — keyframe every 2 seconds
- `encoder->InitializeExt(&param)`
- `encoder->EncodeFrame(&pic, &info)` — feed NV12, get NAL units from layer info
- Extract NALs from `SFrameBSInfo.sLayerInfo`, return as `[]byte`

**Performance:** OpenH264's `SCREEN_CONTENT_REAL_TIME` mode is designed for exactly this use case — sharp text, low motion. Expect ~10% of one core at 1080p 10fps.

**Install:** `apt-get install libopenh264-dev`. For static builds: compile from source (BSD, no restrictions).

### Licensing

Wingthing is MIT-licensed. x264 (GPL-2.0) cannot be statically linked without relicensing.

| Platform | Encoder | License | Decision |
|----------|---------|---------|----------|
| macOS | VideoToolbox | Apple framework | **Use this.** Zero CPU, zero deps, no license concern. |
| Linux | OpenH264 | BSD-2-Clause | **Use this.** Cisco pays MPEG-LA. MIT-compatible. |

OpenH264 is 20-30% lower quality than x264 at the same bitrate. For code editors (sharp text, mostly static frames), compensate by targeting a slightly higher bitrate (~800 Kbps vs ~500 Kbps). Bandwidth cost difference is negligible at $5/month pricing.

### Encoder Interface

```go
// internal/gui/encode.go
type NALUnit struct {
    Data    []byte
    IsIDR   bool   // keyframe
    HasSPS  bool   // contains SPS/PPS (for init segment)
}

type Encoder interface {
    Init(width, height, fps, bitrate int) error
    Encode(frame Frame) ([]NALUnit, error)
    Close()
}
```

## fMP4 Muxing (Pure Go)

H.264 NAL units wrapped in fragmented MP4 for browser MSE playback. Pure Go, no cgo.

```go
// internal/gui/mux.go
type FragmentedMuxer struct {
    seqNum   uint32
    timebase uint32 // typically 90000 (standard for H.264)
}

// Init produces ftyp + moov boxes (send once on stream start)
func (m *FragmentedMuxer) Init(width, height int, sps, pps []byte) []byte

// Fragment produces moof + mdat boxes (send per frame or frame batch)
func (m *FragmentedMuxer) Fragment(nalus []NALUnit, durationTicks uint32) []byte
```

The init segment contains codec parameters (SPS/PPS) extracted from the first IDR frame. Each subsequent fragment is a `moof` (timing metadata) + `mdat` (encoded data) pair. Overhead: ~50-80 bytes per fragment.

**Keyframe strategy:** One IDR every 2 seconds. On reattach, send init segment + most recent IDR fragment + subsequent P-frame fragments. Mirrors the PTY replay buffer concept.

## Transport

### New Message Types

```go
// internal/ws/protocol.go additions

// Wing → Browser (via relay)
"gui.init"    // { session_id, width, height, init_segment (base64 fMP4 init) }
"gui.frame"   // { session_id, data (base64 fMP4 fragment), keyframe: bool }
"gui.resize"  // { session_id, width, height, init_segment }
"gui.stopped" // { session_id }

// Browser → Wing (via relay)
"gui.attach"  // { session_id, public_key }
"gui.input"   // { session_id, type: "key"|"mouse", event: {...} }
"gui.quality" // { session_id, max_fps, max_bitrate }
```

### Binary Frames

Current protocol uses JSON with base64-encoded payloads. For video, base64 adds 33% overhead. Two options:

**Option A: Stay with JSON + base64.** Simpler. At 500 Kbps video, base64 adds ~165 Kbps. Total ~665 Kbps. Probably fine.

**Option B: Binary WebSocket frames.** Small header (1 byte type + 16 byte session ID + 4 byte flags) followed by raw fMP4 data. Relay forwards without parsing. Saves bandwidth, adds protocol complexity.

Start with Option A. Migrate to B if bandwidth becomes a concern.

### Relay Changes

None. The relay already forwards unknown message types as opaque payloads. New `gui.*` messages flow through the existing `forwardToWing` / `forwardToBrowser` paths. E2E encryption is applied by the endpoints, relay sees ciphertext.

### Replay Buffer

```go
// internal/gui/replay.go
type GUIReplayBuffer struct {
    initSegment  []byte     // fMP4 init (ftyp + moov)
    lastIDR      []byte     // most recent keyframe fragment
    pFrames      [][]byte   // P-frames since last IDR
    maxSize      int        // cap total buffer size (default 2MB)
}
```

On reattach: send `initSegment` + `lastIDR` + all `pFrames`. Browser gets a full picture within one IDR interval (2 seconds of data max).

## Browser Playback

### Media Source Extensions (MSE)

MSE feeds fMP4 segments into a `<video>` element. Hardware-accelerated decode on every browser. Universal support (Chrome, Firefox, Safari, Edge).

```javascript
// web/src/gui-player.js
const mediaSource = new MediaSource();
const video = document.createElement('video');
video.src = URL.createObjectURL(mediaSource);
video.autoplay = true;
video.muted = true; // autoplay requires muted

mediaSource.addEventListener('sourceopen', () => {
    const sb = mediaSource.addSourceBuffer('video/mp4; codecs="avc1.640028"');

    // Handle gui.init — append init segment
    // Handle gui.frame — decrypt, append fragment
    // Handle buffer overflow — remove old data when buffer exceeds 30s
});
```

**Latency target:** With `zerolatency` encoder tune + immediate fragment dispatch + MSE buffering, expect 150-300ms glass-to-glass. Acceptable for watching an agent work.

**For lower latency (v2):** Switch to WebCodecs `VideoDecoder` API. Skips MSE's internal buffer, decodes directly to `<canvas>`. Target: <100ms. Useful for interactive remote control (typing in VS Code). WebCodecs has broad support in 2026 (Chrome, Edge, Safari; Firefox partial).

### Adaptive Quality

Browser sends `gui.quality` messages based on connection quality:

```javascript
// Measure decode-to-render time and buffer fullness
if (sourceBuffer.buffered.length > 0) {
    const buffered = sourceBuffer.buffered.end(0) - video.currentTime;
    if (buffered > 1.0) {
        // Falling behind — request lower quality
        send({ type: 'gui.quality', max_fps: 5, max_bitrate: 200000 });
    }
}
```

Wing adjusts encoder parameters in real-time. CRF-based encoding handles this naturally — lower fps means fewer frames, each with better quality.

## Input Forwarding

For remote control (not just viewing). Small JSON messages, same WebSocket.

```go
// internal/gui/input.go
type KeyEvent struct {
    Type      string // "keydown", "keyup"
    Key       string // "a", "Enter", "Meta"
    Code      string // "KeyA", "Enter", "MetaLeft"
    Modifiers uint8  // bitmask: shift|ctrl|alt|meta
}

type MouseEvent struct {
    Type   string  // "mousemove", "mousedown", "mouseup", "scroll"
    X, Y   float64 // normalized 0.0-1.0 (resolution independent)
    Button int
    DeltaX float64 // scroll
    DeltaY float64
}
```

### macOS Input Injection

```c
// CGEventPost via cgo — inject keyboard/mouse events at HID level
CGEventRef event = CGEventCreateKeyboardEvent(NULL, keycode, isDown);
CGEventSetFlags(event, modifierFlags);
CGEventPost(kCGHIDEventTap, event);
```

Requires Accessibility permission (one-time system prompt, same as Karabiner, BetterTouchTool).

### Linux Input Injection

```c
// XTest extension via cgo
XTestFakeKeyEvent(display, keycode, isDown, CurrentTime);
XTestFakeMotionEvent(display, screen, x, y, CurrentTime);
XTestFakeButtonEvent(display, button, isDown, CurrentTime);
```

XTest works on both real X11 and Xvfb. No special permissions needed.

## Egg Integration

### Session Lifecycle

The egg already manages a child process with PTY. GUI sessions add a parallel capture pipeline:

```go
// internal/egg/session.go additions
type Session struct {
    // existing
    pty       *os.File
    process   *os.Process
    replay    *ReplayBuffer

    // new for GUI
    capturer  gui.Capturer
    encoder   gui.Encoder
    muxer     *gui.FragmentedMuxer
    guiReplay *gui.GUIReplayBuffer
}
```

On session start with GUI agent:

1. Start the agent process (as today)
2. Wait for the agent's window to appear (poll window list by PID, timeout 10s)
3. Start capturer targeting that window
4. Initialize encoder + muxer
5. Stream fragments to subscribers (same fan-out as PTY chunks)

### Egg Config

```yaml
# egg.yaml
display: auto    # "auto" | "none" | "xvfb" | "native"
gui:
  fps: 10        # capture framerate (default 10)
  bitrate: 500k  # target bitrate (default 500 Kbps)
  keyframe: 2s   # IDR interval (default 2 seconds)
```

- `auto`: detect if the agent needs a GUI (cursor → yes, claude → no)
- `none`: terminal only (current behavior, default)
- `xvfb`: launch Xvfb inside the egg (Linux headless)
- `native`: capture the real display (macOS, or Linux with desktop)

### Sandbox Considerations

The capture helper runs inside the egg's sandbox. On macOS, ScreenCaptureKit requires:
- Screen Recording permission (system-level, applies to the `wt` binary)
- No additional seatbelt holes needed — ScreenCaptureKit talks to WindowServer via Mach IPC, which seatbelt allows by default

On Linux with Xvfb inside the egg namespace:
- Xvfb runs as a child process in the same namespace
- X11 capture connects to the local Xvfb display
- No host display access needed — fully isolated

## Bandwidth Analysis

For a 1920x1080 code editor:

| Scenario | FPS | Bitrate | Notes |
|----------|-----|---------|-------|
| Idle (cursor blink) | 2 | 20-50 Kbps | P-frames nearly empty |
| Agent typing code | 10 | 200-500 Kbps | Small regions changing |
| Fast scrolling | 15 | 1-3 Mbps | Brief spike (1-2 seconds) |
| File tree navigation | 10 | 300-800 Kbps | Moderate change |
| **Weighted average** | — | **200-600 Kbps** | — |

Compare to PTY relay: 40-400 Kbps. GUI streaming is roughly 5-10x terminal bandwidth. Well within WebSocket capacity. No relay scaling changes needed.

### Relay Impact

At 500 Kbps per GUI session, a relay serving 100 concurrent GUI streams needs ~50 Mbps throughput. A $5/month VPS handles this. The relay does zero encoding/decoding — just byte forwarding.

## Build

```makefile
# macOS: zero external deps (Apple frameworks only)
build-darwin:
	CGO_ENABLED=1 go build ./cmd/wt

# Linux: requires libopenh264-dev, libx11-dev, libxext-dev, libxtst-dev
build-linux:
	CGO_ENABLED=1 go build ./cmd/wt
```

**macOS:** Zero external dependencies. ScreenCaptureKit, VideoToolbox, CoreGraphics are system frameworks. The cgo directives (`#cgo LDFLAGS: -framework ...`) handle everything. Build-tagged via `//go:build darwin`.

**Linux:** `apt-get install libopenh264-dev libx11-dev libxext-dev libxtst-dev`. For static release builds, compile OpenH264 from source (BSD, no restrictions). Build-tagged via `//go:build linux`.

**Cross-compilation:** Not practical with cgo platform frameworks. Build on each target (already the case for sandbox code).

## Implementation Phases

### Phase 1: macOS Capture + Encode PoC (1-2 weeks)

- ScreenCaptureKit cgo bridge — capture a window, deliver NV12 frames to Go
- VideoToolbox cgo bridge — encode frames to H.264 NAL units
- Write encoded output to file, verify with `ffplay`
- Validate: quality, bitrate, CPU usage, latency from capture to encoded NAL
- No network, no muxing, no browser — just the native pipeline

### Phase 2: fMP4 Mux + Browser Playback (1 week)

- Go fMP4 muxer — wrap NALs into fragmented MP4
- Static HTML page with MSE playback
- Local WebSocket server — capture → encode → mux → ws → browser
- Validate: end-to-end visual quality, latency, browser compatibility

### Phase 3: Integrate into Egg + Wing (1-2 weeks)

- Add `gui.*` message types to `internal/ws/protocol.go`
- Wire capture pipeline into egg session lifecycle
- GUI replay buffer for reattach
- Forward `gui.*` through relay (should work with no relay changes)
- E2E encryption for GUI frames (same as PTY)
- Browser player component in `web/` alongside xterm.js
- `gui.input` forwarding (keyboard/mouse) browser → wing → egg → window

### Phase 4: Linux Support (1 week)

- x264 cgo encoder
- X11 XShm capture
- Xvfb integration for headless eggs (spawn Xvfb in egg namespace)
- NV12 color conversion (BGRA from X11 → NV12 for x264)
- Test with VS Code on Linux

### Phase 5: Wayland Support (2+ weeks)

- PipeWire + xdg-desktop-portal capture backend (universal Wayland)
- ext-image-copy-capture-v1 backend (wlroots compositors, optional)
- Auto-detect: Wayland session → try ext-image-copy first → fall back to PipeWire portal
- Headless Linux always uses Xvfb (no Wayland dependency)

### Phase 6: Polish (ongoing)

- Adaptive quality (browser sends `gui.quality` based on connection)
- WebCodecs decoder for lower latency (v2 browser player)
- Dual-mode sessions: PTY + GUI side by side (terminal output + visual window)
- AV1 encoder option (royalty-free, better compression, slower encode)

## Known Limitations

### ScreenCaptureKit permission (macOS)

First use requires Screen Recording permission approval via system dialog. This is a one-time grant per app binary. If `wt` is installed via brew or downloaded, the user sees the dialog once. Subsequent sessions use the cached permission.

### No per-domain network filtering with GUI streaming

GUI agents (Cursor, VS Code with extensions) make their own network calls. The sandbox allows outbound HTTPS for the agent (existing limitation documented in egg-sandbox-design.md). GUI streaming doesn't change this — same agent, same network profile.

### Wayland picker dialog

On GNOME/KDE Wayland, PipeWire portal requires user to select the capture target via a compositor dialog. This runs on the machine where the egg is running. For remote-only use (no local desktop), use Xvfb instead.

### H.264 text rendering

H.264 is optimized for natural video, not sharp text. At low bitrates, code text can show compression artifacts. Mitigation: target higher CRF quality (CRF 20-23), use 4:4:4 chroma subsampling if encoder supports it (VideoToolbox does, x264 does with `--output-csp i444`). For code editors, quality matters more than bitrate savings.

### Input injection permissions (macOS)

Remote keyboard/mouse injection via CGEventPost requires Accessibility permission. Separate from Screen Recording — another one-time system dialog. Without it, view-only mode works fine; input forwarding is disabled.

## Future Considerations

### AV1

Royalty-free, patent-free, better compression than H.264. SVT-AV1 is cgo-compilable. Browser support is universal in 2026. The tradeoff is encode latency — SVT-AV1 preset 12 (fastest) is comparable to x264 `veryfast`. For code editors with mostly static frames, AV1's screen content coding tools are actually ideal. Worth evaluating for v2 or as a build flag option.

### WebRTC

Hardware H.264 + native browser decode with built-in congestion control. But: requires STUN/TURN infrastructure, ICE negotiation, NAT traversal complexity. Doesn't fit the dumb-pipe relay model — the relay would need to become a TURN server. Not recommended unless the relay architecture changes.

### Dual PTY + GUI

Some agents (Claude Code in VS Code) have both a terminal and a GUI. The egg could stream both simultaneously — PTY as `pty.output`, GUI as `gui.frame`. Browser shows a split view or tab toggle. The session has both replay buffers. This falls out naturally from the architecture.
