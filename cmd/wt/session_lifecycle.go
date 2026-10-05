package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
	"github.com/spf13/cobra"
)

// Lifecycle reads include archived sessions. Terminal input/attachment retains
// its existing live-only resolution. IDs and labels use the same exact-first
// ambiguity rules, without constructing a path from a caller-supplied string.
func resolveLifecycleSession(cfg *config.Config, ref string) (localSession, error) {
	if ref == "" {
		return localSession{}, errors.New("session is required")
	}
	if err := validateSessionName(ref); err != nil {
		return localSession{}, err
	}
	entries, err := os.ReadDir(filepath.Join(cfg.Dir, "eggs"))
	if err != nil {
		return localSession{}, err
	}
	var candidates []localSession
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(cfg.Dir, "eggs", entry.Name())
		meta := readEggMetaValues(dir)
		pid, _ := readAliveEggPID(dir)
		session := localSession{ID: entry.Name(), Name: readSessionName(dir), Principal: readSessionPrincipal(dir), Agent: meta["agent"], Kind: meta["kind"], CWD: meta["cwd"], PID: pid}
		if session.ID == ref {
			return session, nil
		}
		if session.Name == ref || strings.HasPrefix(session.ID, ref) {
			candidates = append(candidates, session)
		}
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	if len(candidates) > 1 {
		return localSession{}, errors.New("session reference is ambiguous; use its full ID")
	}
	return localSession{}, errors.New("session not found")
}

func lifecycleViewForSession(cfg *config.Config, session localSession, after int64, limit int) (egg.SessionView, error) {
	return readLifecycleViewForSession(cfg, session, after, limit, false)
}

func tryLifecycleViewForSession(cfg *config.Config, session localSession) (egg.SessionView, error) {
	return readLifecycleViewForSession(cfg, session, 0, 1, true)
}

func readLifecycleViewForSession(cfg *config.Config, session localSession, after int64, limit int, nonBlocking bool) (egg.SessionView, error) {
	dir := filepath.Join(cfg.Dir, "eggs", session.ID)
	meta := readEggMetaValues(dir)
	home, err := lifecycleProviderHome(cfg, meta["provider_home"])
	if err != nil {
		return egg.SessionView{}, err
	}
	_, alive := readAliveEggPID(dir)
	read := egg.ReadSessionLifecycle
	if nonBlocking {
		read = egg.TryReadSessionLifecycle
	}
	return read(dir, session.Agent, session.CWD, home, meta["provider_session_id"], alive, after, limit)
}

func lifecycleProviderHome(cfg *config.Config, recorded string) (string, error) {
	if config.Channel() == "preview" {
		home := effectiveSessionHome(cfg, EggIdentity{})
		if recorded != "" && wingpolicy.CanonicalSessionPath(recorded) != wingpolicy.CanonicalSessionPath(home) {
			return "", errors.New("preview session provider home does not belong to this preview state")
		}
		return home, nil
	}
	if recorded != "" {
		return recorded, nil
	}
	return os.UserHomeDir()
}

func readSessionLifecycleView(ctx context.Context, cfg *config.Config, ref string, after int64, limit int) (egg.SessionView, error) {
	if err := ctx.Err(); err != nil {
		return egg.SessionView{}, err
	}
	session, err := resolveLifecycleSession(cfg, ref)
	if err != nil {
		return egg.SessionView{}, err
	}
	return lifecycleViewForSession(cfg, session, after, limit)
}

func (s *localMCPServer) resolveOwnedLifecycleSession(ref string) (localSession, error) {
	session, err := resolveLifecycleSession(s.cfg, ref)
	if err != nil {
		return localSession{}, err
	}
	if !s.ownsSession(session) || (s.enforcePathBounds && (len(s.allowedPaths) == 0 || !wingpolicy.IsUnderPaths(wingpolicy.CanonicalSessionPath(session.CWD), s.allowedPaths))) {
		return localSession{}, errors.New("session not found or not owned by caller")
	}
	return session, nil
}

type sessionLifecycleArgs struct {
	Session     string `json:"session"`
	AfterCursor int64  `json:"after_cursor"`
	Limit       int    `json:"limit"`
}

func lifecycleResult(view egg.SessionView) map[string]any {
	return map[string]any{"session": view.SessionID, "lifecycle": view}
}

