package localmcp

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/ehrlich-b/wingthing/internal/egg"
)

func (s *Server) ToolSessionReport(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		Session string             `json:"session"`
		Report  *egg.SessionReport `json:"report"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if args.Session == "" || args.Report == nil {
		return nil, errors.New("session and report are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	session, err := s.resolveOwnedLifecycleSession(args.Session)
	if err != nil {
		return nil, err
	}
	if args.Report.Shell != nil && args.Session != session.ID {
		return nil, errors.New("shell reports require the full immutable session ID")
	}
	event, err := egg.RecordSessionReport(filepath.Join(s.Cfg.Dir, "eggs", session.ID), *args.Report)
	if err != nil {
		return nil, err
	}
	return map[string]any{"session": session.ID, "event": event, "scheduling": "reported plan only; no timer or approval created"}, nil
}
