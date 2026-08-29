package egg

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/creack/pty"
	agentpkg "github.com/ehrlich-b/wingthing/internal/agent"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const maxReplaySize = 2 * 1024 * 1024 // 2MB replay buffer — trim at safe cut points

// Terminal escape sequences used as safe cut points for buffer trimming.
var (
	syncEnd   = []byte("\x1b[?2026l")   // end of synchronized update frame (Claude, Codex)
	eraseLine = []byte("\x1b[2K\x1b[G") // erase line + column 1 (Cursor)
)

// Server implements the Egg gRPC service — wraps a SINGLE process.
// Each egg is its own child process with its own socket/PID/token in ~/.wingthing/eggs/<session-id>/.
type Server struct {
	pb.UnimplementedEggServer

	dir        string // ~/.wingthing/eggs/<session-id>/
	token      string
	session    *Session
	mu         sync.RWMutex
	grpcServer *grpc.Server
	listener   net.Listener
	metaMu     sync.Mutex
}

// Session holds a single PTY process and its state.
type Session struct {
	ID             string
	PID            int
	Agent          string
	Kind           string
	Command        []string
	CWD            string
	Network        string // summary: "none", "*", or comma-separated domains
	RenderedConfig string // effective egg config as YAML (after merge/resolve)
	Cols           uint32
	Rows           uint32
	StartedAt      time.Time
	ptmx           *os.File
	replay         *replayBuffer
	vterm          *VTerm        // server-side VTE — only accessed by runVTermLoop goroutine
	vtermCh        chan vtermMsg // async vterm processing channel
	useVTE         bool          // when true, attach sends VTerm snapshot instead of replay buffer
	sb             sandbox.Sandbox
	cmd            *exec.Cmd
	mu             sync.Mutex
	lastOutput     time.Time     // last PTY output timestamp
	lastInput      time.Time     // last user input timestamp
	idleTimeout    time.Duration // 0 = disabled
	done           chan struct{} // closed when process exits
	exitCode       int
	debug          bool
	audit          bool
	auditor        *inputAuditor // nil when audit disabled
	auditWriter    *gzip.Writer  // nil when audit disabled or after PTY exit
	auditFile      *os.File      // underlying file for audit flush
	auditStart     time.Time     // start time for audit timestamps
	auditLastMS    uint64        // last frame timestamp for delta encoding
	auditFrames    int           // frame count since last flush
	auditErr       error         // first recording failure; suppresses repeated writes/logs
	auditMu        sync.Mutex    // protects auditWriter/auditLastMS/auditFrames
}

// RunConfig holds everything needed to start a single egg session.
type RunConfig struct {
	Agent string
	Kind  string
	// Command replaces the agent entirely; AgentArgs extends the agent's own
	// invocation and keeps the session an agent session. They are alternatives.
	Command      []string
	AgentArgs    []string
	CWD          string
	Shell        string
	FS           []string // "rw:./", "deny:~/.ssh"
	Network      []string // domain list
	LocalPorts   []int    // host loopback ports forwarded into the network namespace
	NetworkMode  string   // ""/"enforce" or "observe"
	AgentDomains string   // ""/"merge" or "none"
	Env          map[string]string
	Rows         uint32
	Cols         uint32

	DangerouslySkipPermissions bool
	CPULimit                   time.Duration
	MemLimit                   uint64
	MaxFDs                     uint32
	PidLimit                   uint32
	Debug                      bool
	Audit                      bool
	Trace                      bool          // wrap sandbox command with strace (Linux only)
	VTE                        bool          // use VTerm snapshot for reconnect instead of replay buffer
	RenderedConfig             string        // effective egg config as YAML (after merge/resolve)
	UserHome                   string        // per-user home directory (relay sessions only)
	SkipHostAgentEnv           bool          // shared hosts keep provider credentials in UserHome
	IdleTimeout                time.Duration // 0 = disabled; self-terminate after this much idle
	ResumeSessionID            string        // agent session ID to resume (from chat.meta)
	ToolNames                  []string      // names of privileged tools (for shim generation)
	ToolSocketPath             string        // path to tool.sock (set by wing, empty = no tools)
	OuterBoundary              bool          // explicit trusted-host marker from the parent process
}

// replayBuffer is an append-only (bounded) log of PTY output.
// Readers use cursor-based reads (ReadAfter) instead of subscriber channels,
// guaranteeing every byte arrives in exact PTY order with no drops.
// readerCursor tracks an active reader's position in the buffer.
type readerCursor struct {
	offset int64
}

type replayBuffer struct {
	mu           sync.Mutex
	buf          []byte
	trimmed      int64         // total bytes ever trimmed from front
	written      int64         // total bytes ever written
	notify       chan struct{} // closed+replaced on Write to wake readers
	advanced     chan struct{} // closed+replaced when a reader advances (unblocks writer)
	readers      []*readerCursor
	trimPreamble []byte // mode sequences to re-inject after trim
	cursorRow    int    // last known absolute cursor row (1-based)
	cursorCol    int    // last known absolute cursor col (1-based)
}

type replayStats struct {
	BufSize int
	Written int64
	Trimmed int64
	Readers int
}

func (r *replayBuffer) Stats() replayStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return replayStats{
		BufSize: len(r.buf),
		Written: r.written,
		Trimmed: r.trimmed,
		Readers: len(r.readers),
	}
}

func newReplayBuffer(agent string) *replayBuffer {
	return &replayBuffer{
		buf:          make([]byte, 0, 64*1024),
		notify:       make(chan struct{}),
		advanced:     make(chan struct{}),
		trimPreamble: agentPreamble(agent),
	}
}

// Register adds a reader at the given absolute offset. Returns a cursor
// that must be passed to ReadAfter and eventually Unregister.
func (r *replayBuffer) Register(offset int64) *readerCursor {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := &readerCursor{offset: offset}
	r.readers = append(r.readers, c)
	return c
}

// Unregister removes a reader. May unblock a backpressured writer.
func (r *replayBuffer) Unregister(c *readerCursor) {
	r.mu.Lock()
	for i, rc := range r.readers {
		if rc == c {
			r.readers = append(r.readers[:i], r.readers[i+1:]...)
			break
		}
	}
	// Unblock writer — no reader holding back trim anymore.
	ch := r.advanced
	r.advanced = make(chan struct{})
	r.mu.Unlock()
	close(ch)
}

// Write appends PTY data. If the buffer is full and a reader is behind,
// blocks until the reader catches up (backpressure on the terminal).
// When no reader is attached, trims from front as a ring buffer.
func (r *replayBuffer) Write(p []byte) {
	counted := false
	for {
		r.mu.Lock()
		// Backpressure can retry this append after a reader advances. Account for
		// and interpret the PTY write only once, even when the buffer retry loops.
		if !counted {
			trackCursorPos(p, &r.cursorRow, &r.cursorCol)
			r.written += int64(len(p))
			counted = true
		}
		r.buf = append(r.buf, p...)

		if len(r.buf) <= maxReplaySize {
			// Under limit — just wake readers and return.
			ch := r.notify
			r.notify = make(chan struct{})
			r.mu.Unlock()
			close(ch)
			return
		}

		excess := len(r.buf) - maxReplaySize

		// Find a safe cut point near the excess boundary.
		// Search forward from the excess offset for the nearest frame boundary.
		cut := findSafeCut(r.buf, excess)

		// Re-inject agent's mode preamble + cursor position so replays
		// start with correct terminal state after trim.
		preamble := r.buildTrimPreamble()

		if len(r.readers) == 0 {
			// No readers — trim freely at the safe cut point.
			remaining := append(preamble, r.buf[cut:]...)
			r.buf = append(r.buf[:0], remaining...)
			r.trimmed += int64(cut) - int64(len(preamble))
			ch := r.notify
			r.notify = make(chan struct{})
			r.mu.Unlock()
			close(ch)
			return
		}

		// Find slowest reader.
		minOff := r.readers[0].offset
		for _, rc := range r.readers[1:] {
			if rc.offset < minOff {
				minOff = rc.offset
			}
		}

		canTrim := int(minOff - r.trimmed)
		if canTrim >= cut {
			// Slowest reader is ahead of our safe cut — trim and go.
			remaining := append(preamble, r.buf[cut:]...)
			r.buf = append(r.buf[:0], remaining...)
			r.trimmed += int64(cut) - int64(len(preamble))
			ch := r.notify
			r.notify = make(chan struct{})
			r.mu.Unlock()
			close(ch)
			return
		}

		// Reader is behind — can't trim enough. Undo append, wait for reader to advance.
		r.buf = r.buf[:len(r.buf)-len(p)]
		waitCh := r.advanced
		// Still wake readers so they can consume and advance.
		ch := r.notify
		r.notify = make(chan struct{})
		r.mu.Unlock()
		close(ch)

		<-waitCh // block until a reader advances or disconnects
	}
}

