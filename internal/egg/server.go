package egg

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
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
	syncEnd   = []byte("\x1b[?2026l")  // end of synchronized update frame (Claude, Codex)
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
}

// Session holds a single PTY process and its state.
type Session struct {
	ID             string
	PID            int
	Agent          string
	CWD            string
	Network        string // summary: "none", "*", or comma-separated domains
	RenderedConfig string // effective egg config as YAML (after merge/resolve)
	Cols           uint32
	Rows           uint32
	StartedAt      time.Time
	ptmx           *os.File
	replay         *replayBuffer
	vterm   *VTerm        // server-side VTE — only accessed by runVTermLoop goroutine
	vtermCh chan vtermMsg // async vterm processing channel
	useVTE  bool         // when true, attach sends VTerm snapshot instead of replay buffer
	sb      sandbox.Sandbox
	cmd     *exec.Cmd
	mu      sync.Mutex
	done           chan struct{} // closed when process exits
	exitCode       int
	debug          bool
	audit          bool
	auditor        *inputAuditor // nil when audit disabled
	auditWriter    *gzip.Writer  // nil when audit disabled or after PTY exit
	auditFile      *os.File     // underlying file for audit flush
	auditStart     time.Time     // start time for audit timestamps
	auditLastMS    uint64        // last frame timestamp for delta encoding
	auditFrames    int           // frame count since last flush
	auditMu        sync.Mutex   // protects auditWriter/auditLastMS/auditFrames
}

// RunConfig holds everything needed to start a single egg session.
type RunConfig struct {
	Agent   string
	CWD     string
	Shell   string
	FS      []string          // "rw:./", "deny:~/.ssh"
	Network []string          // domain list
	Env     map[string]string
	Rows    uint32
	Cols    uint32

	DangerouslySkipPermissions bool
	CPULimit                   time.Duration
	MemLimit                   uint64
	MaxFDs                     uint32
	Debug                      bool
	Audit                      bool
	VTE                        bool   // use VTerm snapshot for reconnect instead of replay buffer
	RenderedConfig             string // effective egg config as YAML (after merge/resolve)
	UserHome                   string // per-user home directory (relay sessions only)
}

// replayBuffer is an append-only (bounded) log of PTY output.
// Readers use cursor-based reads (ReadAfter) instead of subscriber channels,
// guaranteeing every byte arrives in exact PTY order with no drops.
// readerCursor tracks an active reader's position in the buffer.
type readerCursor struct {
	offset int64
}

type replayBuffer struct {
	mu            sync.Mutex
	buf           []byte
	trimmed       int64          // total bytes ever trimmed from front
	written       int64          // total bytes ever written
	notify        chan struct{}   // closed+replaced on Write to wake readers
	advanced      chan struct{}   // closed+replaced when a reader advances (unblocks writer)
	readers       []*readerCursor
	trimPreamble  []byte         // mode sequences to re-inject after trim
	cursorRow     int            // last known absolute cursor row (1-based)
	cursorCol     int            // last known absolute cursor col (1-based)
}

