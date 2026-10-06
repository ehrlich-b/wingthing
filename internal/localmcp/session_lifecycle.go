package localmcp

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
)

func (s *Server) resolveOwnedLifecycleSession(ref string) (eggclient.LocalSession, error) {
	session, err := eggclient.ResolveOwnedLifecycleSession(s.Cfg, ref, s.ownsSession)
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

func LifecycleResult(view egg.SessionView) map[string]any {
	return map[string]any{"session": view.SessionID, "lifecycle": view}
}

func (s *Server) toolSessionStatus(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
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
	view, err := eggclient.LifecycleViewForSession(s.Cfg, session, 0, 1)
	if err != nil {
		return nil, err
	}
	view.Events = []egg.SessionEvent{}
	view.Cursor = view.HeadCursor
	view.HasMore = false
	return LifecycleResult(view), nil
}

func (s *Server) toolSessionRead(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
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
	view, err := eggclient.LifecycleViewForSession(s.Cfg, session, args.AfterCursor, args.Limit)
	if err != nil {
		return nil, err
	}
	result := LifecycleResult(view)
	s.addSessionContinuation(result, view)
	return result, nil
}

type sessionWaitArgs struct {
	Session        string  `json:"session"`
	AfterCursor    int64   `json:"after_cursor"`
	State          string  `json:"state"`
	TimeoutSeconds float64 `json:"timeout_seconds"`
}

func (s *Server) toolSessionWait(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
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
	view, matched, err := eggclient.WaitSessionLifecycle(waitCtx, s.Cfg, session, args.AfterCursor, args.State)
	if err != nil {
		return nil, err
	}
	result := LifecycleResult(view)
	result["matched"] = matched
	result["timed_out"] = !matched
	return result, nil
}