// Snapshot returns a copy of the entire buffer and the absolute end offset.
func (r *replayBuffer) Snapshot() ([]byte, int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]byte, len(r.buf))
	copy(cp, r.buf)
	return cp, r.trimmed + int64(len(r.buf))
}

// WritePosition returns the absolute end offset without copying the buffer.
func (r *replayBuffer) WritePosition() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.trimmed + int64(len(r.buf))
}

// ReadAfter returns data after the cursor's current offset and advances the cursor.
// If no new data, returns nil data and a wait channel for use in select.
func (r *replayBuffer) ReadAfter(c *readerCursor) (data []byte, wait <-chan struct{}) {
	r.mu.Lock()
	relOff := c.offset - r.trimmed
	if relOff < 0 {
		relOff = 0
	}
	if int(relOff) >= len(r.buf) {
		w := r.notify
		r.mu.Unlock()
		return nil, w
	}
	cp := make([]byte, len(r.buf)-int(relOff))
	copy(cp, r.buf[int(relOff):])
	c.offset = r.trimmed + int64(len(r.buf))
	ch := r.advanced
	r.advanced = make(chan struct{})
	r.mu.Unlock()
	close(ch) // signal writer that cursor advanced (may unblock backpressure)
	return cp, nil
}

// Bytes returns a copy of the current buffer.
func (r *replayBuffer) Bytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]byte(nil), r.buf...)
}

// agentPreamble returns the terminal mode sequences an agent sets at startup.
// Re-injected after replay buffer trims so reconnecting clients get correct state.
// If the agent is actively outputting, recent data overrides these anyway.
func agentPreamble(agent string) []byte {
	switch agent {
	case "claude":
		// Hide hardware cursor (Claude renders its own in the TUI),
		// bracketed paste, and synchronized updates. Cursor position is
		// tracked separately and injected at trim time.
		return []byte("\x1b[?25l\x1b[?2004h\x1b[?2026h")
	default:
		return nil
	}
}

// buildTrimPreamble returns the full preamble to inject after a trim:
// static mode sequences + last known cursor position.
func (r *replayBuffer) buildTrimPreamble() []byte {
	if len(r.trimPreamble) == 0 && r.cursorRow == 0 {
		return nil
	}
	var out []byte
	out = append(out, r.trimPreamble...)
	if r.cursorRow > 0 {
		out = append(out, []byte(fmt.Sprintf("\x1b[%d;%dH", r.cursorRow, r.cursorCol))...)
	}
	return out
}

// trackCursorPos scans PTY output for absolute cursor position sequences
// (CSI row;col H) and updates the tracked position. TUI apps like Claude
// use absolute positioning extensively, so this captures the cursor location
// accurately without needing to track relative movements.
func trackCursorPos(data []byte, row *int, col *int) {
	for i := 0; i < len(data); i++ {
		if data[i] != '\x1b' {
			continue
		}
		i++
		if i >= len(data) || data[i] != '[' {
			continue
		}
		i++
		// Parse CSI parameters: digits and semicolons until final byte.
		start := i
		for i < len(data) && ((data[i] >= '0' && data[i] <= '9') || data[i] == ';') {
			i++
		}
		if i >= len(data) {
			break
		}
		final := data[i]
		if final != 'H' && final != 'f' {
			continue
		}
		// CUP — Cursor Position: ESC [ row ; col H
		params := data[start:i]
		r, c := 1, 1
		semi := bytes.IndexByte(params, ';')
		if semi >= 0 {
			if semi > 0 {
				if v, err := strconv.Atoi(string(params[:semi])); err == nil {
					r = v
				}
			}
			if semi+1 < len(params) {
				if v, err := strconv.Atoi(string(params[semi+1:])); err == nil {
					c = v
				}
			}
		} else if len(params) > 0 {
			if v, err := strconv.Atoi(string(params)); err == nil {
				r = v
			}
		}
		*row = r
		*col = c
	}
}

// findSafeCut searches forward from minOffset for the nearest safe cut point.
// Returns an offset into buf where we can trim without corrupting terminal state.
// Safe cut points (in priority order):
//  1. End of a sync-update frame (\x1b[?2026l) — used by Claude, Codex
//  2. Erase-line + column-reset (\x1b[2K\x1b[G]) — used by Cursor
//  3. CRLF boundary — last resort for plain-text agents
//  4. minOffset itself — if nothing better found within search window
func findSafeCut(buf []byte, minOffset int) int {
	// Search up to 64KB past minOffset for a safe boundary.
	searchEnd := minOffset + 64*1024
	if searchEnd > len(buf) {
		searchEnd = len(buf)
	}
	window := buf[minOffset:searchEnd]

	// Try sync-frame end first (Claude, Codex).
	if idx := bytes.Index(window, syncEnd); idx >= 0 {
		return utf8SafeCut(buf, minOffset+idx+len(syncEnd))
	}

	// Try erase-line + column-reset (Cursor).
	if idx := bytes.Index(window, eraseLine); idx >= 0 {
		return utf8SafeCut(buf, minOffset+idx)
	}

	// Fall back to nearest CRLF.
	if idx := bytes.Index(window, []byte("\r\n")); idx >= 0 {
		return utf8SafeCut(buf, minOffset+idx+2)
	}

	return utf8SafeCut(buf, minOffset)
}

// utf8SafeCut moves a proposed byte cut back at most one UTF-8 sequence. PTY
// output can contain arbitrary bytes, so an invalid run of continuation bytes
// retains the original cut instead of causing an unbounded backwards scan.
func utf8SafeCut(data []byte, offset int) int {
	if offset <= 0 {
		return 0
	}
	if offset >= len(data) {
		return len(data)
	}
	adjusted := offset
	for steps := 0; steps < utf8.UTFMax-1 && adjusted > 0 && !utf8.RuneStart(data[adjusted]); steps++ {
		adjusted--
	}
	if utf8.RuneStart(data[adjusted]) {
		return adjusted
	}
	return offset
}

// NewServer creates a new per-session egg server.
// dir is the session directory: ~/.wingthing/eggs/<session-id>/
func NewServer(dir string) (*Server, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("generate token: %w", err)
	}
	return &Server{
		dir:   dir,
		token: fmt.Sprintf("%x", tokenBytes),
	}, nil
}

// Mouse-tracking enables the browser terminal must never see. Claude turns mouse
// reporting on so the wheel reaches it, but if xterm.js also enters mouse mode it stops
// doing local selection and drag-to-copy dies — there is no DECSET that reports the
// wheel without also capturing buttons. So we swallow the enable: the agent believes
// mouse is on and parses the SGR wheel events terminal.js synthesizes, while the
// terminal never captures the mouse and selection keeps working. Disables pass through.
var mouseTrackingEnables = [][]byte{
	[]byte("\x1b[?1000h"), []byte("\x1b[?1002h"), []byte("\x1b[?1003h"),
	[]byte("\x1b[?1005h"), []byte("\x1b[?1006h"), []byte("\x1b[?1015h"), []byte("\x1b[?1016h"),
}

// Private OSC left in place of the enable so the browser terminal knows the agent will
// parse mouse input even though xterm never entered mouse mode — it sends SGR wheel
// events when it sees this and falls back to arrow keys when it doesn't. xterm discards
// OSCs it has no handler for, so anything else rendering this stream is unaffected.
var mouseTrackingMarker = []byte("\x1b]7771;1\x07")

func stripMouseTracking(data []byte) []byte {
	found := false
	for _, seq := range mouseTrackingEnables {
		if bytes.Contains(data, seq) {
			data = bytes.ReplaceAll(data, seq, nil)
			found = true
		}
	}
	if found {
		data = append(data, mouseTrackingMarker...)
	}
	return data
}