type replayStats struct {
	BufSize  int
	Written  int64
	Trimmed  int64
	Readers  int
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
	for {
		r.mu.Lock()
		// Track cursor position from incoming data before appending.
		trackCursorPos(p, &r.cursorRow, &r.cursorCol)
		r.buf = append(r.buf, p...)
		r.written += int64(len(p))

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
		params := data[start : i]
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
		return minOffset + idx + len(syncEnd)
	}

	// Try erase-line + column-reset (Cursor).
	if idx := bytes.Index(window, eraseLine); idx >= 0 {
		return minOffset + idx
	}

	// Fall back to nearest CRLF.
	if idx := bytes.Index(window, []byte("\r\n")); idx >= 0 {
		return minOffset + idx + 2
	}

	return minOffset
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

// RunSession is the core lifecycle: create sandbox, start agent in PTY, serve gRPC, exit when done.
func (s *Server) RunSession(ctx context.Context, rc RunConfig) error {
	os.Setenv("GOTRACEBACK", "all")

	name, args := agentCommand(rc.Agent, rc.DangerouslySkipPermissions, rc.Shell)
	if name == "" {
		return fmt.Errorf("unsupported agent: %s", rc.Agent)
	}

	binPath, err := exec.LookPath(name)
	if err != nil {
		return fmt.Errorf("agent %q not found: %v", name, err)
	}
	// Resolve symlinks so the real binary path works inside namespaces
	// (e.g. ~/.local/bin/claude -> ~/.claude/bin/claude)
	if resolved, err := filepath.EvalSymlinks(binPath); err == nil {
		binPath = resolved
	}

	// Build environment: always use rc.Env (caller filtered via BuildEnvMap).
	// Merge agent profile required vars + essentials from host env if missing.
	profile := Profile(rc.Agent)
	envMap := make(map[string]string, len(rc.Env))
	for k, v := range rc.Env {
		envMap[k] = v
	}
	// Merge required env vars from agent profile
	for _, k := range profile.EnvVars {
		if _, ok := envMap[k]; !ok {
			if v := os.Getenv(k); v != "" {
				envMap[k] = v
			}
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
	// Per-user home override for relay sessions
	if rc.UserHome != "" {
		envMap["HOME"] = rc.UserHome
	}

	// Snapshot agent config before session so we can restore on exit
	configSnap := SnapshotAgentConfig(rc.Agent)

	// Merge domains: user config + agent profile (dedup)
	mergedDomains := mergeDomains(rc.Network, profile.Domains)
	netNeed := sandbox.NetworkNeedFromDomains(mergedDomains)

	// Start domain-filtering proxy if we have specific domains (not "*" or empty)
	var domainProxy *sandbox.DomainProxy
	if netNeed == sandbox.NetworkHTTPS && len(mergedDomains) > 0 {
		var err2 error
		domainProxy, err2 = sandbox.StartProxy(mergedDomains)
		if err2 != nil {
			log.Printf("egg: warning: domain proxy failed, falling back to port-level filtering: %v", err2)
		} else {
			proxyURL := fmt.Sprintf("http://localhost:%d", domainProxy.Port())
			envMap["HTTPS_PROXY"] = proxyURL
			envMap["HTTP_PROXY"] = proxyURL
			envMap["NODE_USE_ENV_PROXY"] = "1" // node 22.18+ native proxy support
		}
	}

	// Browser open interception shim
	shimDir := filepath.Join(s.dir, "shims")
	os.MkdirAll(shimDir, 0755)
	shimScript := "#!/bin/sh\necho \"$1\" >> \"$WT_SESSION_DIR/browser-requests\"\n"
	shimPath := filepath.Join(shimDir, "wt-browser")
	os.WriteFile(shimPath, []byte(shimScript), 0755)
	os.Symlink("wt-browser", filepath.Join(shimDir, "open"))
	os.Symlink("wt-browser", filepath.Join(shimDir, "xdg-open"))
	envMap["BROWSER"] = "wt-browser"
	envMap["WT_SESSION_DIR"] = s.dir
	if path, ok := envMap["PATH"]; ok {
		envMap["PATH"] = shimDir + ":" + path
	} else {
		envMap["PATH"] = shimDir + ":/usr/bin:/bin"
	}

	// Build envSlice AFTER proxy setup so HTTPS_PROXY etc. are included
	var envSlice []string
	for k, v := range envMap {
		envSlice = append(envSlice, k+"="+v)
	}

	// Build sandbox and command
	var sb sandbox.Sandbox
	var cmd *exec.Cmd

	hasSandbox := len(rc.FS) > 0 || netNeed < sandbox.NetworkFull
	if hasSandbox {
		home, _ := os.UserHomeDir()
		mounts, deny, denyWrite := ParseFSRules(rc.FS, home)

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
				mounts = append(mounts, sandbox.Mount{Source: abs, Target: abs, UseRegex: true})
			}
			for _, d := range profile.WriteDirs {
				abs := filepath.Join(profileHome, d)
				mounts = append(mounts, sandbox.Mount{Source: abs, Target: abs})
			}
		}

		proxyPort := 0
		if domainProxy != nil {
			proxyPort = domainProxy.Port()
		}

		sbCfg := sandbox.Config{
			Mounts:      mounts,
			Deny:        deny,
			DenyWrite:   denyWrite,
			NetworkNeed: netNeed,
			Domains:     mergedDomains,
			ProxyPort:   proxyPort,
			CPULimit:    rc.CPULimit,
			MemLimit:    rc.MemLimit,
			MaxFDs:      rc.MaxFDs,
		}

		sb, err = sandbox.New(sbCfg)
		if err != nil {
			return fmt.Errorf("sandbox: %v", err)
		}
		cmd, err = sb.Exec(context.Background(), binPath, args)
		if err != nil {
			sb.Destroy()
			return fmt.Errorf("sandbox exec: %v", err)
		}
		cmd.Env = envSlice
		if rc.CWD != "" {
			cmd.Dir = rc.CWD
		}
	} else {
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

	size := &pty.Winsize{Cols: uint16(rc.Cols), Rows: uint16(rc.Rows)}
	ptmx, err := pty.StartWithSize(cmd, size)
	if err != nil {
		if sb != nil {
			sb.Destroy()
		}
		return fmt.Errorf("start pty: %v", err)
	}

	// Apply post-start hooks (rlimits on Linux)
	if sb != nil {
		if psErr := sb.PostStart(cmd.Process.Pid); psErr != nil {
			log.Printf("egg: sandbox post-start warning: %v", psErr)
		}
	}

	sessionID := filepath.Base(s.dir)
	networkSummary := networkSummaryFromDomains(mergedDomains)
	sess := &Session{
		ID:             sessionID,
		PID:            cmd.Process.Pid,
		Agent:          rc.Agent,
		CWD:            rc.CWD,
		Network:        networkSummary,
		RenderedConfig: rc.RenderedConfig,
		StartedAt:      time.Now(),
		Cols:           rc.Cols,
		Rows:           rc.Rows,
		ptmx:           ptmx,
		replay:  newReplayBuffer(rc.Agent),
		vterm:   NewVTerm(int(rc.Cols), int(rc.Rows)),
		vtermCh: make(chan vtermMsg, 256),
		useVTE:  rc.VTE,
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

	log.Printf("egg: session %s agent=%s pid=%d network=%s fs=%d", sessionID, rc.Agent, cmd.Process.Pid, networkSummary, len(rc.FS))

	// VTerm async processing goroutine — must start before readPTY
	go runVTermLoop(sess.vterm, sess.vtermCh, sess.done)

	// Read PTY output (with first-byte timing)
	go s.readPTY(sess)

	// Watchdog: if no PTY output within 15s, dump diagnostic info
	go s.startupWatchdog(sess)

	// Write files and start gRPC server
	sockPath := filepath.Join(s.dir, "egg.sock")
	tokenPath := filepath.Join(s.dir, "egg.token")
	pidPath := filepath.Join(s.dir, "egg.pid")

	os.Remove(sockPath)
	lis, err := net.Listen("unix", sockPath)
	if err != nil {
		ptmx.Close()
		if sb != nil {
			sb.Destroy()
		}
		return fmt.Errorf("listen: %w", err)
	}
	s.listener = lis
	os.Chmod(sockPath, 0600)

	if err := os.WriteFile(tokenPath, []byte(s.token), 0600); err != nil {
		lis.Close()
		return fmt.Errorf("write token: %w", err)
	}
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0644); err != nil {
		lis.Close()
		return fmt.Errorf("write pid: %w", err)
	}

	// Write session metadata so the wing can read it on reclaim
	metaPath := filepath.Join(s.dir, "egg.meta")
	metaContent := fmt.Sprintf("agent=%s\ncwd=%s\nnetwork=%s\ncols=%d\nrows=%d\n", rc.Agent, rc.CWD, networkSummary, rc.Cols, rc.Rows)
	if err := os.WriteFile(metaPath, []byte(metaContent), 0644); err != nil {
		log.Printf("egg: warning: write meta: %v", err)
	}

	s.grpcServer = grpc.NewServer(
		grpc.ChainUnaryInterceptor(recoveryUnary, s.authUnary),
		grpc.ChainStreamInterceptor(recoveryStream, s.authStream),
	)
	pb.RegisterEggServer(s.grpcServer, s)

	log.Printf("egg: serving on %s (pid %d)", sockPath, os.Getpid())

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

		ptmx.Close()
		configSnap.Restore()
		if domainProxy != nil {
			domainProxy.Close()
		}
		if sess.sb != nil {
			sess.sb.Destroy()
		}

		// Give gRPC a moment to send exit_code, then stop
		time.Sleep(500 * time.Millisecond)
		s.grpcServer.GracefulStop()
	}()

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.grpcServer.Serve(lis)
	}()

	select {
	case <-ctx.Done():
		s.shutdown()
		return ctx.Err()
	case err := <-errCh:
		s.cleanup()
		return err
	}
}

