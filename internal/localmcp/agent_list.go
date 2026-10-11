package localmcp

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
)

func (s *Server) toolAgentList(ctx context.Context, args json.RawMessage) (map[string]any, error) {
	var request wingsession.RunInventoryRequest
	if err := decodeStrict(args, &request); err != nil {
		return nil, err
	}
	if s.Sessions == nil || s.Sessions.RunManager == nil {
		return nil, errors.New("wing run inventory is not ready")
	}
	page, err := s.Sessions.RunManager.List(ctx, s.sessionAuthority(), request)
	if err != nil {
		return nil, err
	}
	wire, err := json.Marshal(page)
	if err != nil {
		return nil, err
	}
	var data map[string]any
	if err = json.Unmarshal(wire, &data); err != nil {
		return nil, err
	}

	return control.QualifyResult(page.WingID, data), nil
}