// RunSession is the core lifecycle: create sandbox, start agent in PTY, serve gRPC, exit when done.
func (s *Server) RunSession(ctx context.Context, rc RunConfig) error {
	if err := validatePTYSize(rc.Cols, rc.Rows); err != nil {
		return err
	}
	name, args := sessionCommand(rc)
	if len(rc.Command) > 0 {
		if rc.Kind == "" {
			rc.Kind = "command"
		}
	} else {
		if name == "" {
			return fmt.Errorf("unsupported agent: %s", rc.Agent)
		}
		if rc.Kind == "" {
			rc.Kind = "agent"
		}
	}

	sessionPolicy := runConfigPolicy(rc)
	if rc.OuterBoundary && RequiresSandbox(sessionPolicy, rc.Agent) {
		return errors.New("outer-boundary mode cannot be combined with filesystem, network, environment, or resource restrictions")
	}
	hasSandbox := !rc.OuterBoundary
	if hasSandbox {
		if ok, help := sandbox.CheckCapability(); !ok {
			return fmt.Errorf("sandbox not available: %s\nrun: wt doctor --fix", help)
		}
	}

	binPath, err := exec.LookPath(name)
	if err != nil {
		return fmt.Errorf("command %q not found: %v", name, err)
	}
	// Resolve symlinks so the real binary path works inside namespaces
	// (e.g. ~/.local/bin/claude -> ~/.claude/bin/claude)
	if resolved, err := filepath.EvalSymlinks(binPath); err == nil {
		binPath = resolved
	}
	log.Printf("egg: command binary: %s", binPath)

	// Build environment: always use rc.Env (caller filtered via BuildEnvMap).
	// Merge agent profile required vars + essentials from host env if missing.
	profile := Profile(rc.Agent)
	envMap := make(map[string]string, len(rc.Env))
	for k, v := range rc.Env {
		envMap[k] = v
	}
	// Merge required env vars from agent profile
	if !rc.SkipHostAgentEnv {
		for _, k := range profile.EnvVars {
			if _, ok := envMap[k]; !ok {
				if v := os.Getenv(k); v != "" {
					envMap[k] = v
				}
			}
		}
		// Merge platform-specific env vars (e.g. macOS Keychain access for Claude)
		for _, k := range profile.PlatformEnv {
			if _, ok := envMap[k]; !ok {
				if v := os.Getenv(k); v != "" {
					envMap[k] = v
				}
			}
		}
	}
	// Agent-forced env defaults (e.g. CLAUDE_CODE_DISABLE_MOUSE). Host/config still wins.
	for k, v := range profile.SetEnv {
		if _, ok := envMap[k]; !ok {
			envMap[k] = v
		}
	}
	// Ensure essentials are present
	for _, k := range []string{"HOME", "PATH", "TERM"} {
		if _, ok := envMap[k]; !ok {
			if v := os.Getenv(k); v != "" {
				envMap[k] = v
			}
		}
	}
	// Default TERM for headless services (systemd has no terminal)
	if envMap["TERM"] == "" {
		envMap["TERM"] = "xterm-256color"
	}
	if envMap["GOTRACEBACK"] == "" {
		envMap["GOTRACEBACK"] = "all"
	}
	// Agent terminals should not get interleaved git credential prompts. Human
	// shell/command sessions retain ordinary terminal behavior.
	if rc.Kind == "agent" {
		if _, ok := envMap["GIT_TERMINAL_PROMPT"]; !ok {
			envMap["GIT_TERMINAL_PROMPT"] = "0"
		}
	}
	// Per-user home override for relay sessions
	if rc.UserHome != "" {
		envMap["HOME"] = rc.UserHome
	}
	// Prepend ~/.local/bin to PATH so agents like Claude Code find their
	// native installation and don't warn about missing PATH entries.
	home := envMap["HOME"]
	if home != "" {
		localBin := filepath.Join(home, ".local", "bin")
		if p, ok := envMap["PATH"]; ok {
			envMap["PATH"] = localBin + ":" + p
		} else {
			envMap["PATH"] = localBin + ":/usr/bin:/bin"
		}
	}

	// Snapshot agent config before session so we can restore on exit
	configSnap := SnapshotAgentConfig(rc.Agent)

	// Resolve declared, agent-profile, and provider-derived domains through the
	// same policy path used by `wt egg explain`.
	networkPolicy, err := ResolvePolicyWithProvider(&EggConfig{
		Network: NetworkField{Domains: rc.Network, LocalPorts: rc.LocalPorts, Mode: rc.NetworkMode, AgentDomains: rc.AgentDomains},
	}, rc.Agent, envMap["HOME"], envMap["WT_PROVIDER_BASE_URL"])
	if err != nil {
		return err
	}
	mergedDomains := networkPolicy.Domains
	netNeed := sandbox.NetworkNeedFromDomains(mergedDomains)

	// A sandboxed Linux process has no route except the inherited relay. Proxy
	// setup therefore fails closed instead of degrading to advisory env vars.
	var domainProxy *sandbox.DomainProxy
	if hasSandbox {
		domainProxy, err = sandbox.StartPolicyProxyWithMode(netNeed, mergedDomains, networkPolicy.Mode)
		if err != nil {
			return fmt.Errorf("start enforcing network proxy: %w", err)
		}
		if domainProxy != nil {
			proxyURL := fmt.Sprintf("http://127.0.0.1:%d", domainProxy.Port())
			envMap["HTTPS_PROXY"] = proxyURL
			envMap["HTTP_PROXY"] = proxyURL
			envMap["NODE_USE_ENV_PROXY"] = "1" // node 22.18+ native proxy support
		}
	}
	// Keep proxy ownership scoped to this RunSession even when a later sandbox,
	// PTY, socket, or metadata setup step fails. Close is idempotent, so the
	// normal process-exit cleanup below may still release it eagerly.
	defer func() {
		if domainProxy != nil {
			domainProxy.Close()
		}
	}()

	// Browser open interception shim. The request file is the only writable
	// object from the session directory exposed to a sandbox; logs, metadata,
	// tokens, and control sockets remain outside its writable mount set.
	browserRequestsPath := filepath.Join(s.dir, "browser-requests")
	browserRequests, err := os.OpenFile(browserRequestsPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create browser request file: %w", err)
	}
	if err := browserRequests.Close(); err != nil {
		return fmt.Errorf("close browser request file: %w", err)
	}
	shimDir := filepath.Join(s.dir, "shims")
	if err := os.MkdirAll(shimDir, 0o755); err != nil {
		return fmt.Errorf("create browser shim directory: %w", err)
	}
	shimScript := "#!/bin/sh\nprintf '%s\\n' \"$1\" >> \"$WT_SESSION_DIR/browser-requests\"\n"
	shimPath := filepath.Join(shimDir, "wt-browser")
	if err := os.WriteFile(shimPath, []byte(shimScript), 0o755); err != nil {
		return fmt.Errorf("write browser shim: %w", err)
	}
	if err := os.Symlink("wt-browser", filepath.Join(shimDir, "open")); err != nil {
		return fmt.Errorf("link open browser shim: %w", err)
	}
	if err := os.Symlink("wt-browser", filepath.Join(shimDir, "xdg-open")); err != nil {
		return fmt.Errorf("link xdg-open browser shim: %w", err)
	}
	envMap["BROWSER"] = "wt-browser"
	envMap["WT_SESSION_DIR"] = s.dir
	if path, ok := envMap["PATH"]; ok {
		envMap["PATH"] = shimDir + ":" + path
	} else {
		envMap["PATH"] = shimDir + ":/usr/bin:/bin"
	}

	// Generate tool shims for privileged tools (called via wt tool-call).
	// Tool shims live in the .tools dir (parent of ToolSocketPath) which gets
	// mounted read-only into the sandbox — separate from the session dir so
	// audit logs, chat data, etc. are never exposed to the sandboxed agent.
	if rc.ToolSocketPath != "" && len(rc.ToolNames) > 0 {
		toolsDir := filepath.Dir(rc.ToolSocketPath)
		envMap["WT_TOOL_SOCKET"] = rc.ToolSocketPath
		wtBin, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locate wt binary for tool shims: %w", err)
		}
		if resolved, err := filepath.EvalSymlinks(wtBin); err == nil {
			wtBin = resolved
		}
		for _, name := range rc.ToolNames {
			toolShim := fmt.Sprintf("#!/bin/sh\nexec \"%s\" tool-call %s \"$@\"\n", wtBin, name)
			if err := os.WriteFile(filepath.Join(toolsDir, name), []byte(toolShim), 0o755); err != nil {
				return fmt.Errorf("write tool shim %q: %w", name, err)
			}
		}
		if path, ok := envMap["PATH"]; ok {
			envMap["PATH"] = toolsDir + ":" + path
		} else {
			envMap["PATH"] = toolsDir + ":" + shimDir + ":/usr/bin:/bin"
		}
	}

	// Build envSlice AFTER proxy setup so HTTPS_PROXY etc. are included
	var envSlice []string
	for k, v := range envMap {
		envSlice = append(envSlice, k+"="+v)
	}

	sessionID := filepath.Base(s.dir)

	// Build sandbox and command
	var sb sandbox.Sandbox
	var cmd *exec.Cmd

	if hasSandbox {
		home, _ := os.UserHomeDir()
		// Use per-user home for ~ expansion when set, so FS rules like
		// rw:~/.cache resolve to the per-user home, not the host home.
		fsHome := home
		if rc.UserHome != "" {
			fsHome = rc.UserHome
		}
		mounts, deny, denyWrite := ParseFSRules(rc.FS, fsHome)
		mounts = append(mounts, sandbox.Mount{
			Source: browserRequestsPath,
			Target: browserRequestsPath,
		})

		// Auto-inject agent binary install root so sandbox can find it.
		if home != "" && len(mounts) > 0 {
			realBin := binPath
			if resolved, err := filepath.EvalSymlinks(binPath); err == nil {
				realBin = resolved
			}
			binDir := filepath.Dir(realBin)
			if strings.HasPrefix(binDir, home+string(filepath.Separator)) {
				root := installRoot(binDir, home)
				mounts = append(mounts, sandbox.Mount{Source: root, Target: root, ReadOnly: true})
			}
		}

		// Auto-inject agent profile write dirs.
		// Use per-user home for agent config dirs when set.
		profileHome := home
		if rc.UserHome != "" {
			profileHome = rc.UserHome
		}
		if profileHome != "" && len(mounts) > 0 {
			for _, d := range profile.WriteRegex {
				abs := filepath.Join(profileHome, d)
				if err := os.MkdirAll(abs, 0o700); err != nil {
					return fmt.Errorf("create agent profile directory %s: %w", abs, err)
				}
				mounts = append(mounts, sandbox.Mount{Source: abs, Target: abs, UseRegex: true})
			}
			for _, d := range profile.WriteDirs {
				abs := filepath.Join(profileHome, d)
				if err := os.MkdirAll(abs, 0o700); err != nil {
					return fmt.Errorf("create agent profile directory %s: %w", abs, err)
				}
				mounts = append(mounts, sandbox.Mount{Source: abs, Target: abs})
			}
		}

		// Mount wt binary dir and .tools dir for tool shim access in jail mode.
		// Only .tools is mounted — the rest of the session dir (audit logs, chat
		// data) stays invisible to the sandboxed agent.
		if rc.ToolSocketPath != "" && len(rc.ToolNames) > 0 {
			wtBin, err := os.Executable()
			if err != nil {
				return fmt.Errorf("locate wt binary for sandbox mounts: %w", err)
			}
			if resolved, err := filepath.EvalSymlinks(wtBin); err == nil {
				wtBin = resolved
			}
			wtDir := filepath.Dir(wtBin)
			toolsDir := filepath.Dir(rc.ToolSocketPath)
			mounts = append(mounts, sandbox.Mount{Source: wtDir, Target: wtDir, ReadOnly: true})
			mounts = append(mounts, sandbox.Mount{Source: toolsDir, Target: toolsDir, ReadOnly: true})
		}

		proxyPort := 0
		if domainProxy != nil {
			proxyPort = domainProxy.Port()
		}

		allowSockets := sandboxAllowedSockets(rc.ToolSocketPath, envMap)

		sbCfg := sandbox.Config{
			Mounts:       mounts,
			Deny:         deny,
			DenyWrite:    denyWrite,
			NetworkNeed:  netNeed,
			NetworkMode:  networkPolicy.Mode,
			Domains:      mergedDomains,
			ProxyPort:    proxyPort,
			LocalPorts:   append([]int(nil), networkPolicy.LocalPorts...),
			CPULimit:     rc.CPULimit,
			MemLimit:     rc.MemLimit,
			MaxFDs:       rc.MaxFDs,
			PidLimit:     rc.PidLimit,
			SessionID:    sessionID,
			UserHome:     rc.UserHome,
			Trace:        rc.Trace,
			AllowSockets: allowSockets,
		}

		sb, err = sandbox.New(sbCfg)
		if err != nil {
			return fmt.Errorf("sandbox: %v", err)
		}
		cmd, err = sb.Exec(context.Background(), binPath, args)
		if err != nil {
			if destroyErr := sb.Destroy(); destroyErr != nil {
				log.Printf("egg: destroy sandbox after exec failure: %v", destroyErr)
			}
			return fmt.Errorf("sandbox exec: %v", err)
		}
		cmd.Env = envSlice
		if rc.CWD != "" {
			cmd.Dir = rc.CWD
		}
	} else {
		log.Printf("SECURITY: egg runs in outer-boundary mode with the full authority of the local OS user; Wingthing filesystem, network, syscall, and resource isolation is disabled")
		cmd = exec.CommandContext(context.Background(), binPath, args...)
		cmd.Env = envSlice
		if rc.CWD != "" {
			cmd.Dir = rc.CWD
		}
	}

	// Graceful termination
	cmd.Cancel = func() error {
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = 5 * time.Second

	// Prepare the control endpoint before starting the child. Once the PTY has
	// started, every subsequent failure path has to terminate and reap an
	// untrusted process. Binding the socket and durably creating its credentials
	// first makes endpoint setup atomic and prevents a permissions or filesystem
	// error from leaving an unreachable agent running in the background.
	lis, err := s.prepareEndpoint()
	if err != nil {
		if sb != nil {
			_ = sb.Destroy()
		}
		return err
	}
	endpointOwnedByServer := false
	defer func() {
		if !endpointOwnedByServer {
			s.closeEndpoint(lis)
		}
	}()

	size := &pty.Winsize{Cols: uint16(rc.Cols), Rows: uint16(rc.Rows)}
	ptmx, err := pty.StartWithSize(cmd, size)
	if err != nil {
		if sb != nil {
			if destroyErr := sb.Destroy(); destroyErr != nil {
				log.Printf("egg: destroy sandbox after PTY failure: %v", destroyErr)
			}
		}
		// Detect namespace creation failures that race with or otherwise slip
		// past the capability probe. Name the actual failed outer operation and
		// let the sandbox package provide platform-specific (including WSL2)
		// guidance rather than guessing at a security profile.
		if strings.Contains(err.Error(), "operation not permitted") || strings.Contains(err.Error(), "permission denied") {
			return fmt.Errorf("start sandboxed PTY with user/mount/PID/network namespaces: %v. %s", err, sandbox.CapabilityFailureHelp())
		}
		return fmt.Errorf("start pty: %v", err)
	}

	// Apply post-start hooks (rlimits on Linux)
	if sb != nil {
		if psErr := sb.PostStart(cmd.Process.Pid); psErr != nil {
			log.Printf("egg: sandbox post-start warning: %v", psErr)
		}
	}

	networkSummary := networkSummaryFromDomains(mergedDomains)
	sess := &Session{
		ID:             sessionID,
		PID:            cmd.Process.Pid,
		Agent:          rc.Agent,
		Kind:           rc.Kind,
		Command:        append([]string(nil), rc.Command...),
		CWD:            rc.CWD,
		Network:        networkSummary,
		RenderedConfig: rc.RenderedConfig,
		StartedAt:      time.Now(),
		idleTimeout:    rc.IdleTimeout,
		Cols:           rc.Cols,
		Rows:           rc.Rows,
		ptmx:           ptmx,
		replay:         newReplayBuffer(rc.Agent),
		vterm:          NewVTerm(int(rc.Cols), int(rc.Rows)),
		vtermCh:        make(chan vtermMsg, 256),
		useVTE:         rc.VTE,
		sb:             sb,
		cmd:            cmd,
		done:           make(chan struct{}),
		debug:          rc.Debug,
		audit:          rc.Audit,
	}

	// Set up input auditor if audit is enabled
	if rc.Audit {
		auditPath := filepath.Join(s.dir, "audit.log")
		auditor, auditErr := newInputAuditor(auditPath)
		if auditErr != nil {
			log.Printf("egg: audit log failed: %v", auditErr)
		} else {
			sess.auditor = auditor
			log.Printf("egg: audit enabled → %s", auditPath)
		}
	}

	s.mu.Lock()
	s.session = sess
	s.mu.Unlock()

	log.Printf("egg: session %s kind=%s agent=%s command=%q pid=%d network=%s fs=%d", sessionID, rc.Kind, rc.Agent, rc.Command, cmd.Process.Pid, networkSummary, len(rc.FS))

	// VTerm async processing goroutine — must start before readPTY
	go runVTermLoop(sess.vterm, sess.vtermCh, sess.done)

	// Read PTY output (with first-byte timing)
	go s.readPTY(sess)

	// Watchdog: if no PTY output within 15s, dump diagnostic info
	go s.startupWatchdog(sess)

	// Idle timeout self-termination (safety net if wing dies)
	if sess.idleTimeout > 0 {
		go s.idleWatchdog(sess)
	}

	// Periodic chat history capture (runs outside sandbox, on host filesystem)
	captureHome := rc.UserHome
	if captureHome == "" {
		captureHome, _ = os.UserHomeDir()
	}
	if profile := Profile(rc.Agent); profile.SessionDir != "" && captureHome != "" {
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					if err := CaptureSessionHistory(rc.Agent, rc.CWD, s.dir, captureHome, sess.StartedAt); err != nil {
						log.Printf("egg: chat capture: %v", err)
					}
				case <-sess.done:
					return
				}
			}
		}()
	}

	// Write session metadata so the wing can read it on reclaim
	metaPath := filepath.Join(s.dir, "egg.meta")
	isolationMode := "outer-boundary"
	if hasSandbox {
		isolationMode = "wingthing-sandbox"
	}
	metaContent := fmt.Sprintf("agent=%s\nkind=%s\ncommand=%s\ncwd=%s\nnetwork=%s\nisolation=%s\ncols=%d\nrows=%d\nstarted_at=%d\n",
		rc.Agent, rc.Kind, formatCommand(rc.Command), rc.CWD, networkSummary, isolationMode, rc.Cols, rc.Rows, sess.StartedAt.Unix())
	if err := atomicWritePrivate(metaPath, []byte(metaContent)); err != nil {
		log.Printf("egg: warning: write meta: %v", err)
	}

	s.grpcServer = grpc.NewServer(
		grpc.ChainUnaryInterceptor(recoveryUnary, s.authUnary),
		grpc.ChainStreamInterceptor(recoveryStream, s.authStream),
	)
	pb.RegisterEggServer(s.grpcServer, s)

	log.Printf("egg: serving on %s (pid %d)", lis.Addr(), os.Getpid())

	// Wait for process exit in background
	go func() {
		exitCode := 0
		if err := cmd.Wait(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = 1
			}
		}
		sess.mu.Lock()
		sess.exitCode = exitCode
		sess.mu.Unlock()
		close(sess.done)
		log.Printf("egg: session %s exited with code %d", sessionID, exitCode)

		if err := ptmx.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			log.Printf("egg: close PTY: %v", err)
		}
		configSnap.Restore()
		if sess.sb != nil {
			if diagPath := sess.sb.DiagLog(); diagPath != "" {
				if data, readErr := os.ReadFile(diagPath); readErr == nil && len(data) > 0 {
					logsDir := filepath.Join(filepath.Dir(filepath.Dir(s.dir)), "logs")
					if err := os.MkdirAll(logsDir, 0o700); err != nil {
						log.Printf("egg: create diagnostic log directory: %v", err)
					} else if err := atomicWritePrivate(filepath.Join(logsDir, sessionID+".deny_init.log"), data); err != nil {
						log.Printf("egg: preserve sandbox diagnostic: %v", err)
					}
				} else if readErr != nil {
					log.Printf("egg: read sandbox diagnostic: %v", readErr)
				}
			}
			if tracePath := sess.sb.TraceLog(); tracePath != "" {
				if data, readErr := os.ReadFile(tracePath); readErr == nil && len(data) > 0 {
					logsDir := filepath.Join(filepath.Dir(filepath.Dir(s.dir)), "logs")
					if len(data) > 10*1024*1024 {
						data = data[len(data)-10*1024*1024:]
					}
					if err := os.MkdirAll(logsDir, 0o700); err != nil {
						log.Printf("egg: create trace log directory: %v", err)
					} else if err := atomicWritePrivate(filepath.Join(logsDir, sessionID+".strace.log"), data); err != nil {
						log.Printf("egg: preserve sandbox trace: %v", err)
					}
				} else if readErr != nil {
					log.Printf("egg: read sandbox trace: %v", readErr)
				}
			}
			if err := sess.sb.Destroy(); err != nil {
				log.Printf("egg: destroy sandbox: %v", err)
			}
		}
		// Tear down the namespace bridge before releasing the host proxy's
		// loopback port. Otherwise a local process can win the brief port-reuse
		// race and receive connections from a still-live sandbox relay.
		if domainProxy != nil {
			domainProxy.Close()
		}

		// Final chat history capture (gets the complete conversation)
		if profile := Profile(rc.Agent); profile.SessionDir != "" && captureHome != "" {
			if err := CaptureSessionHistory(rc.Agent, rc.CWD, s.dir, captureHome, sess.StartedAt); err != nil {
				log.Printf("egg: final chat capture: %v", err)
			}
		}

		// Give gRPC a moment to send exit_code, then stop
		time.Sleep(500 * time.Millisecond)
		s.grpcServer.GracefulStop()
	}()

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.grpcServer.Serve(lis)
	}()
	endpointOwnedByServer = true

	select {
	case <-ctx.Done():
		s.shutdown()
		return ctx.Err()
	case err := <-errCh:
		s.cleanup()
		return err
	}
}

