package eggclient

import (
	"context"

	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"time"

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/procinfo"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	sessionNameFile      = "session.name"
	sessionPrincipalFile = "session.principal"
	// Interactive TUIs such as Claude Code distinguish pasted text from a
	// separately pressed Enter. Writing both in one PTY frame can leave the
	// text in the editor instead of submitting it. Preserve the terminal-send
	// contract by separating Enter from a non-empty text write.
	sessionEnterDelay = 50 * time.Millisecond
)

var ErrSessionNameInUse = errors.New("session name is already in use")

type LocalSession struct {
	ConversationLink
	ID              string   `json:"id"`
	Name            string   `json:"name,omitempty"`
	Principal       string   `json:"principal,omitempty"`
	Kind            string   `json:"kind"`
	Agent           string   `json:"agent,omitempty"`
	Status          string   `json:"status"`
	Command         string   `json:"command,omitempty"`
	CWD             string   `json:"cwd,omitempty"`
	Isolation       string   `json:"isolation,omitempty"`
	IsolationReason string   `json:"isolation_reason,omitempty"`
	LegacySessions  []string `json:"legacy_sessions,omitempty"`
	PID             int      `json:"pid"`
	Readers         int32    `json:"readers"`
	WriterID        string   `json:"writer_id,omitempty"`
	WriterOwner     string   `json:"writer_owner,omitempty"`
	InputEpoch      uint64   `json:"input_epoch,omitempty"`
	UptimeSecs      int64    `json:"uptime_seconds"`
	IdleSecs        int64    `json:"idle_seconds"`
	BufferBytes     int64    `json:"buffer_bytes"`
	TotalWritten    int64    `json:"total_written"`
}

func DiscoverActiveSessions(ctx context.Context, cfg *config.Config) ([]LocalSession, error) {
	sessions, err := DiscoverSessionRefs(cfg)
	if err != nil {
		return nil, err
	}
	for i := range sessions {
		session := &sessions[i]
		session.Status = "unknown"
		dir := filepath.Join(cfg.Dir, "eggs", session.ID)
		ec, dialErr := egg.Dial(filepath.Join(dir, "egg.sock"), filepath.Join(dir, "egg.token"))
		if dialErr == nil {
			statusCtx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
			st, statusErr := ec.Status(statusCtx)
			cancel()
			_ = ec.Close()
			if statusErr == nil {
				session.Readers = st.Readers
				session.WriterID, session.WriterOwner, session.InputEpoch = st.WriterId, st.WriterOwner, st.InputEpoch
				session.UptimeSecs = st.UptimeSeconds
				session.IdleSecs = st.IdleSeconds
				session.BufferBytes = st.BufferBytes
				session.TotalWritten = st.TotalWritten
				if session.Agent == "" {
					session.Agent = st.Agent
				}
			}
		}
		if view, err := tryLifecycleViewForSession(cfg, *session); err == nil {
			session.Status = view.Status
		}
	}
	sortLocalSessions(sessions)
	return sessions, nil
}

// discoverSessionRefs reads only process and metadata files. Resolution stays
// fast even with many sessions; status RPCs are reserved for list displays.
func DiscoverSessionRefs(cfg *config.Config) ([]LocalSession, error) {
	eggsDir := filepath.Join(cfg.Dir, "eggs")
	entries, err := os.ReadDir(eggsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read sessions: %w", err)
	}

	sessions := make([]LocalSession, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sessionID := entry.Name()
		dir := filepath.Join(eggsDir, sessionID)
		pid, alive := ReadAliveEggPID(dir)
		if !alive {
			continue
		}

		meta := ReadEggMetaValues(dir)
		s := LocalSession{
			ConversationLink: SessionConversationLink(cfg, sessionID),
			ID:               sessionID,
			Name:             ReadSessionName(dir),
			Principal:        ReadSessionPrincipal(dir),
			Kind:             meta["kind"],
			Agent:            meta["agent"],
			Command:          meta["command"],
			CWD:              meta["cwd"],
			Isolation:        meta["isolation"],
			PID:              pid,
		}
		if isolation := egg.ReadLegacyIsolation(dir); isolation != nil {
			s.Isolation = "degraded"
			s.IsolationReason = isolation.Reason
			s.LegacySessions = isolation.LegacySessions
		}
		if s.Kind == "" {
			if s.Agent != "" {
				s.Kind = "agent"
			} else {
				s.Kind = "command"
			}
		}

		sessions = append(sessions, s)
	}
	sortLocalSessions(sessions)
	return sessions, nil
}

