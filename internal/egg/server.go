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
	"strconv"
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
	"gopkg.in/yaml.v3"
)

const (
	ringSize    = 50 * 1024 // 50KB replay buffer per session
	idleTimeout = 5 * time.Minute
)

// Server implements the Egg gRPC service — holds PTY fds across wing restarts.
type Server struct {
	pb.UnimplementedEggServer

	socketPath string
	tokenPath  string
	pidPath    string
	token      string
	version    string
	sessions   map[string]*Session
	mu         sync.RWMutex
	grpcServer *grpc.Server
	listener   net.Listener
	config     *EggConfig // current active config
	configMu   sync.RWMutex
}

// Session holds a single PTY process and its state.
type Session struct {
	ID         string
	PID        int
	Agent      string
	CWD        string
	Isolation  string
	ConfigYAML string // egg config snapshot at spawn time
	StartedAt  time.Time
	ptmx       *os.File
	ring       *ringBuffer
	sb         sandbox.Sandbox
	cmd        *exec.Cmd
	subs       map[string]chan []byte // stream subscribers
	mu         sync.Mutex
	done       chan struct{} // closed when process exits
	exitCode   int
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

// NewServer creates a new egg server. The dir should be ~/.wingthing.
// The version string is recorded so wings can detect version mismatches.
func NewServer(dir, version string) (*Server, error) {
	// Generate random token for auth
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("generate token: %w", err)
	}
	token := fmt.Sprintf("%x", tokenBytes)

	s := &Server{
		socketPath: filepath.Join(dir, "egg.sock"),
		tokenPath:  filepath.Join(dir, "egg.token"),
		pidPath:    filepath.Join(dir, "egg.pid"),
		token:      token,
		version:    version,
		sessions:   make(map[string]*Session),
		config:     DefaultEggConfig(),
	}
	return s, nil
}

// Version returns the egg's binary version.
func (s *Server) Version(ctx context.Context, req *pb.VersionRequest) (*pb.VersionResponse, error) {
	return &pb.VersionResponse{Version: s.version}, nil
}

// GetConfig returns the current active egg config as YAML.
func (s *Server) GetConfig(ctx context.Context, req *pb.GetConfigRequest) (*pb.GetConfigResponse, error) {
	s.configMu.RLock()
	cfg := s.config
	s.configMu.RUnlock()
	yamlStr, err := cfg.YAML()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "serialize config: %v", err)
	}
	return &pb.GetConfigResponse{Yaml: yamlStr}, nil
}

// SetConfig updates the active egg config from YAML. New sessions use the updated config.
func (s *Server) SetConfig(ctx context.Context, req *pb.SetConfigRequest) (*pb.SetConfigResponse, error) {
	cfg, err := parseEggConfigYAML(req.Yaml)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "parse config: %v", err)
	}
	s.configMu.Lock()
	s.config = cfg
	s.configMu.Unlock()
	log.Printf("egg: config updated (isolation=%s)", cfg.Isolation)
	return &pb.SetConfigResponse{Ok: true}, nil
}

// Run starts the gRPC server. Blocks until context is cancelled or idle timeout.
func (s *Server) Run(ctx context.Context) error {
	// Clean stale socket
	os.Remove(s.socketPath)

	lis, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	s.listener = lis
	os.Chmod(s.socketPath, 0600)

	// Write token file
	if err := os.WriteFile(s.tokenPath, []byte(s.token), 0600); err != nil {
		lis.Close()
		return fmt.Errorf("write token: %w", err)
	}

	// Write PID file
	if err := os.WriteFile(s.pidPath, []byte(strconv.Itoa(os.Getpid())), 0644); err != nil {
		lis.Close()
		return fmt.Errorf("write pid: %w", err)
	}

	s.grpcServer = grpc.NewServer(
		grpc.UnaryInterceptor(s.authUnary),
		grpc.StreamInterceptor(s.authStream),
	)
	pb.RegisterEggServer(s.grpcServer, s)

	log.Printf("egg listening on %s (pid %d)", s.socketPath, os.Getpid())

	// Idle timer: shutdown if no sessions and no connections for idleTimeout
	idleCtx, idleCancel := context.WithCancel(ctx)
	defer idleCancel()
	go s.idleWatcher(idleCtx)

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
	log.Println("egg shutting down...")
	s.mu.Lock()
	sessions := make([]*Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	s.mu.Unlock()

	// SIGTERM all children
	for _, sess := range sessions {
		if sess.cmd != nil && sess.cmd.Process != nil {
			log.Printf("egg: terminating session %s (pid %d)", sess.ID, sess.PID)
			sess.cmd.Process.Signal(syscall.SIGTERM)
		}
	}

	// Wait then force-kill
	if len(sessions) > 0 {
		time.Sleep(5 * time.Second)
		for _, sess := range sessions {
			if sess.cmd != nil && sess.cmd.Process != nil {
				if err := sess.cmd.Process.Signal(syscall.Signal(0)); err == nil {
					log.Printf("egg: force-killing session %s (pid %d)", sess.ID, sess.PID)
					sess.cmd.Process.Kill()
				}
			}
			if sess.sb != nil {
				sess.sb.Destroy()
			}
		}
	}

	s.grpcServer.GracefulStop()
	s.cleanup()
}

func (s *Server) cleanup() {
	os.Remove(s.socketPath)
	os.Remove(s.pidPath)
	os.Remove(s.tokenPath)
}

func (s *Server) idleWatcher(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	idleSince := time.Now()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.mu.RLock()
			n := len(s.sessions)
			s.mu.RUnlock()
			if n > 0 {
				idleSince = time.Now()
				continue
			}
			if time.Since(idleSince) > idleTimeout {
				log.Println("egg: idle timeout, shutting down")
				s.grpcServer.Stop()
				return
			}
		}
	}
}