// sandboxAllowedSockets converts already-filtered endpoint environment into
// explicit Seatbelt exceptions. SSH_AUTH_SOCK reaches envMap only when the egg
// deliberately opted in (shared-host policy strips it unconditionally), but a
// macOS proxy profile still starts with deny network* and therefore needs the
// live Unix socket named separately.
func sandboxAllowedSockets(toolSocket string, envMap map[string]string) []string {
	var sockets []string
	if toolSocket != "" {
		sockets = append(sockets, toolSocket)
	}

	sshSocket := strings.TrimSpace(envMap["SSH_AUTH_SOCK"])
	if sshSocket == "" || !filepath.IsAbs(sshSocket) {
		return sockets
	}
	sshSocket = filepath.Clean(sshSocket)
	if resolved, err := filepath.EvalSymlinks(sshSocket); err == nil {
		sshSocket = resolved
	}
	info, err := os.Stat(sshSocket)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return sockets
	}
	for _, existing := range sockets {
		if existing == sshSocket {
			return sockets
		}
	}
	return append(sockets, sshSocket)
}

// prepareEndpoint creates the local control endpoint as one transaction. It is
// deliberately called before the PTY starts, so any failure here is guaranteed
// not to strand an agent process with no way to attach or terminate it.
func (s *Server) prepareEndpoint() (net.Listener, error) {
	sockPath := filepath.Join(s.dir, "egg.sock")
	tokenPath := filepath.Join(s.dir, "egg.token")
	pidPath := filepath.Join(s.dir, "egg.pid")

	if err := os.Remove(sockPath); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("remove stale socket: %w", err)
	}
	lis, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}
	fail := func(step string, err error) (net.Listener, error) {
		s.closeEndpoint(lis)
		return nil, fmt.Errorf("%s: %w", step, err)
	}
	if err := os.Chmod(sockPath, 0o600); err != nil {
		return fail("secure socket", err)
	}
	if err := os.WriteFile(tokenPath, []byte(s.token), 0o600); err != nil {
		return fail("write token", err)
	}
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		return fail("write pid", err)
	}
	s.listener = lis
	return lis, nil
}