func ReadSessionPrincipal(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, sessionPrincipalFile))
	if err != nil {
		return ""
	}
	principal := strings.TrimSpace(string(data))
	if ValidateSessionName(principal) != nil {
		return ""
	}
	return principal
}

func WriteSessionPrincipal(dir, principal string) error {
	if principal == "" {
		return nil
	}
	if err := ValidateSessionName(principal); err != nil {
		return fmt.Errorf("invalid MCP client name: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, sessionPrincipalFile), []byte(principal+"\n"), 0600); err != nil {
		return fmt.Errorf("write session principal: %w", err)
	}
	return nil
}

func sortLocalSessions(sessions []LocalSession) {
	sort.Slice(sessions, func(i, j int) bool {
		left, right := sessions[i], sessions[j]
		if left.Name != "" && right.Name == "" {
			return true
		}
		if left.Name == "" && right.Name != "" {
			return false
		}
		if left.Name != right.Name {
			return left.Name < right.Name
		}
		return left.ID < right.ID
	})
}

func ReadAliveEggPID(dir string) (int, bool) {
	data, err := os.ReadFile(filepath.Join(dir, "egg.pid"))
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, false
	}
	if !procinfo.OwnedProcessIsAlive(pid) {
		return 0, false
	}
	if config.Channel() == "preview" && !previewEggProcessMatches(pid, filepath.Base(dir)) {
		return 0, false
	}
	return pid, true
}

func ReadEggMetaValues(dir string) map[string]string {
	values := make(map[string]string)
	data, err := os.ReadFile(filepath.Join(dir, "egg.meta"))
	if err != nil {
		return values
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[key] = value
		}
	}
	return values
}

func ReadSessionName(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, sessionNameFile))
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(string(data))
	if ValidateSessionName(name) != nil {
		return ""
	}
	return name
}

