package localmcp

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/ehrlich-b/wingthing/internal/eggclient"
)

func (s *Server) ToolSessionPin(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		Session string `json:"session"`
		Pinned  *bool  `json:"pinned"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if args.Session == "" || args.Pinned == nil {
		return nil, errors.New("session and pinned are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	session, err := s.resolveOwnedLifecycleSession(args.Session)
	if err != nil {
		return nil, err
	}
	m, err := eggclient.SetSessionPinned(s.Cfg, session.ID, *args.Pinned)
	if err != nil {
		return nil, err
	}
	return map[string]any{"session": session.ID, "pinned": m.Pinned, "role": m.Role}, nil
}