func (s *Server) closeEndpoint(lis net.Listener) {
	if lis != nil {
		_ = lis.Close()
	}
	_ = os.Remove(filepath.Join(s.dir, "egg.sock"))
	_ = os.Remove(filepath.Join(s.dir, "egg.token"))
	_ = os.Remove(filepath.Join(s.dir, "egg.pid"))
}

func runConfigPolicy(rc RunConfig) *EggConfig {
	policy := &EggConfig{
		FS: rc.FS,
		Network: NetworkField{
			Domains: rc.Network, LocalPorts: rc.LocalPorts,
			Mode: rc.NetworkMode, AgentDomains: rc.AgentDomains,
		},
		Resources: EggResources{MaxFDs: rc.MaxFDs, MaxPids: rc.PidLimit},
		Trace:     rc.Trace,
	}
	if rc.OuterBoundary {
		policy.Env = EnvField{"*"}
	}
	if rc.CPULimit > 0 {
		policy.Resources.CPU = rc.CPULimit.String()
	}
	if rc.MemLimit > 0 {
		policy.Resources.Memory = strconv.FormatUint(rc.MemLimit, 10)
	}
	return policy
}

func (s *Server) shutdown() {
	log.Println("egg: shutting down...")
	s.mu.RLock()
	sess := s.session
	s.mu.RUnlock()
	if sess != nil && sess.cmd != nil && sess.cmd.Process != nil {
		if err := sess.cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
			log.Printf("egg: signal session during shutdown: %v", err)
		}
		time.Sleep(3 * time.Second)
		if err := sess.cmd.Process.Signal(syscall.Signal(0)); err == nil {
			if err := sess.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				log.Printf("egg: kill session during shutdown: %v", err)
			}
		}
		if sess.sb != nil {
			if err := sess.sb.Destroy(); err != nil {
				log.Printf("egg: destroy sandbox during shutdown: %v", err)
			}
		}
	}
	if s.grpcServer != nil {
		s.grpcServer.GracefulStop()
	}
	s.cleanup()
}

