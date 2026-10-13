package localmcp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
)

// OwnerFleetControl is the paired phone/browser bridge to the wing-owned MCP
// socket. Remote delegation is explicit opt-in, personal-only and owner-bound.
// The socket rechecks clients.yaml, passkeys, paths, grants and remote pins.
func OwnerFleetControl(ctx context.Context, sessions *wingsession.Service, owner, operation string, args json.RawMessage) (map[string]any, error) {
	if sessions == nil || sessions.Policy == nil {
		return nil, errors.New("wing runtime unavailable")
	}
	policy := sessions.Policy()
	if policy.Wing == nil || sessions.SharedHost || policy.Wing.Org != "" || owner == "" {
		return nil, errors.New("personal owner fleet control required")
	}
	if operation != "session_inventory" && operation != "session_action" && operation != "session_launch" {
		return nil, errors.New("unsupported phone fleet operation")
	}
	if _, err := os.Stat(filepath.Join(sessions.Config.Dir, "local-owner.yaml")); err != nil {
		return nil, errors.New("personal owner binding unavailable")
	}
	local, err := config.EnsureLocalOwner(sessions.Config.Dir, "")
	if err != nil {
		return nil, err
	}
	if local.ID != owner {
		return nil, errors.New("fleet access is not delegated to this owner")
	}
	remote := false
	if operation == "session_inventory" {
		var a inventoryArgs
		if e := decodeStrict(args, &a); e != nil {
			return nil, e
		}
		remote = a.IncludeRemotes
	} else if operation == "session_action" {
		var a actionArgs
		if e := decodeStrict(args, &a); e != nil {
			return nil, e
		}
		id := policy.Wing.WingID
		if id == "" {
			id = sessions.Config.WingID
		}
		remote = a.OriginWingID != id
	} else {
		var a launchArgs
		if e := decodeFleetArguments(args, &a); e != nil {
			return nil, e
		}
		if a.Operation == "agent_start" {
			var fields map[string]json.RawMessage
			if e := json.Unmarshal(a.Arguments, &fields); e != nil {
				return nil, e
			}
			if len(fields["conversation_role"]) == 0 && len(fields["parent_conversation_id"]) == 0 {
				return nil, errors.New("phone/browser agent launch requires a linked personal conversation")
			}
		}
		id := policy.Wing.WingID
		if id == "" {
			id = sessions.Config.WingID
		}
		remote = a.OriginWingID != id
	}
	if remote && !policy.Wing.PhoneFleet {
		return nil, errors.New("phone fleet access requires phone_fleet: true on the personal gateway")
	}
	return CallLocalWingTool(ctx, sessions.Config.Dir, "", operation, args)
}