func WriteSessionName(dir, name string) error {
	if err := ValidateSessionName(name); err != nil {
		return err
	}
	if name == "" {
		if err := os.Remove(filepath.Join(dir, sessionNameFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove session name: %w", err)
		}
		return nil
	}
	if err := os.WriteFile(filepath.Join(dir, sessionNameFile), []byte(name+"\n"), 0600); err != nil {
		return fmt.Errorf("write session name: %w", err)
	}
	return nil
}

func ValidateSessionName(name string) error {
	if name == "" {
		return nil
	}
	if len(name) > 64 {
		return errors.New("session name must be at most 64 characters")
	}
	for i, r := range name {
		valid := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.'
		if !valid || (i == 0 && (r == '-' || r == '.')) {
			return fmt.Errorf("invalid session name %q; use letters, numbers, '.', '_', and '-'", name)
		}
	}
	return nil
}

// acquireSessionNameLock serializes name assignments across every entry point
// and process. Hold it through the availability check and durable write; a new
// egg must also become discoverable before releasing its label claim.
func AcquireSessionNameLock(cfg *config.Config) (*os.File, error) {
	eggsDir := filepath.Join(cfg.Dir, "eggs")
	if err := os.MkdirAll(eggsDir, 0o700); err != nil {
		return nil, fmt.Errorf("create eggs dir: %w", err)
	}
	lock, err := os.OpenFile(filepath.Join(eggsDir, ".session-name.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open session name lock: %w", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("lock session names: %w", err)
	}
	return lock, nil
}

func EnsureSessionNameAvailable(cfg *config.Config, name, exceptID string) error {
	if name == "" {
		return nil
	}
	sessions, err := DiscoverSessionRefs(cfg)
	if err != nil {
		return err
	}
	for _, session := range sessions {
		if session.ID != exceptID && (session.Name == name || session.ID == name) {
			return ErrSessionNameInUse
		}
	}
	return nil
}

func ResolveActiveSession(ctx context.Context, cfg *config.Config, ref string) (LocalSession, error) {
	return ResolveOwnedActiveSession(ctx, cfg, ref, nil)
}

func ResolveOwnedActiveSession(ctx context.Context, cfg *config.Config, ref string, owns func(LocalSession) bool) (LocalSession, error) {
	if err := ValidateSessionName(ref); err != nil {
		return LocalSession{}, err
	}
	sessions, err := DiscoverSessionRefs(cfg)
	if err != nil {
		return LocalSession{}, err
	}
	for _, session := range sessions {
		if session.ID == ref {
			if owns != nil && !owns(session) {
				return LocalSession{}, errors.New("session not found or not owned by caller")
			}
			return session, nil
		}
	}
	var names []LocalSession
	for _, session := range sessions {
		if session.Name == ref && (owns == nil || owns(session)) {
			names = append(names, session)
		}
	}
	if len(names) == 1 {
		return names[0], nil
	}
	if len(names) > 1 {
		return LocalSession{}, fmt.Errorf("session reference %q is ambiguous", ref)
	}
	var matches []LocalSession
	for _, session := range sessions {
		if strings.HasPrefix(session.ID, ref) && (owns == nil || owns(session)) {
			matches = append(matches, session)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return LocalSession{}, fmt.Errorf("session reference %q is ambiguous", ref)
	}
	return LocalSession{}, fmt.Errorf("session %q not found; run 'wt attach' to list active sessions", ref)
}

func OpenLocalEgg(ctx context.Context, cfg *config.Config, ref string) (LocalSession, *egg.Client, error) {
	session, err := ResolveActiveSession(ctx, cfg, ref)
	if err != nil {
		return LocalSession{}, nil, err
	}
	dir := filepath.Join(cfg.Dir, "eggs", session.ID)
	ec, err := egg.Dial(filepath.Join(dir, "egg.sock"), filepath.Join(dir, "egg.token"))
	if err != nil {
		return LocalSession{}, nil, fmt.Errorf("connect to session %s: %w", session.ID, err)
	}
	return session, ec, nil
}

func ReadSessionSnapshot(ctx context.Context, cfg *config.Config, ref string) (LocalSession, []byte, error) {
	session, ec, err := OpenLocalEgg(ctx, cfg, ref)
	if err != nil {
		return LocalSession{}, nil, err
	}
	defer cmdutil.CloseWithLog("egg client", ec)
	stream, err := ec.AttachSessionWithOptions(ctx, session.ID, egg.AttachOptions{ReadOnly: true, Owner: "snapshot"})
	if err != nil {
		return LocalSession{}, nil, fmt.Errorf("read session %s: %w", session.ID, err)
	}
	msg, err := stream.Recv()
	if err != nil {
		return LocalSession{}, nil, fmt.Errorf("read session %s: %w", session.ID, err)
	}
	_ = stream.Send(&pb.SessionMsg{SessionId: session.ID, Payload: &pb.SessionMsg_Detach{Detach: true}})
	_ = stream.CloseSend()
	payload, ok := msg.Payload.(*pb.SessionMsg_Output)
	if !ok {
		return LocalSession{}, nil, fmt.Errorf("read session %s: expected terminal snapshot", session.ID)
	}
	return session, payload.Output, nil
}

func sessionInputChunks(input []byte, enter bool) [][]byte {
	chunks := make([][]byte, 0, 2)
	if len(input) > 0 {
		chunks = append(chunks, input)
	}
	if enter {
		chunks = append(chunks, []byte{'\r'})
	}
	return chunks
}

func SendSessionInput(ctx context.Context, cfg *config.Config, ref string, input []byte, enter bool) (LocalSession, error) {
	session, ec, err := OpenLocalEgg(ctx, cfg, ref)
	if err != nil {
		return LocalSession{}, err
	}
	defer cmdutil.CloseWithLog("egg client", ec)
	stream, err := ec.AttachSessionWithOptions(ctx, session.ID, egg.AttachOptions{Claim: true, Owner: "session-send"})
	if err != nil {
		return LocalSession{}, fmt.Errorf("send to session %s: %w", session.ID, err)
	}
	// Drain the initial snapshot before sending. This keeps the bidirectional
	// stream moving even when a terminal has accumulated a large replay.
	if _, err := stream.Recv(); err != nil {
		return LocalSession{}, fmt.Errorf("send to session %s: %w", session.ID, err)
	}
	chunks := sessionInputChunks(input, enter)
	for index, chunk := range chunks {
		if index > 0 && len(input) > 0 {
			timer := time.NewTimer(sessionEnterDelay)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return LocalSession{}, ctx.Err()
			case <-timer.C:
			}
		}
		if err := stream.Send(&pb.SessionMsg{SessionId: session.ID, Payload: &pb.SessionMsg_Input{Input: chunk}}); err != nil {
			return LocalSession{}, fmt.Errorf("send to session %s: %w", session.ID, err)
		}
	}
	if err := stream.Send(&pb.SessionMsg{SessionId: session.ID, Payload: &pb.SessionMsg_Detach{Detach: true}}); err != nil {
		return LocalSession{}, fmt.Errorf("detach after send: %w", err)
	}
	if err := stream.CloseSend(); err != nil {
		return LocalSession{}, err
	}
	// Send only queues bytes. Wait for the server to process the ordered input
	// and detach before closing the connection and reporting delivery success.
	for {
		_, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return session, nil
		}
		if err != nil {
			return LocalSession{}, fmt.Errorf("confirm session input: %w", err)
		}
	}
}

func WaitForSessionText(ctx context.Context, cfg *config.Config, ref, needle string) (LocalSession, error) {
	session, ec, err := OpenLocalEgg(ctx, cfg, ref)
	if err != nil {
		return LocalSession{}, err
	}
	defer cmdutil.CloseWithLog("egg client", ec)
	stream, err := ec.AttachSessionWithOptions(ctx, session.ID, egg.AttachOptions{ReadOnly: true, Owner: "terminal-wait"})
	if err != nil {
		return LocalSession{}, fmt.Errorf("wait for session %s: %w", session.ID, err)
	}

	window := make([]byte, 0, 64*1024)
	for {
		msg, recvErr := stream.Recv()
		if recvErr != nil {
			if errors.Is(recvErr, io.EOF) || status.Code(recvErr) == codes.Canceled || status.Code(recvErr) == codes.DeadlineExceeded {
				if ctx.Err() != nil {
					return LocalSession{}, ctx.Err()
				}
				return LocalSession{}, fmt.Errorf("session %s closed before producing %q", session.ID, needle)
			}
			return LocalSession{}, fmt.Errorf("wait for session %s: %w", session.ID, recvErr)
		}
		switch payload := msg.Payload.(type) {
		case *pb.SessionMsg_Output:
			window = append(window, payload.Output...)
			if strings.Contains(string(window), needle) {
				_ = stream.Send(&pb.SessionMsg{SessionId: session.ID, Payload: &pb.SessionMsg_Detach{Detach: true}})
				return session, nil
			}
			if len(window) > 1024*1024 {
				window = append(window[:0], window[len(window)-512*1024:]...)
			}
		case *pb.SessionMsg_ExitCode:
			return LocalSession{}, fmt.Errorf("session %s exited before producing %q", session.ID, needle)
		}
	}
}