func (s *Server) cleanup() {
	// Preserve egg.log to persistent log dir before removing the session directory.
	// Logs are not audits — always keep them so `wt support` can capture crash reasons.
	s.preserveEggLog()

	for _, name := range []string{"egg.sock", "egg.token", "egg.pid"} {
		if err := os.Remove(filepath.Join(s.dir, name)); err != nil && !os.IsNotExist(err) {
			log.Printf("egg: remove %s during cleanup: %v", name, err)
		}
	}
	s.mu.RLock()
	sess := s.session
	s.mu.RUnlock()
	// Keep session dir if audit recordings or chat history exist
	hasAudit := sess != nil && sess.audit
	_, hasChat := os.Stat(filepath.Join(s.dir, "chat.jsonl.gz"))
	if !hasAudit && hasChat != nil {
		if err := os.RemoveAll(s.dir); err != nil {
			log.Printf("egg: remove session directory: %v", err)
		}
	}
}

// preserveEggLog copies the session's egg.log to ~/.wingthing/logs/<session-id>.log,
// pruning old logs to keep at most 20.
func (s *Server) preserveEggLog() {
	src := filepath.Join(s.dir, "egg.log")
	data, err := os.ReadFile(src)
	if err != nil || len(data) == 0 {
		return
	}
	logsDir := filepath.Join(filepath.Dir(filepath.Dir(s.dir)), "logs")
	if err := os.MkdirAll(logsDir, 0o700); err != nil {
		log.Printf("egg: create persistent log directory: %v", err)
		return
	}
	sessionID := filepath.Base(s.dir)
	dst := filepath.Join(logsDir, sessionID+".log")
	if err := atomicWritePrivate(dst, data); err != nil {
		log.Printf("egg: preserve log: %v", err)
		return
	}

	// Prune: keep most recent 20 logs
	entries, err := os.ReadDir(logsDir)
	if err != nil {
		log.Printf("egg: list persistent logs: %v", err)
		return
	}
	type persistentLog struct {
		name    string
		modTime time.Time
	}
	logs := make([]persistentLog, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".log" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			log.Printf("egg: stat persistent log %s: %v", entry.Name(), err)
			continue
		}
		logs = append(logs, persistentLog{name: entry.Name(), modTime: info.ModTime()})
	}
	if len(logs) <= 20 {
		return
	}
	sort.Slice(logs, func(i, j int) bool { return logs[i].modTime.Before(logs[j].modTime) })
	for _, entry := range logs[:len(logs)-20] {
		if err := os.Remove(filepath.Join(logsDir, entry.name)); err != nil && !os.IsNotExist(err) {
			log.Printf("egg: prune persistent log %s: %v", entry.name, err)
		}
	}
}

func (s *Server) readPTY(sess *Session) {
	var debugFile *os.File
	if sess.debug {
		path := "/tmp/wt-pty-" + sess.Agent + "-" + sess.ID + ".bin"
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			log.Printf("egg: debug: cannot open %s: %v", path, err)
		} else {
			debugFile = f
			defer func() {
				if err := f.Close(); err != nil {
					log.Printf("egg: close PTY debug file: %v", err)
				}
			}()
			log.Printf("egg: debug: writing raw PTY output to %s", path)
		}
	}

	// PTY stream audit: gzipped V2 varint delta format for replay
	if sess.audit {
		path := filepath.Join(s.dir, "audit.pty.gz")
		f, gw, err := createPTYAudit(path, sess.Cols, sess.Rows)
		if err != nil {
			log.Printf("egg: audit pty recording failed: %v", err)
		} else {
			sess.auditMu.Lock()
			sess.auditWriter = gw
			sess.auditFile = f
			sess.auditStart = sess.StartedAt
			sess.auditMu.Unlock()
			defer func() {
				sess.auditMu.Lock()
				sess.auditWriter = nil
				sess.auditFile = nil
				sess.auditMu.Unlock()
				if err := gw.Close(); err != nil {
					log.Printf("egg: close PTY audit compressor: %v", err)
				}
				if err := f.Sync(); err != nil {
					log.Printf("egg: sync PTY audit: %v", err)
				}
				if err := f.Close(); err != nil {
					log.Printf("egg: close PTY audit: %v", err)
				}
			}()
		}
	}

	buf := make([]byte, 4096)
	firstByte := true
	for {
		n, err := sess.ptmx.Read(buf)
		if n > 0 {
			if firstByte {
				log.Printf("egg: first PTY output from pid %d after %s (%d bytes)", sess.PID, time.Since(sess.StartedAt).Round(time.Millisecond), n)
				firstByte = false
			}
			data := make([]byte, n)
			copy(data, buf[:n])
			data = stripMouseTracking(data)
			sess.replay.Write(data)
			sess.mu.Lock()
			sess.lastOutput = time.Now()
			sess.mu.Unlock()
			offset := sess.replay.WritePosition()
			select {
			case sess.vtermCh <- vtermMsg{data: data, offset: offset}:
			default:
			}
			if debugFile != nil {
				if _, err := debugFile.Write(data); err != nil {
					log.Printf("egg: write PTY debug file: %v", err)
					debugFile = nil
				}
			}
			sess.writeAuditFrame(0, data)
		}
		if err != nil {
			// Close auditor on PTY exit
			if sess.auditor != nil {
				sess.auditor.Close()
			}
			return
		}
	}
}

func createPTYAudit(path string, cols, rows uint32) (*os.File, *gzip.Writer, error) {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".audit-pty-*.tmp")
	if err != nil {
		return nil, nil, err
	}
	temporaryPath := f.Name()
	committed := false
	defer func() {
		if !committed {
			_ = f.Close()
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		return nil, nil, err
	}
	gw := gzip.NewWriter(f)
	if _, err := gw.Write([]byte("WTA2")); err != nil {
		_ = gw.Close()
		return nil, nil, err
	}
	if err := writeVarint(gw, uint64(cols)); err != nil {
		_ = gw.Close()
		return nil, nil, err
	}
	if err := writeVarint(gw, uint64(rows)); err != nil {
		_ = gw.Close()
		return nil, nil, err
	}
	if err := gw.Flush(); err != nil {
		_ = gw.Close()
		return nil, nil, err
	}
	if err := f.Sync(); err != nil {
		_ = gw.Close()
		return nil, nil, err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		_ = gw.Close()
		return nil, nil, err
	}
	committed = true
	return f, gw, nil
}

// writeAuditFrame writes a V2 audit frame (delta_ms, frame_type, data_len, data).
func (sess *Session) writeAuditFrame(frameType uint64, data []byte) {
	sess.auditMu.Lock()
	defer sess.auditMu.Unlock()
	if sess.auditWriter == nil {
		return
	}
	ms := uint64(time.Since(sess.auditStart).Milliseconds())
	delta := ms - sess.auditLastMS
	sess.auditLastMS = ms
	if err := writeVarint(sess.auditWriter, delta); err != nil {
		sess.failAuditLocked(err)
		return
	}
	if err := writeVarint(sess.auditWriter, frameType); err != nil {
		sess.failAuditLocked(err)
		return
	}
	if err := writeVarint(sess.auditWriter, uint64(len(data))); err != nil {
		sess.failAuditLocked(err)
		return
	}
	if _, err := sess.auditWriter.Write(data); err != nil {
		sess.failAuditLocked(err)
		return
	}
	sess.auditFrames++
	if sess.auditFrames%100 == 0 {
		if err := sess.auditWriter.Flush(); err != nil {
			sess.failAuditLocked(err)
			return
		}
		if sess.auditFile != nil {
			if err := sess.auditFile.Sync(); err != nil {
				sess.failAuditLocked(err)
			}
		}
	}
}