func (s *localMCPServer) toolSessionStatus(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		Session string `json:"session"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	session, err := s.resolveOwnedLifecycleSession(args.Session)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	view, err := lifecycleViewForSession(s.cfg, session, 0, 1)
	if err != nil {
		return nil, err
	}
	view.Events = []egg.SessionEvent{}
	view.Cursor = view.HeadCursor
	view.HasMore = false
	return lifecycleResult(view), nil
}

func (s *localMCPServer) toolSessionRead(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
	var args sessionLifecycleArgs
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if args.Limit == 0 {
		args.Limit = 50
	}
	session, err := s.resolveOwnedLifecycleSession(args.Session)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	view, err := lifecycleViewForSession(s.cfg, session, args.AfterCursor, args.Limit)
	if err != nil {
		return nil, err
	}
	result := lifecycleResult(view)
	s.addSessionContinuation(result, view)
	return result, nil
}

type sessionWaitArgs struct {
	Session        string  `json:"session"`
	AfterCursor    int64   `json:"after_cursor"`
	State          string  `json:"state"`
	TimeoutSeconds float64 `json:"timeout_seconds"`
}

func validateLifecycleWait(state string) error {
	switch state {
	case "", "ready", "starting", "working", "idle", "completed", "needs_input", "failed", "unknown":
		return nil
	}
	return errors.New("state must be ready, starting, working, idle, completed, needs_input, failed, or unknown")
}

func lifecycleWaitMatched(view egg.SessionView, after int64, state string) bool {
	if state == "" {
		return view.HeadCursor > after
	}
	if state == "ready" {
		return view.Ready
	}
	if view.State != state {
		return false
	}
	if after == 0 {
		return true
	}
	return view.StateCursor > after
}

func waitSessionLifecycle(ctx context.Context, cfg *config.Config, session localSession, after int64, state string) (egg.SessionView, bool, error) {
	if err := validateLifecycleWait(state); err != nil {
		return egg.SessionView{}, false, err
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var view egg.SessionView
	for {
		var err error
		view, err = lifecycleViewForSession(cfg, session, after, 200)
		if err != nil {
			return view, false, err
		}
		if lifecycleWaitMatched(view, after, state) {
			return view, true, nil
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return view, false, nil
			}
			return view, false, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *localMCPServer) toolSessionWait(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
	var args sessionWaitArgs
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if args.TimeoutSeconds == 0 {
		args.TimeoutSeconds = 30
	}
	if args.TimeoutSeconds < 0.1 || args.TimeoutSeconds > 3600 {
		return nil, errors.New("timeout_seconds must be between 0.1 and 3600")
	}
	session, err := s.resolveOwnedLifecycleSession(args.Session)
	if err != nil {
		return nil, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, durationSeconds(args.TimeoutSeconds))
	defer cancel()
	view, matched, err := waitSessionLifecycle(waitCtx, s.cfg, session, args.AfterCursor, args.State)
	if err != nil {
		return nil, err
	}
	result := lifecycleResult(view)
	result["matched"] = matched
	result["timed_out"] = !matched
	return result, nil
}

func sessionLifecycleCmd() *cobra.Command {
	var jsonFlag bool
	cmd := &cobra.Command{Use: "status <session>", Short: "Read native agent lifecycle (independent of terminal activity)", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		view, err := readSessionLifecycleView(cmd.Context(), cfg, args[0], 0, 1)
		if err != nil {
			return err
		}
		view.Events = []egg.SessionEvent{}
		view.Cursor = view.HeadCursor
		view.HasMore = false
		if jsonFlag {
			return writeSessionJSON(lifecycleResult(view))
		}
		fmt.Printf("%s: %s (%s), ready=%t, process_alive=%t\n", view.SessionID, view.State, view.StateSource, view.Ready, view.ProcessAlive)
		return nil
	}}
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "print machine-readable JSON")
	return cmd
}

func sessionTranscriptCmd() *cobra.Command {
	var jsonFlag bool
	var after int64
	var limit int
	cmd := &cobra.Command{Use: "transcript <session>", Short: "Read exact-session native conversation and lifecycle events", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		view, err := readSessionLifecycleView(cmd.Context(), cfg, args[0], after, limit)
		if err != nil {
			return err
		}
		if jsonFlag {
			return writeSessionJSON(lifecycleResult(view))
		}
		for _, e := range view.Events {
			fmt.Printf("%d %s %s %s\n", e.Sequence, e.Type, e.Role, e.Text)
		}
		return nil
	}}
	cmd.Flags().Int64Var(&after, "after-cursor", 0, "read events after this durable cursor")
	cmd.Flags().IntVar(&limit, "limit", 50, "maximum events (1-200)")
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "print machine-readable JSON")
	return cmd
}

func sessionAwaitCmd() *cobra.Command {
	var jsonFlag bool
	var after int64
	var state string
	var timeout time.Duration
	cmd := &cobra.Command{Use: "await <session>", Short: "Wait for a native lifecycle state or event", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if timeout < 100*time.Millisecond || timeout > time.Hour {
			return errors.New("timeout must be between 100ms and 1h")
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		session, err := resolveLifecycleSession(cfg, args[0])
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
		defer cancel()
		view, matched, err := waitSessionLifecycle(ctx, cfg, session, after, state)
		if err != nil {
			return err
		}
		result := lifecycleResult(view)
		result["matched"] = matched
		result["timed_out"] = !matched
		if jsonFlag {
			return writeSessionJSON(result)
		}
		fmt.Printf("%s: %s, matched=%t\n", view.SessionID, view.State, matched)
		return nil
	}}
	cmd.Flags().Int64Var(&after, "after-cursor", 0, "wait for evidence after this durable cursor")
	cmd.Flags().StringVar(&state, "state", "", "native state to wait for, or ready; omit to wait for any new event")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "maximum wait (100ms-1h)")
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "print machine-readable JSON")
	return cmd
}