func (s *Server) shutdown() {
	log.Println("egg: shutting down...")
	s.mu.RLock()
	sess := s.session
	s.mu.RUnlock()
	if sess != nil && sess.cmd != nil && sess.cmd.Process != nil {
		sess.cmd.Process.Signal(syscall.SIGTERM)
		time.Sleep(3 * time.Second)
		if err := sess.cmd.Process.Signal(syscall.Signal(0)); err == nil {
			sess.cmd.Process.Kill()
		}
		if sess.sb != nil {
			sess.sb.Destroy()
		}
	}
	s.grpcServer.GracefulStop()
	s.cleanup()
}

func (s *Server) cleanup() {
	os.Remove(filepath.Join(s.dir, "egg.sock"))
	os.Remove(filepath.Join(s.dir, "egg.token"))
	s.mu.RLock()
	sess := s.session
	s.mu.RUnlock()
	if sess == nil || !sess.audit {
		// Non-audit: remove the entire session directory
		os.RemoveAll(s.dir)
	}
	// Audit sessions keep egg.meta, egg.pid, audit.pty.gz, audit.log
}

func (s *Server) readPTY(sess *Session) {
	var debugFile *os.File
	if sess.debug {
		path := "/tmp/wt-pty-" + sess.Agent + "-" + sess.ID + ".bin"
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			log.Printf("egg: debug: cannot open %s: %v", path, err)
		} else {
			debugFile = f
			defer debugFile.Close()
			log.Printf("egg: debug: writing raw PTY output to %s", path)
		}
	}

	// PTY stream audit: gzipped V2 varint delta format for replay
	if sess.audit {
		path := filepath.Join(s.dir, "audit.pty.gz")
		f, err := os.Create(path)
		if err != nil {
			log.Printf("egg: audit pty recording failed: %v", err)
		} else {
			gw := gzip.NewWriter(f)
			gw.Write([]byte("WTA2")) // V2 header
			writeVarint(gw, uint64(sess.Cols))
			writeVarint(gw, uint64(sess.Rows))
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
				gw.Close()
				f.Close()
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
			sess.replay.Write(data)
			offset := sess.replay.WritePosition()
			select {
			case sess.vtermCh <- vtermMsg{data: data, offset: offset}:
			default:
			}
			if debugFile != nil {
				debugFile.Write(data)
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
	writeVarint(sess.auditWriter, delta)
	writeVarint(sess.auditWriter, frameType)
	writeVarint(sess.auditWriter, uint64(len(data)))
	sess.auditWriter.Write(data)
	sess.auditFrames++
	if sess.auditFrames%100 == 0 {
		sess.auditWriter.Flush()
		if sess.auditFile != nil {
			sess.auditFile.Sync()
		}
	}
}

// writeAuditResize writes a resize event to the audit stream.
func (sess *Session) writeAuditResize(cols, rows uint32) {
	var buf [20]byte
	n := binary.PutUvarint(buf[:], uint64(cols))
	n += binary.PutUvarint(buf[n:], uint64(rows))
	sess.writeAuditFrame(1, buf[:n])
}

// writeVarint writes a protobuf-style unsigned varint.
func writeVarint(w io.Writer, v uint64) {
	var buf [10]byte
	n := binary.PutUvarint(buf[:], v)
	w.Write(buf[:n])
}

// Kill terminates the session.
func (s *Server) Kill(ctx context.Context, req *pb.KillRequest) (*pb.KillResponse, error) {
	s.mu.RLock()
	sess := s.session
	s.mu.RUnlock()
	if sess == nil {
		return nil, status.Error(codes.NotFound, "no session")
	}
	if sess.cmd != nil && sess.cmd.Process != nil {
		sess.cmd.Process.Signal(syscall.SIGTERM)
	}
	return &pb.KillResponse{}, nil
}

// Resize changes the terminal dimensions.
func (s *Server) Resize(ctx context.Context, req *pb.ResizeRequest) (*pb.ResizeResponse, error) {
	s.mu.RLock()
	sess := s.session
	s.mu.RUnlock()
	if sess == nil {
		return nil, status.Error(codes.NotFound, "no session")
	}
	pty.Setsize(sess.ptmx, &pty.Winsize{
		Cols: uint16(req.Cols),
		Rows: uint16(req.Rows),
	})
	select {
	case sess.vtermCh <- vtermMsg{resize: &vtermResize{int(req.Cols), int(req.Rows)}}:
	default:
	}
	sess.writeAuditResize(req.Cols, req.Rows)
	s.updateMetaDimensions(req.Cols, req.Rows)
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
	return &pb.StatusResponse{
		SessionId:      sess.ID,
		Agent:          sess.Agent,
		BufferBytes:    int64(st.BufSize),
		TotalWritten:   st.Written,
		TotalTrimmed:   st.Trimmed,
		Readers:        int32(st.Readers),
		UptimeSeconds:  int64(time.Since(sess.StartedAt).Seconds()),
		RenderedConfig: sess.RenderedConfig,
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
					stream.Send(&pb.SessionMsg{
						SessionId: sessionID,
						Payload:   &pb.SessionMsg_Output{Output: data},
					})
				}
				sess.mu.Lock()
				code := sess.exitCode
				sess.mu.Unlock()
				stream.Send(&pb.SessionMsg{
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
			if sess.auditor != nil {
				sess.auditor.Process(p.Input)
			}
			sess.ptmx.Write(p.Input)
		case *pb.SessionMsg_Resize:
			pty.Setsize(sess.ptmx, &pty.Winsize{
				Cols: uint16(p.Resize.Cols),
				Rows: uint16(p.Resize.Rows),
			})
			select {
			case sess.vtermCh <- vtermMsg{resize: &vtermResize{int(p.Resize.Cols), int(p.Resize.Rows)}}:
			default:
			}
			sess.writeAuditResize(p.Resize.Cols, p.Resize.Rows)
			s.updateMetaDimensions(p.Resize.Cols, p.Resize.Rows)
		case *pb.SessionMsg_Detach:
			if p.Detach {
				return nil
			}
		}
	}
}

// agentCommand returns the command and args for an interactive terminal session.
func agentCommand(agentName string, dangerouslySkip bool, shell string) (string, []string) {
	var name string
	var args []string

	switch agentName {
	case "claude":
		name = "claude"
		if dangerouslySkip {
			args = append(args, "--dangerously-skip-permissions")
		}
	case "codex":
		name = "codex"
		if dangerouslySkip {
			args = append(args, "--full-auto")
		}
	case "cursor":
		name = "agent"
	case "ollama":
		name = "ollama"
		args = []string{"run", "llama3.2"}
	default:
		return "", nil
	}

	return name, args
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
func (s *Server) updateMetaDimensions(cols, rows uint32) {
	metaPath := filepath.Join(s.dir, "egg.meta")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return
	}
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "cols=") {
			lines[i] = fmt.Sprintf("cols=%d", cols)
		} else if strings.HasPrefix(line, "rows=") {
			lines[i] = fmt.Sprintf("rows=%d", rows)
		}
	}
	os.WriteFile(metaPath, []byte(strings.Join(lines, "\n")), 0644)
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
			fmt.Sprintf("eventMessage contains \"deny\" AND process == \"sandbox-exec\""),
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

// mergeDomains deduplicates and merges two domain lists.
func mergeDomains(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	var out []string
	for _, d := range a {
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	for _, d := range b {
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
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
