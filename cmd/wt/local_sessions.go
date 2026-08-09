package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"golang.org/x/term"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const sessionNameFile = "session.name"

type localSession struct {
	ID           string `json:"id"`
	Name         string `json:"name,omitempty"`
	Kind         string `json:"kind"`
	Agent        string `json:"agent,omitempty"`
	Command      string `json:"command,omitempty"`
	CWD          string `json:"cwd,omitempty"`
	PID          int    `json:"pid"`
	Readers      int32  `json:"readers"`
	UptimeSecs   int64  `json:"uptime_seconds"`
	IdleSecs     int64  `json:"idle_seconds"`
	BufferBytes  int64  `json:"buffer_bytes"`
	TotalWritten int64  `json:"total_written"`
}

func discoverActiveSessions(ctx context.Context, cfg *config.Config) ([]localSession, error) {
	sessions, err := discoverSessionRefs(cfg)
	if err != nil {
		return nil, err
	}
	for i := range sessions {
		session := &sessions[i]
		dir := filepath.Join(cfg.Dir, "eggs", session.ID)
		ec, dialErr := egg.Dial(filepath.Join(dir, "egg.sock"), filepath.Join(dir, "egg.token"))
		if dialErr == nil {
			statusCtx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
			st, statusErr := ec.Status(statusCtx)
			cancel()
			_ = ec.Close()
			if statusErr == nil {
				session.Readers = st.Readers
				session.UptimeSecs = st.UptimeSeconds
				session.IdleSecs = st.IdleSeconds
				session.BufferBytes = st.BufferBytes
				session.TotalWritten = st.TotalWritten
				if session.Agent == "" {
					session.Agent = st.Agent
				}
			}
		}
	}
	sortLocalSessions(sessions)
	return sessions, nil
}