func (sess *Session) failAuditLocked(err error) {
	if sess.auditErr == nil {
		sess.auditErr = err
		log.Printf("egg: PTY audit recording stopped after write failure: %v", err)
	}
	sess.auditWriter = nil
}

// writeAuditResize writes a resize event to the audit stream.
func (sess *Session) writeAuditResize(cols, rows uint32) {
	var buf [20]byte
	n := binary.PutUvarint(buf[:], uint64(cols))
	n += binary.PutUvarint(buf[n:], uint64(rows))
	sess.writeAuditFrame(1, buf[:n])
}

// writeVarint writes a protobuf-style unsigned varint.
func writeVarint(w io.Writer, v uint64) error {
	var buf [10]byte
	n := binary.PutUvarint(buf[:], v)
	_, err := w.Write(buf[:n])
	return err
}

// Kill terminates the session.
func (s *Server) Kill(ctx context.Context, req *pb.KillRequest) (*pb.KillResponse, error) {
	s.mu.RLock()
	sess := s.session
	s.mu.RUnlock()
	if sess == nil {
		return nil, status.Error(codes.NotFound, "no session")
	}
	if err := terminateSession(ctx, sess, 3*time.Second); err != nil {
		return nil, status.Errorf(codes.Internal, "terminate session: %v", err)
	}
	return &pb.KillResponse{}, nil
}

// terminateSession does not report success until the child has actually
// exited. Interactive shells commonly ignore SIGTERM, so a fire-and-forget
// signal makes `wt session kill` lie while the egg and its session remain
// discoverable. Give cooperative processes a grace period, then force the
// process down and wait for the normal cmd.Wait cleanup path to complete.
func terminateSession(ctx context.Context, sess *Session, grace time.Duration) error {
	if sess == nil || sess.cmd == nil || sess.cmd.Process == nil {
		return nil
	}

	if err := sess.cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("signal: %w", err)
	}

	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-sess.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}

	if err := sess.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("kill after %s grace period: %w", grace, err)
	}
	select {
	case <-sess.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Resize changes the terminal dimensions.
func (s *Server) Resize(ctx context.Context, req *pb.ResizeRequest) (*pb.ResizeResponse, error) {
	s.mu.RLock()
	sess := s.session
	s.mu.RUnlock()
	if sess == nil {
		return nil, status.Error(codes.NotFound, "no session")
	}
	if err := validatePTYSize(req.Cols, req.Rows); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := pty.Setsize(sess.ptmx, &pty.Winsize{
		Cols: uint16(req.Cols),
		Rows: uint16(req.Rows),
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "resize PTY: %v", err)
	}
	select {
	case sess.vtermCh <- vtermMsg{resize: &vtermResize{int(req.Cols), int(req.Rows)}}:
	default:
	}
	sess.writeAuditResize(req.Cols, req.Rows)
	if err := s.updateMetaDimensions(req.Cols, req.Rows); err != nil {
		log.Printf("egg: update terminal metadata: %v", err)
	}
	return &pb.ResizeResponse{}, nil
}

func (s *Server) Status(ctx context.Context, req *pb.StatusRequest) (*pb.StatusResponse, error) {
	s.mu.RLock()
	sess := s.session
	s.mu.RUnlock()
	if sess == nil {
		return nil, status.Error(codes.NotFound, "no session")
	}
	st := sess.replay.Stats()
	idleSec := int64(sess.idleDuration().Seconds())
	return &pb.StatusResponse{
		SessionId:      sess.ID,
		Agent:          sess.Agent,
		BufferBytes:    int64(st.BufSize),
		TotalWritten:   st.Written,
		TotalTrimmed:   st.Trimmed,
		Readers:        int32(st.Readers),
		UptimeSeconds:  int64(time.Since(sess.StartedAt).Seconds()),
		RenderedConfig: sess.RenderedConfig,
		IdleSeconds:    idleSec,
	}, nil
}

// Session implements the bidirectional PTY I/O stream.
func (s *Server) Session(stream pb.Egg_SessionServer) error {
	msg, err := stream.Recv()
	if err != nil {
		return err
	}

	s.mu.RLock()
	sess := s.session
	s.mu.RUnlock()
	if sess == nil {
		return status.Error(codes.NotFound, "no session")
	}

	sessionID := msg.SessionId
	var startOffset int64

	if msg.GetAttach() {
		// Full replay as first message, then cursor starts after snapshot.
		var snapshot []byte
		if sess.useVTE && sess.vterm != nil {
			fenceCh := make(chan vtermFenceResult, 1)
			select {
			case sess.vtermCh <- vtermMsg{fence: fenceCh}:
				select {
				case result := <-fenceCh:
					snapshot = result.Snapshot
					startOffset = result.Offset
				case <-sess.done:
					snapshot, startOffset = sess.replay.Snapshot()
				case <-time.After(5 * time.Second):
					log.Printf("egg: vterm fence timeout, falling back to replay")
					snapshot, startOffset = sess.replay.Snapshot()
				}
			case <-sess.done:
				snapshot, startOffset = sess.replay.Snapshot()
			case <-time.After(5 * time.Second):
				log.Printf("egg: vterm fence enqueue timeout, falling back to replay")
				snapshot, startOffset = sess.replay.Snapshot()
			}
		} else {
			snapshot, startOffset = sess.replay.Snapshot()
		}
		if err := stream.Send(&pb.SessionMsg{
			SessionId: sessionID,
			Payload:   &pb.SessionMsg_Output{Output: snapshot},
		}); err != nil {
			return err
		}
	} else {
		// Non-attach (initial session): start cursor at current position.
		_, startOffset = sess.replay.Snapshot()
	}

	// Register cursor so the buffer knows our position (enables backpressure).
	cursor := sess.replay.Register(startOffset)
	defer sess.replay.Unregister(cursor)

	// Output goroutine: cursor-based reads from the replay buffer.
	// Every byte arrives in exact PTY order — no channel, no drops.
	go func() {
		for {
			data, wait := sess.replay.ReadAfter(cursor)
			if data != nil {
				if err := stream.Send(&pb.SessionMsg{
					SessionId: sessionID,
					Payload:   &pb.SessionMsg_Output{Output: data},
				}); err != nil {
					return
				}
				continue
			}
			// No new data — wait for buffer write, process exit, or client disconnect.
			select {
			case <-wait:
			case <-sess.done:
				// Drain any remaining data after process exit.
				if data, _ := sess.replay.ReadAfter(cursor); data != nil {
					if err := stream.Send(&pb.SessionMsg{
						SessionId: sessionID,
						Payload:   &pb.SessionMsg_Output{Output: data},
					}); err != nil {
						return
					}
				}
				sess.mu.Lock()
				code := sess.exitCode
				sess.mu.Unlock()
				_ = stream.Send(&pb.SessionMsg{
					SessionId: sessionID,
					Payload:   &pb.SessionMsg_ExitCode{ExitCode: int32(code)},
				})
				return
			case <-stream.Context().Done():
				return
			}
		}
	}()

	// Read input from client.
	for {
		msg, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}

		switch p := msg.Payload.(type) {
		case *pb.SessionMsg_Input:
			sess.mu.Lock()
			sess.lastInput = time.Now()
			sess.mu.Unlock()
			if sess.auditor != nil {
				sess.auditor.Process(p.Input)
			}
			if _, err := sess.ptmx.Write(p.Input); err != nil {
				return status.Errorf(codes.Unavailable, "write PTY input: %v", err)
			}
		case *pb.SessionMsg_Resize:
			if err := validatePTYSize(p.Resize.Cols, p.Resize.Rows); err != nil {
				return status.Error(codes.InvalidArgument, err.Error())
			}
			if err := pty.Setsize(sess.ptmx, &pty.Winsize{
				Cols: uint16(p.Resize.Cols),
				Rows: uint16(p.Resize.Rows),
			}); err != nil {
				return status.Errorf(codes.Internal, "resize PTY: %v", err)
			}
			select {
			case sess.vtermCh <- vtermMsg{resize: &vtermResize{int(p.Resize.Cols), int(p.Resize.Rows)}}:
			default:
			}
			sess.writeAuditResize(p.Resize.Cols, p.Resize.Rows)
			if err := s.updateMetaDimensions(p.Resize.Cols, p.Resize.Rows); err != nil {
				log.Printf("egg: update terminal metadata: %v", err)
			}
		case *pb.SessionMsg_Detach:
			if p.Detach {
				return nil
			}
		}
	}
}