// Auth interceptors — validate token from metadata.
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

// agentCommand returns the command and args for an interactive terminal session.
// When dangerouslySkip is true, adds the appropriate flag to skip permission prompts.
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

// Spawn creates a new PTY session.
// The critical fix: sandbox is created BEFORE the process starts, and the sandbox's
// Exec() produces the cmd that gets passed to pty.StartWithSize().
func (s *Server) Spawn(ctx context.Context, req *pb.SpawnRequest) (*pb.SpawnResponse, error) {
	// Safety gate: refuse dangerously_skip_permissions without real sandbox
	if req.DangerouslySkipPermissions && (req.Isolation == "" || req.Isolation == "privileged") {
		return nil, status.Errorf(codes.InvalidArgument, "dangerously_skip_permissions requires sandbox isolation (not privileged)")
	}

	name, args := agentCommand(req.Agent, req.DangerouslySkipPermissions, req.Shell)
	if name == "" {
		return nil, status.Errorf(codes.InvalidArgument, "unsupported agent: %s", req.Agent)
	}

	binPath, err := exec.LookPath(name)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "agent %q not found: %v", name, err)
	}

	// Build environment
	var envSlice []string
	if len(req.Env) > 0 {
		for k, v := range req.Env {
			envSlice = append(envSlice, k+"="+v)
		}
	} else {
		envSlice = os.Environ()
	}

	// Build sandbox config from request
	var sb sandbox.Sandbox
	var cmd *exec.Cmd

	if req.Isolation != "" && req.Isolation != "privileged" {
		mounts := protoMountsToSandbox(req.Mounts)
		sbCfg := sandbox.Config{
			Isolation: sandbox.ParseLevel(req.Isolation),
			Mounts:    mounts,
			Deny:      req.Deny,
		}
		if req.TimeoutSeconds > 0 {
			sbCfg.Timeout = time.Duration(req.TimeoutSeconds) * time.Second
		}
		if req.ResourceLimits != nil {
			if req.ResourceLimits.CpuSeconds > 0 {
				sbCfg.CPULimit = time.Duration(req.ResourceLimits.CpuSeconds) * time.Second
			}
			if req.ResourceLimits.MemoryBytes > 0 {
				sbCfg.MemLimit = req.ResourceLimits.MemoryBytes
			}
			if req.ResourceLimits.MaxFds > 0 {
				sbCfg.MaxFDs = req.ResourceLimits.MaxFds
			}
		}

		var sbErr error
		sb, sbErr = sandbox.New(sbCfg)
		if sbErr != nil {
			log.Printf("egg: sandbox init failed, running unsandboxed: %v", sbErr)
			cmd = exec.CommandContext(context.Background(), binPath, args...)
			cmd.Env = envSlice
			if req.Cwd != "" {
				cmd.Dir = req.Cwd
			}
		} else {
			cmd, err = sb.Exec(context.Background(), binPath, args)
			if err != nil {
				sb.Destroy()
				return nil, status.Errorf(codes.Internal, "sandbox exec: %v", err)
			}
			// Sandbox sets its own env; merge request env on top
			cmd.Env = envSlice
			if req.Cwd != "" {
				cmd.Dir = req.Cwd
			}
		}
	} else {
		cmd = exec.CommandContext(context.Background(), binPath, args...)
		cmd.Env = envSlice
		if req.Cwd != "" {
			cmd.Dir = req.Cwd
		}
	}

	// Graceful termination
	cmd.Cancel = func() error {
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = 5 * time.Second

	size := &pty.Winsize{
		Cols: uint16(req.Cols),
		Rows: uint16(req.Rows),
	}
	ptmx, err := pty.StartWithSize(cmd, size)
	if err != nil {
		if sb != nil {
			sb.Destroy()
		}
		return nil, status.Errorf(codes.Internal, "start pty: %v", err)
	}

	// Apply post-start hooks (e.g. rlimits on Linux)
	if sb != nil {
		if psErr := sb.PostStart(cmd.Process.Pid); psErr != nil {
			log.Printf("egg: sandbox post-start warning: %v", psErr)
		}
	}

	sess := &Session{
		ID:         req.SessionId,
		PID:        cmd.Process.Pid,
		Agent:      req.Agent,
		CWD:        req.Cwd,
		Isolation:  req.Isolation,
		ConfigYAML: req.ConfigYaml,
		StartedAt:  time.Now(),
		ptmx:       ptmx,
		ring:       newRingBuffer(ringSize),
		sb:         sb,
		cmd:        cmd,
		subs:       make(map[string]chan []byte),
		done:       make(chan struct{}),
	}

	s.mu.Lock()
	s.sessions[req.SessionId] = sess
	s.mu.Unlock()

	log.Printf("egg: spawned session %s agent=%s pid=%d isolation=%s",
		req.SessionId, req.Agent, cmd.Process.Pid, req.Isolation)

	// Output reader goroutine — reads PTY, writes to ring buffer, fans out to subscribers
	go s.readPTY(sess)

	// Wait for process exit
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
		log.Printf("egg: session %s exited with code %d", sess.ID, exitCode)

		// Clean up
		ptmx.Close()
		if sess.sb != nil {
			sess.sb.Destroy()
		}
		s.mu.Lock()
		delete(s.sessions, sess.ID)
		s.mu.Unlock()
	}()

	return &pb.SpawnResponse{
		SessionId: req.SessionId,
		Pid:       int32(cmd.Process.Pid),
	}, nil
}

