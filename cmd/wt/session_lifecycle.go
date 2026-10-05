package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
	"github.com/spf13/cobra"
)

func (s *localMCPServer) resolveOwnedLifecycleSession(ref string) (eggclient.LocalSession, error) {
	session, err := eggclient.ResolveLifecycleSession(s.cfg, ref)
	if err != nil {
		return eggclient.LocalSession{}, err
	}
	if !s.ownsSession(session) || (s.enforcePathBounds && (len(s.allowedPaths) == 0 || !wingpolicy.IsUnderPaths(wingpolicy.CanonicalSessionPath(session.CWD), s.allowedPaths))) {
		return eggclient.LocalSession{}, errors.New("session not found or not owned by caller")
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
	view, err := eggclient.LifecycleViewForSession(s.cfg, session, 0, 1)
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
	view, err := eggclient.LifecycleViewForSession(s.cfg, session, args.AfterCursor, args.Limit)
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
	view, matched, err := eggclient.WaitSessionLifecycle(waitCtx, s.cfg, session, args.AfterCursor, args.State)
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
		view, err := eggclient.ReadSessionLifecycleView(cmd.Context(), cfg, args[0], 0, 1)
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
		view, err := eggclient.ReadSessionLifecycleView(cmd.Context(), cfg, args[0], after, limit)
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
		session, err := eggclient.ResolveLifecycleSession(cfg, args[0])
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
		defer cancel()
		view, matched, err := eggclient.WaitSessionLifecycle(ctx, cfg, session, after, state)
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
