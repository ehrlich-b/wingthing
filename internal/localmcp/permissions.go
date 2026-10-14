package localmcp

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/permissionbridge"
	"github.com/ehrlich-b/wingthing/internal/permissions"
)

// Human permission operations deliberately bypass the MCP dispatcher. All
// authentication and current policy still come from the local wing resolver.
func permissionControl(ctx context.Context, s *Server, surface, name string, args json.RawMessage, refresh func() (*Server, error)) (map[string]any, error) {
	ledger := permissions.Ledger{Dir: filepath.Join(s.Cfg.Dir, "permissions")}
	actor := permissions.Actor{Principal: s.clientPrincipal(), Client: s.clientActor(), Surface: surface}
	var r permissions.Response
	if name == "permission_list" {
		var a struct {
			WingID    string `json:"wing_id"`
			SessionID string `json:"session_id"`
		}
		if err := decodeStrict(args, &a); err != nil {
			return nil, err
		}
		r.WingID, r.SessionID = a.WingID, a.SessionID
	} else if name == "permission_respond" {
		if err := decodeStrict(args, &r); err != nil {
			return nil, err
		}
	} else {
		return nil, errors.New("unsupported permission operation")
	}
	authorize := func() error {
		current, err := refresh()
		if err != nil {
			return err
		}
		tool, ok := control.Lookup(name)
		if !ok || current.Grants == nil || !current.Grants[tool.Grant] || current.tools != nil && !current.tools[name] {
			return errors.New("explicit " + tool.Grant + " grant required")
		}
		if current.BoundConversation != "" || current.Sessions == nil {
			return errors.New("personal owner permission control required")
		}
		wc := current.Sessions.Policy().Wing
		if wc == nil || wc.Org != "" || current.Sessions.SharedHost {
			return errors.New("personal wing required")
		}
		wingID := wc.WingID
		if wingID == "" {
			wingID = current.Cfg.WingID
		}
		if r.WingID == "" || r.WingID != wingID || r.SessionID == "" {
			return errors.New("exact wing and session IDs required")
		}
		session, err := current.resolveOwnedLifecycleSession(r.SessionID)
		if err != nil {
			return err
		}
		if session.ID != r.SessionID {
			return errors.New("exact session ID required")
		}
		if current.clientPrincipal() != s.clientPrincipal() || current.clientActor() != s.clientActor() {
			return errors.New("principal binding changed")
		}
		return ctx.Err()
	}
	if err := authorize(); err != nil {
		auditErr := ledger.Refuse(r, actor, "denied")
		return nil, errors.Join(err, auditErr)
	}
	if name == "permission_list" {
		result, err := permissionEggCall(ctx, s, r, "List")
		if err != nil {
			return nil, err
		}
		wire, _ := json.Marshal(result["requests"])
		var requests []permissions.Request
		if err = json.Unmarshal(wire, &requests); err != nil {
			return nil, err
		}
		for i := range requests {
			if permissionbridge.Digest(permissionbridge.Input{Tool: requests[i].Tool, Arguments: requests[i].Arguments, Directory: requests[i].Directory}) != requests[i].Digest {
				return map[string]any{"requests": []permissions.Request{}}, nil
			}
			requests[i].WingID = r.WingID
		}
		return map[string]any{"requests": requests}, nil
	}
	outcome, err := ledger.Decide(r, actor, authorize, func() (permissions.Outcome, error) {
		result, err := permissionEggCall(ctx, s, r, "Respond")
		if err != nil {
			return permissions.Outcome{}, err
		}
		wire, _ := json.Marshal(result["outcome"])
		var outcome permissions.Outcome
		err = json.Unmarshal(wire, &outcome)
		return outcome, err
	})
	if err != nil {
		return map[string]any{"outcome": outcome}, err
	}
	return map[string]any{"outcome": outcome}, nil
}
func permissionEggCall(ctx context.Context, s *Server, r permissions.Response, method string) (map[string]any, error) {
	dir := filepath.Join(s.Cfg.Dir, "eggs", r.SessionID)
	client, err := egg.Dial(filepath.Join(dir, "egg.sock"), filepath.Join(dir, "egg.token"))
	if err != nil {
		return nil, err
	}
	defer client.Close()
	return client.PermissionCall(ctx, method, r)
}

// CallHumanPermission addresses only the selected local wing. No remote
// discovery or terminal-input fallback is part of this contract.
func CallHumanPermission(ctx context.Context, dir, clientName, surface, name string, args json.RawMessage) (map[string]any, error) {
	c, err := controlsocket.Dial(ctx, dir, controlsocket.Hello{Client: clientName, Human: surface})
	if err != nil {
		return nil, err
	}
	defer c.Close()
	result, isError, err := c.Call(ctx, name, args)
	if err != nil {
		return nil, err
	}
	if isError {
		return result, control.ToolError(result)
	}
	return result, nil
}
