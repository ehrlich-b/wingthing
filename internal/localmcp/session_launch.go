package localmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/fleet"
)

type launchArgs struct {
	OriginWingID string          `json:"origin_wing_id"`
	Operation    string          `json:"operation"`
	Arguments    json.RawMessage `json:"arguments,omitempty"`
	LocalOnly    bool            `json:"local_only"`
}

// ToolSessionLaunch selects an execution origin before using the existing
// launch primitive. Transport loss never triggers another dispatch or origin.
func (s *Server) ToolSessionLaunch(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
	var args launchArgs
	if err := decodeFleetArguments(arguments, &args); err != nil {
		return nil, err
	}
	if args.OriginWingID == "" || (args.Operation != "terminal_start" && args.Operation != "agent_start") {
		return nil, errors.New("origin_wing_id and terminal_start or agent_start are required")
	}
	if s.Sessions == nil || s.Sessions.SharedHost || s.Sessions.Policy().Wing == nil || s.Sessions.Policy().Wing.Org != "" || s.BoundConversation != "" || s.scopedParent {
		return nil, errors.New("qualified launch requires an unrestricted personal wing connection")
	}
	if !s.toolAllowed(args.Operation) {
		return nil, errors.New("underlying launch operation is not granted")
	}
	if len(args.Arguments) == 0 {
		args.Arguments = json.RawMessage(`{}`)
	}
	var fields map[string]json.RawMessage
	if err := decodeFleetArguments(args.Arguments, &fields); err != nil {
		return nil, err
	}
	// Continuation is an action on an existing identity, not a new launch.
	if _, ok := fields["resume_session"]; ok {
		return nil, errors.New("qualified launch cannot resume an existing session")
	}
	id, label, err := s.fleetIdentity()
	if err != nil {
		return nil, err
	}
	route := fleet.Route{WingID: id, MachineLabel: label, Kind: "direct", Healthy: true, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if args.OriginWingID != id {
		if args.LocalOnly || !s.fleetRemotesAllowed() {
			return nil, errors.New("origin is not this wing or remote access is unavailable")
		}
		remotes, err := config.LoadRemotes(s.Cfg.Dir)
		if err != nil {
			return nil, err
		}
		var names []string
		for name, remote := range remotes {
			if remote.WingID == args.OriginWingID {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		if len(names) == 0 {
			return nil, errors.New("origin has no verified configured route")
		}
		args.LocalOnly = true
		raw, _ := json.Marshal(args)
		result, err := s.remoteToolCall(ctx, names[0], "session_launch", raw)
		if err != nil {
			return nil, fmt.Errorf("qualified launch outcome may be unknown; inspect the origin inventory before any deliberate retry: %w", err)
		}
		session, ok := result["session_id"].(string)
		if result["origin_wing_id"] != args.OriginWingID || !ok || eggclient.ValidateSessionID(session) != nil {
			return nil, errors.New("launch receipt identity mismatch; inspect the origin inventory before retrying")
		}
		route.Kind, route.Remote = "via", names[0]
		result["route"] = route
		return result, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result, denied, protocolErr := s.callTool(ctx, args.Operation, args.Arguments)
	if protocolErr != nil {
		return nil, errors.New(protocolErr.Message)
	}
	if denied {
		err := control.ToolError(result)
		if err == nil {
			err = errors.New("launch denied")
		}
		return nil, err
	}
	session, ok := result["session"].(string)
	if !ok || eggclient.ValidateSessionID(session) != nil {
		return nil, errors.New("launch returned no exact session identity; inspect the origin inventory before retrying")
	}
	return map[string]any{"origin_wing_id": id, "session_id": session, "route": route, "result": result}, nil
}