func validatePTYSize(cols, rows uint32) error {
	if cols == 0 || rows == 0 || cols > 65535 || rows > 65535 {
		return fmt.Errorf("terminal size must be between 1 and 65535 columns and rows")
	}
	return nil
}

func formatCommand(command []string) string {
	quoted := make([]string, 0, len(command))
	for _, arg := range command {
		quoted = append(quoted, strconv.Quote(arg))
	}
	return strings.Join(quoted, " ")
}

// agentCommand returns the command and args for an interactive agent session.
func agentCommand(agentName string, dangerouslySkip bool, resumeSessionID string, extra ...string) (string, []string) {
	name, args, ok := agentpkg.InteractiveInvocation(agentName, dangerouslySkip, resumeSessionID, extra...)
	if !ok {
		return "", nil
	}
	return name, args
}

// sessionCommand resolves what a session actually executes. An explicit Command
// replaces the agent entirely and the session becomes an opaque command;
// AgentArgs extends the agent's own invocation and the session stays an agent
// session, keeping the agent's sandbox profile and resume semantics.
func sessionCommand(rc RunConfig) (string, []string) {
	if len(rc.Command) > 0 {
		return rc.Command[0], append([]string(nil), rc.Command[1:]...)
	}
	return agentCommand(rc.Agent, rc.DangerouslySkipPermissions, rc.ResumeSessionID, rc.AgentArgs...)
}

// Recovery interceptors
func recoveryUnary(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
	defer func() {
		if r := recover(); r != nil {
			stack := make([]byte, 16384)
			n := runtime.Stack(stack, false)
			log.Printf("egg: PANIC in %s: %v\n%s", info.FullMethod, r, stack[:n])
			err = status.Errorf(codes.Internal, "egg panic in %s: %v", info.FullMethod, r)
		}
	}()
	return handler(ctx, req)
}

func recoveryStream(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
	defer func() {
		if r := recover(); r != nil {
			stack := make([]byte, 16384)
			n := runtime.Stack(stack, false)
			log.Printf("egg: PANIC in %s: %v\n%s", info.FullMethod, r, stack[:n])
			err = status.Errorf(codes.Internal, "egg panic in %s: %v", info.FullMethod, r)
		}
	}()
	return handler(srv, ss)
}

// Auth interceptors
func (s *Server) authUnary(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if err := s.checkToken(ctx); err != nil {
		return nil, err
	}
	return handler(ctx, req)
}

func (s *Server) authStream(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	if err := s.checkToken(ss.Context()); err != nil {
		return err
	}
	return handler(srv, ss)
}

func (s *Server) checkToken(ctx context.Context) error {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "missing metadata")
	}
	tokens := md.Get("authorization")
	if len(tokens) == 0 || tokens[0] != s.token {
		return status.Error(codes.Unauthenticated, "invalid token")
	}
	return nil
}

// updateMetaDimensions rewrites egg.meta with updated cols/rows values.
func (s *Server) updateMetaDimensions(cols, rows uint32) error {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	metaPath := filepath.Join(s.dir, "egg.meta")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "cols=") {
			lines[i] = fmt.Sprintf("cols=%d", cols)
		} else if strings.HasPrefix(line, "rows=") {
			lines[i] = fmt.Sprintf("rows=%d", rows)
		}
	}
	return atomicWritePrivate(metaPath, []byte(strings.Join(lines, "\n")))
}

// installRoot returns the top-level directory under home for a binary path.
// e.g., ~/.bun/install/global/.../bin -> ~/.bun
//       ~/.local/bin               -> ~/.local

// startupWatchdog logs diagnostic info if no PTY output within 15 seconds.
func (s *Server) startupWatchdog(sess *Session) {
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()

	select {
	case <-sess.done:
		return
	case <-timer.C:
	}

	// Check if we got any output
	if len(sess.replay.Bytes()) > 0 {
		return // got output, all good
	}

	log.Printf("egg: WATCHDOG: no output from pid %d after 15s — dumping diagnostics", sess.PID)

	// Is the process still alive?
	if sess.cmd != nil && sess.cmd.Process != nil {
		if err := sess.cmd.Process.Signal(syscall.Signal(0)); err != nil {
			log.Printf("egg: WATCHDOG: process %d is DEAD: %v", sess.PID, err)
		} else {
			log.Printf("egg: WATCHDOG: process %d is ALIVE but producing no output", sess.PID)
		}
	}

	// Dump process tree under our PID
	if out, err := exec.Command("ps", "-o", "pid,ppid,stat,wchan,command", "-p", strconv.Itoa(sess.PID)).CombinedOutput(); err == nil {
		log.Printf("egg: WATCHDOG: ps output:\n%s", string(out))
	}

	// On macOS, check for sandbox denials in the unified log
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("log", "show", "--predicate",
			"eventMessage contains \"deny\" AND process == \"sandbox-exec\"",
			"--last", "30s", "--style", "compact").CombinedOutput(); err == nil {
			lines := strings.TrimSpace(string(out))
			if lines != "" && !strings.HasPrefix(lines, "Filtering the log data") {
				log.Printf("egg: WATCHDOG: sandbox denials:\n%s", lines)
			} else {
				log.Printf("egg: WATCHDOG: no sandbox denials in last 30s")
			}
		}
	}

	// Also try to find child processes (sandbox-exec spawns the real process)
	if out, err := exec.Command("pgrep", "-P", strconv.Itoa(sess.PID)).CombinedOutput(); err == nil {
		childPids := strings.TrimSpace(string(out))
		if childPids != "" {
			log.Printf("egg: WATCHDOG: child PIDs of %d: %s", sess.PID, childPids)
			for _, cpid := range strings.Fields(childPids) {
				if out2, err := exec.Command("ps", "-o", "pid,stat,wchan,command", "-p", cpid).CombinedOutput(); err == nil {
					log.Printf("egg: WATCHDOG: child %s:\n%s", cpid, string(out2))
				}
			}
		}
	}

	// Second watchdog at 30s with lsof
	timer2 := time.NewTimer(15 * time.Second)
	defer timer2.Stop()
	select {
	case <-sess.done:
		return
	case <-timer2.C:
	}

	if len(sess.replay.Bytes()) > 0 {
		return
	}

	log.Printf("egg: WATCHDOG: still no output at 30s, checking open files")
	if out, err := exec.Command("lsof", "-p", strconv.Itoa(sess.PID)).CombinedOutput(); err == nil {
		// Just log first 50 lines to avoid spam
		lines := strings.Split(string(out), "\n")
		if len(lines) > 50 {
			lines = lines[:50]
		}
		log.Printf("egg: WATCHDOG: lsof (first 50 lines):\n%s", strings.Join(lines, "\n"))
	}
}

// idleDuration returns how long since the last PTY I/O (input or output).
// If no I/O has occurred, returns time since session start.
func (sess *Session) idleDuration() time.Duration {
	sess.mu.Lock()
	lastIO := sess.lastOutput
	if sess.lastInput.After(lastIO) {
		lastIO = sess.lastInput
	}
	sess.mu.Unlock()
	if lastIO.IsZero() {
		return time.Since(sess.StartedAt)
	}
	return time.Since(lastIO)
}

// idleWatchdog terminates the egg if no PTY I/O for idleTimeout.
// Safety net: if the wing dies, the egg can still self-terminate.
func (s *Server) idleWatchdog(sess *Session) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-sess.done:
			return
		case <-ticker.C:
		}
		idle := sess.idleDuration()
		if idle > sess.idleTimeout {
			log.Printf("egg: idle timeout (%s idle, limit %s) — terminating", idle.Round(time.Second), sess.idleTimeout)
			if sess.cmd != nil && sess.cmd.Process != nil {
				if err := sess.cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
					log.Printf("egg: signal idle session: %v", err)
				}
				select {
				case <-sess.done:
					return
				case <-time.After(5 * time.Second):
					if err := sess.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
						log.Printf("egg: kill idle session: %v", err)
					}
				}
			}
			// Wait for normal cleanup path (cmd.Wait -> close(done) -> gRPC stop)
			<-sess.done
			return
		}
	}
}

// networkSummaryFromDomains returns a short description of the network config.
func networkSummaryFromDomains(domains []string) string {
	if len(domains) == 0 {
		return "none"
	}
	for _, d := range domains {
		if d == "*" {
			return "*"
		}
	}
	return strings.Join(domains, ",")
}

func installRoot(binDir, home string) string {
	rel, err := filepath.Rel(home, binDir)
	if err != nil {
		return binDir
	}
	parts := strings.SplitN(rel, string(filepath.Separator), 2)
	return filepath.Join(home, parts[0])
}