// discoverSessionRefs reads only process and metadata files. Resolution stays
// fast even with many sessions; status RPCs are reserved for list displays.
func discoverSessionRefs(cfg *config.Config) ([]localSession, error) {
	eggsDir := filepath.Join(cfg.Dir, "eggs")
	entries, err := os.ReadDir(eggsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read sessions: %w", err)
	}

	sessions := make([]localSession, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sessionID := entry.Name()
		dir := filepath.Join(eggsDir, sessionID)
		pid, alive := readAliveEggPID(dir)
		if !alive {
			continue
		}

		meta := readEggMetaValues(dir)
		s := localSession{
			ID:      sessionID,
			Name:    readSessionName(dir),
			Kind:    meta["kind"],
			Agent:   meta["agent"],
			Command: meta["command"],
			CWD:     meta["cwd"],
			PID:     pid,
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

func sortLocalSessions(sessions []localSession) {
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

func readAliveEggPID(dir string) (int, bool) {
	data, err := os.ReadFile(filepath.Join(dir, "egg.pid"))
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, false
	}
	proc, err := os.FindProcess(pid)
	if err != nil || proc.Signal(syscall.Signal(0)) != nil {
		cleanEggDir(dir)
		return 0, false
	}
	return pid, true
}

func readEggMetaValues(dir string) map[string]string {
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

func readSessionName(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, sessionNameFile))
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(string(data))
	if validateSessionName(name) != nil {
		return ""
	}
	return name
}

func writeSessionName(dir, name string) error {
	if err := validateSessionName(name); err != nil {
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

func validateSessionName(name string) error {
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

func ensureSessionNameAvailable(cfg *config.Config, name, exceptID string) error {
	if name == "" {
		return nil
	}
	sessions, err := discoverSessionRefs(cfg)
	if err != nil {
		return err
	}
	for _, session := range sessions {
		if session.ID != exceptID && (session.Name == name || session.ID == name) {
			return fmt.Errorf("session name %q is already in use by %s", name, session.ID)
		}
	}
	return nil
}

func resolveActiveSession(ctx context.Context, cfg *config.Config, ref string) (localSession, error) {
	if err := validateSessionName(ref); err != nil {
		return localSession{}, err
	}
	sessions, err := discoverSessionRefs(cfg)
	if err != nil {
		return localSession{}, err
	}
	for _, session := range sessions {
		if session.ID == ref {
			return session, nil
		}
	}
	for _, session := range sessions {
		if session.Name == ref {
			return session, nil
		}
	}
	var matches []localSession
	for _, session := range sessions {
		if strings.HasPrefix(session.ID, ref) {
			matches = append(matches, session)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return localSession{}, fmt.Errorf("session reference %q is ambiguous", ref)
	}
	return localSession{}, fmt.Errorf("session %q not found; run 'wt attach' to list active sessions", ref)
}

func printActiveSessions(ctx context.Context, cfg *config.Config, jsonOutput bool) error {
	sessions, err := discoverActiveSessions(ctx, cfg)
	if err != nil {
		return err
	}
	if jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(sessions)
	}
	if len(sessions) == 0 {
		fmt.Println("no active sessions")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tID\tKIND\tPROCESS\tREADERS\tUPTIME\tIDLE\tCWD")
	for _, session := range sessions {
		name := session.Name
		if name == "" {
			name = "-"
		}
		process := session.Agent
		if process == "" {
			process = session.Command
		}
		if process == "" {
			process = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\n",
			name, session.ID, session.Kind, process, session.Readers,
			humanDuration(time.Duration(session.UptimeSecs)*time.Second),
			humanDuration(time.Duration(session.IdleSecs)*time.Second),
			shortenPath(session.CWD),
		)
	}
	return w.Flush()
}

func selectActiveSession(ctx context.Context, cfg *config.Config) (localSession, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return localSession{}, errors.New("interactive selection requires a terminal; pass a session ID or name")
	}
	sessions, err := discoverActiveSessions(ctx, cfg)
	if err != nil {
		return localSession{}, err
	}
	if len(sessions) == 0 {
		return localSession{}, errors.New("no active sessions")
	}
	if len(sessions) == 1 {
		return sessions[0], nil
	}

	fmt.Fprintln(os.Stderr, "Active sessions:")
	for i, session := range sessions {
		name := session.Name
		if name == "" {
			name = session.ID
		}
		process := session.Agent
		if process == "" {
			process = session.Command
		}
		fmt.Fprintf(os.Stderr, "  %d) %-20s %-8s %s  %s\n", i+1, name, session.Kind, shortenPath(session.CWD), process)
	}
	fmt.Fprintf(os.Stderr, "Attach [1-%d]: ", len(sessions))
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return localSession{}, fmt.Errorf("read selection: %w", err)
	}
	selection, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || selection < 1 || selection > len(sessions) {
		return localSession{}, fmt.Errorf("invalid selection %q", strings.TrimSpace(line))
	}
	return sessions[selection-1], nil
}

func openLocalEgg(ctx context.Context, cfg *config.Config, ref string) (localSession, *egg.Client, error) {
	session, err := resolveActiveSession(ctx, cfg, ref)
	if err != nil {
		return localSession{}, nil, err
	}
	dir := filepath.Join(cfg.Dir, "eggs", session.ID)
	ec, err := egg.Dial(filepath.Join(dir, "egg.sock"), filepath.Join(dir, "egg.token"))
	if err != nil {
		return localSession{}, nil, fmt.Errorf("connect to session %s: %w", session.ID, err)
	}
	return session, ec, nil
}

func readSessionSnapshot(ctx context.Context, cfg *config.Config, ref string) (localSession, []byte, error) {
	session, ec, err := openLocalEgg(ctx, cfg, ref)
	if err != nil {
		return localSession{}, nil, err
	}
	defer ec.Close()
	stream, err := ec.AttachSession(ctx, session.ID)
	if err != nil {
		return localSession{}, nil, fmt.Errorf("read session %s: %w", session.ID, err)
	}
	msg, err := stream.Recv()
	if err != nil {
		return localSession{}, nil, fmt.Errorf("read session %s: %w", session.ID, err)
	}
	_ = stream.Send(&pb.SessionMsg{SessionId: session.ID, Payload: &pb.SessionMsg_Detach{Detach: true}})
	_ = stream.CloseSend()
	payload, ok := msg.Payload.(*pb.SessionMsg_Output)
	if !ok {
		return localSession{}, nil, fmt.Errorf("read session %s: expected terminal snapshot", session.ID)
	}
	return session, payload.Output, nil
}

func sendSessionBytes(ctx context.Context, cfg *config.Config, ref string, input []byte) (localSession, error) {
	session, ec, err := openLocalEgg(ctx, cfg, ref)
	if err != nil {
		return localSession{}, err
	}
	defer ec.Close()
	stream, err := ec.AttachSession(ctx, session.ID)
	if err != nil {
		return localSession{}, fmt.Errorf("send to session %s: %w", session.ID, err)
	}
	// Drain the initial snapshot before sending. This keeps the bidirectional
	// stream moving even when a terminal has accumulated a large replay.
	if _, err := stream.Recv(); err != nil {
		return localSession{}, fmt.Errorf("send to session %s: %w", session.ID, err)
	}
	if err := stream.Send(&pb.SessionMsg{SessionId: session.ID, Payload: &pb.SessionMsg_Input{Input: input}}); err != nil {
		return localSession{}, fmt.Errorf("send to session %s: %w", session.ID, err)
	}
	_ = stream.Send(&pb.SessionMsg{SessionId: session.ID, Payload: &pb.SessionMsg_Detach{Detach: true}})
	_ = stream.CloseSend()
	return session, nil
}

func waitForSessionText(ctx context.Context, cfg *config.Config, ref, needle string) (localSession, error) {
	session, ec, err := openLocalEgg(ctx, cfg, ref)
	if err != nil {
		return localSession{}, err
	}
	defer ec.Close()
	stream, err := ec.AttachSession(ctx, session.ID)
	if err != nil {
		return localSession{}, fmt.Errorf("wait for session %s: %w", session.ID, err)
	}

	window := make([]byte, 0, 64*1024)
	for {
		msg, recvErr := stream.Recv()
		if recvErr != nil {
			if errors.Is(recvErr, io.EOF) || status.Code(recvErr) == codes.Canceled || status.Code(recvErr) == codes.DeadlineExceeded {
				if ctx.Err() != nil {
					return localSession{}, ctx.Err()
				}
				return localSession{}, fmt.Errorf("session %s closed before producing %q", session.ID, needle)
			}
			return localSession{}, fmt.Errorf("wait for session %s: %w", session.ID, recvErr)
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
			return localSession{}, fmt.Errorf("session %s exited before producing %q", session.ID, needle)
		}
	}
}