// protoMountsToSandbox converts proto Mount messages to sandbox.Mount.
func protoMountsToSandbox(mounts []*pb.Mount) []sandbox.Mount {
	var out []sandbox.Mount
	for _, m := range mounts {
		out = append(out, sandbox.Mount{
			Source:   m.Source,
			Target:   m.Target,
			ReadOnly: m.ReadOnly,
		})
	}
	return out
}

func (s *Server) readPTY(sess *Session) {
	buf := make([]byte, 4096)
	for {
		n, err := sess.ptmx.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])
			sess.ring.Write(data)

			// Fan out to all subscribers
			sess.mu.Lock()
			for _, ch := range sess.subs {
				select {
				case ch <- data:
				default:
					// Subscriber can't keep up, drop
				}
			}
			sess.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// List returns all active sessions.
func (s *Server) List(ctx context.Context, req *pb.ListRequest) (*pb.ListResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var infos []*pb.SessionInfo
	for _, sess := range s.sessions {
		infos = append(infos, &pb.SessionInfo{
			SessionId:  sess.ID,
			Pid:        int32(sess.PID),
			Agent:      sess.Agent,
			Cwd:        sess.CWD,
			StartedAt:  sess.StartedAt.Format(time.RFC3339),
			Isolation:  sess.Isolation,
			ConfigYaml: sess.ConfigYAML,
		})
	}
	return &pb.ListResponse{Sessions: infos}, nil
}

// Kill terminates a session.
func (s *Server) Kill(ctx context.Context, req *pb.KillRequest) (*pb.KillResponse, error) {
	s.mu.RLock()
	sess, ok := s.sessions[req.SessionId]
	s.mu.RUnlock()
	if !ok {
		return nil, status.Errorf(codes.NotFound, "session %s not found", req.SessionId)
	}

	if sess.cmd != nil && sess.cmd.Process != nil {
		sess.cmd.Process.Signal(syscall.SIGTERM)
	}
	return &pb.KillResponse{}, nil
}

// Resize changes the terminal dimensions of a session.
func (s *Server) Resize(ctx context.Context, req *pb.ResizeRequest) (*pb.ResizeResponse, error) {
	s.mu.RLock()
	sess, ok := s.sessions[req.SessionId]
	s.mu.RUnlock()
	if !ok {
		return nil, status.Errorf(codes.NotFound, "session %s not found", req.SessionId)
	}

	pty.Setsize(sess.ptmx, &pty.Winsize{
		Cols: uint16(req.Cols),
		Rows: uint16(req.Rows),
	})
	return &pb.ResizeResponse{}, nil
}

// Session implements the bidirectional PTY I/O stream.
func (s *Server) Session(stream pb.Egg_SessionServer) error {
	// First message must identify the session
	msg, err := stream.Recv()
	if err != nil {
		return err
	}

	sessionID := msg.SessionId
	s.mu.RLock()
	sess, ok := s.sessions[sessionID]
	s.mu.RUnlock()
	if !ok {
		return status.Errorf(codes.NotFound, "session %s not found", sessionID)
	}

	// Create a subscriber channel for this stream
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

	// Send output and exit_code to wing
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

	// Read input from wing, write to PTY
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

// parseEggConfigYAML parses an EggConfig from a YAML string.
func parseEggConfigYAML(yamlStr string) (*EggConfig, error) {
	var cfg EggConfig
	if err := yaml.Unmarshal([]byte(yamlStr), &cfg); err != nil {
		return nil, err
	}
	if cfg.Isolation == "" {
		cfg.Isolation = "network"
	}
	return &cfg, nil
}
