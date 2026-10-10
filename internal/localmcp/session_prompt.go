package localmcp

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/ehrlich-b/wingthing/internal/egg"
)

func (s *Server) toolSessionPrompt(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		Session        string  `json:"session"`
		RequestID      string  `json:"request_id"`
		Input          string  `json:"input"`
		TimeoutSeconds float64 `json:"timeout_seconds"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if args.TimeoutSeconds == 0 {
		args.TimeoutSeconds = 15
	}
	if args.TimeoutSeconds < 0.1 || args.TimeoutSeconds > 60 {
		return nil, errors.New("timeout_seconds must be between 0.1 and 60")
	}
	session, err := s.resolveOwnedLifecycleSession(args.Session)
	if err != nil {
		return nil, err
	}
	var result egg.SessionPromptResult
	_, result, err = s.Sessions.Prompt(ctx, s.sessionAuthority(), session.ID, args.RequestID, args.Input, durationSeconds(args.TimeoutSeconds), "mcp:"+s.clientActor())
	if err != nil {
		return nil, err
	}
	return map[string]any{"session": session.ID, "receipt": result}, nil
}
