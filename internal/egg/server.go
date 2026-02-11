package egg

import (
	"context"
	"crypto/rand"
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

const ringSize = 50 * 1024 // 50KB replay buffer

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
	ID        string
	PID       int
	Agent     string
	CWD       string
	Isolation string
	StartedAt time.Time
	ptmx      *os.File
	ring      *ringBuffer
	sb        sandbox.Sandbox
	cmd       *exec.Cmd
	subs      map[string]chan []byte // stream subscribers
	mu        sync.Mutex
	done      chan struct{} // closed when process exits
	exitCode  int
}

// RunConfig holds everything needed to start a single egg session.
type RunConfig struct {
	Agent     string
	CWD       string
	Isolation string
	Shell     string
	Mounts    []string          // "~/repos:rw" format
	Deny      []string
	Env       map[string]string
	Rows      uint32
	Cols      uint32

	DangerouslySkipPermissions bool
	CPULimit                   time.Duration
	MemLimit                   uint64
	MaxFDs                     uint32
}

type ringBuffer struct {
	mu   sync.Mutex
	buf  []byte
	size int
	pos  int
	full bool
}

func newRingBuffer(size int) *ringBuffer {
	return &ringBuffer{buf: make([]byte, size), size: size}
}

func (r *ringBuffer) Write(p []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, b := range p {
		r.buf[r.pos] = b
		r.pos = (r.pos + 1) % r.size
		if r.pos == 0 {
			r.full = true
		}
	}
}

func (r *ringBuffer) Bytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		return append([]byte(nil), r.buf[:r.pos]...)
	}
	result := make([]byte, r.size)
	copy(result, r.buf[r.pos:])
	copy(result[r.size-r.pos:], r.buf[:r.pos])
	return result
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

	if rc.DangerouslySkipPermissions && (rc.Isolation == "" || rc.Isolation == "privileged") {
		return fmt.Errorf("dangerously_skip_permissions requires sandbox isolation (not privileged)")
	}

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
	var envSlice []string
	for k, v := range envMap {
		envSlice = append(envSlice, k+"="+v)
	}

	// Build sandbox and command
	var sb sandbox.Sandbox
	var cmd *exec.Cmd

	if rc.Isolation != "" && rc.Isolation != "privileged" {
		home, _ := os.UserHomeDir()
		var mounts []sandbox.Mount
		for _, m := range rc.Mounts {
			source, readOnly := parseMount(m, home)
			mounts = append(mounts, sandbox.Mount{Source: source, Target: source, ReadOnly: readOnly})
		}
		var deny []string
		for _, d := range rc.Deny {
			deny = append(deny, expandTilde(d, home))
		}

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
		if home != "" && len(mounts) > 0 {
			for _, d := range profile.WriteRegex {
				abs := filepath.Join(home, d)
				mounts = append(mounts, sandbox.Mount{Source: abs, Target: abs, UseRegex: true})
			}
			for _, d := range profile.WriteDirs {
				abs := filepath.Join(home, d)
				mounts = append(mounts, sandbox.Mount{Source: abs, Target: abs})
			}
		}

		// Set network need from agent profile, but only when isolation
		// denies network (Strict/Standard). When isolation allows network,
		// the profile doesn't need to override anything.
		netNeed := sandbox.NetworkNone
		if sandbox.ParseLevel(rc.Isolation) < sandbox.Network {
			netNeed = profile.Network
		}

		sbCfg := sandbox.Config{
			Isolation:   sandbox.ParseLevel(rc.Isolation),
			Mounts:      mounts,
			Deny:        deny,
			NetworkNeed: netNeed,
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
	sess := &Session{
		ID:        sessionID,
		PID:       cmd.Process.Pid,
		Agent:     rc.Agent,
		CWD:       rc.CWD,
		Isolation: rc.Isolation,
		StartedAt: time.Now(),
		ptmx:      ptmx,
		ring:      newRingBuffer(ringSize),
		sb:        sb,
		cmd:       cmd,
		subs:      make(map[string]chan []byte),
		done:      make(chan struct{}),
	}

	s.mu.Lock()
	s.session = sess
	s.mu.Unlock()

	log.Printf("egg: session %s agent=%s pid=%d isolation=%s", sessionID, rc.Agent, cmd.Process.Pid, rc.Isolation)

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
	os.Remove(filepath.Join(s.dir, "egg.pid"))
	os.Remove(filepath.Join(s.dir, "egg.token"))
}

func (s *Server) readPTY(sess *Session) {
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
			sess.ring.Write(data)

			sess.mu.Lock()
			for _, ch := range sess.subs {
				select {
				case ch <- data:
				default:
				}
			}
			sess.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
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
	return &pb.ResizeResponse{}, nil
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
	subID := fmt.Sprintf("%p", stream)
	outputCh := make(chan []byte, 256)

	sess.mu.Lock()
	sess.subs[subID] = outputCh
	sess.mu.Unlock()

	defer func() {
		sess.mu.Lock()
		delete(sess.subs, subID)
		sess.mu.Unlock()
	}()

	// Handle attach — replay ring buffer
	if msg.GetAttach() {
		buffered := sess.ring.Bytes()
		if len(buffered) > 0 {
			if err := stream.Send(&pb.SessionMsg{
				SessionId: sessionID,
				Payload:   &pb.SessionMsg_Output{Output: buffered},
			}); err != nil {
				return err
			}
		}
	}

	// Send output and exit_code
	go func() {
		for {
			select {
			case data, ok := <-outputCh:
				if !ok {
					return
				}
				stream.Send(&pb.SessionMsg{
					SessionId: sessionID,
					Payload:   &pb.SessionMsg_Output{Output: data},
				})
			case <-sess.done:
				sess.mu.Lock()
				code := sess.exitCode
				sess.mu.Unlock()
				stream.Send(&pb.SessionMsg{
					SessionId: sessionID,
					Payload:   &pb.SessionMsg_ExitCode{ExitCode: int32(code)},
				})
				return
			}
		}
	}()

	// Read input from client
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
			sess.ptmx.Write(p.Input)
		case *pb.SessionMsg_Resize:
			pty.Setsize(sess.ptmx, &pty.Winsize{
				Cols: uint16(p.Resize.Cols),
				Rows: uint16(p.Resize.Rows),
			})
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
		if dangerouslySkip {
			args = append(args, "--dangerously-skip-permissions")
		}
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
	if len(sess.ring.Bytes()) > 0 {
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

	if len(sess.ring.Bytes()) > 0 {
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

func installRoot(binDir, home string) string {
	rel, err := filepath.Rel(home, binDir)
	if err != nil {
		return binDir
	}
	parts := strings.SplitN(rel, string(filepath.Separator), 2)
	return filepath.Join(home, parts[0])
}
