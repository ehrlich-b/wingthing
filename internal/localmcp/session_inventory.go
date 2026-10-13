package localmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/fleet"
	"github.com/ehrlich-b/wingthing/internal/sessionrole"
)

type inventoryArgs struct {
	Query          string `json:"query"`
	IncludeRemotes bool   `json:"include_remotes"`
}

func (s *Server) fleetIdentity() (string, string, error) {
	if s.Sessions == nil || s.Sessions.Policy == nil {
		return "", "", errors.New("wing session service is not ready")
	}
	wc := s.Sessions.Policy().Wing
	if wc == nil {
		return "", "", errors.New("wing policy is unavailable")
	}
	id := wc.WingID
	if id == "" {
		id = s.Cfg.WingID
	}
	label := wc.Label
	if label == "" {
		label, _ = os.Hostname()
	}
	if id == "" {
		return "", "", errors.New("wing identity is unavailable")
	}
	return id, label, nil
}
func (s *Server) fleetRemotesAllowed() bool {
	return s.Sessions != nil && !s.Sessions.SharedHost && s.Sessions.Policy().Wing.Org == "" && !s.enforcePathBounds && s.BoundConversation == "" && !s.restrictWings && !s.scopedParent && s.remoteToolCall != nil
}
func (s *Server) ToolSessionInventory(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
	var args inventoryArgs
	if err := decodeFleetArguments(arguments, &args); err != nil {
		return nil, err
	}
	if s.BoundConversation != "" || s.scopedParent {
		return nil, errors.New("fleet inventory is unavailable on a conversation-bound connection")
	}
	id, label, err := s.fleetIdentity()
	if err != nil {
		return nil, err
	}
	if args.IncludeRemotes && !s.fleetRemotesAllowed() {
		return nil, errors.New("remote inventory requires an unrestricted personal wing connection")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, complete, err := eggclient.SessionCatalog(ctx, s.Cfg, id, label, args.Query, s.ownsSession)
	if err != nil {
		return nil, err
	}
	at := time.Now().UTC().Format(time.RFC3339Nano)
	for i := range rows {
		rows[i].Routes = []fleet.Route{{WingID: id, MachineLabel: label, Kind: "direct", Healthy: true, ObservedAt: at}}
	}
	inv := fleet.Inventory{Version: fleet.Version, WingID: id, MachineLabel: label, GeneratedAt: at, Sessions: rows, Sources: []fleet.Source{{WingID: id, MachineLabel: label, Healthy: true, ObservedAt: at}}, Complete: complete, Query: args.Query}
	if args.IncludeRemotes {
		remotes, err := config.LoadRemotes(s.Cfg.Dir)
		if err != nil {
			return nil, err
		}
		names := eggclient.SortedRemoteNames(remotes)
		// Fixed worker bound; one request reaches each origin, never a recursive fanout.
		type result struct {
			inv fleet.Inventory
			err error
		}
		results := make([]result, len(names))
		jobs := make(chan int)
		done := make(chan struct{}, min(4, len(names)))
		for range min(4, len(names)) {
			go func() {
				defer func() { done <- struct{}{} }()
				for i := range jobs {
					name := names[i]
					remote := remotes[name]
					if remote.WingID == "" {
						results[i].err = errors.New("remote origin is unverified; run remote verification before fleet access")
						continue
					}
					raw, _ := json.Marshal(inventoryArgs{Query: args.Query})
					value, e := s.remoteToolCall(ctx, name, "session_inventory", raw)
					if e == nil {
						data, _ := json.Marshal(value)
						e = json.Unmarshal(data, &results[i].inv)
					}
					if e == nil && (results[i].inv.Version != fleet.Version || results[i].inv.WingID != remote.WingID) {
						e = errors.New("remote inventory identity/contract mismatch; verify the remote again")
					}
					if e == nil {
						for _, row := range results[i].inv.Sessions {
							if row.OriginWingID != remote.WingID {
								e = errors.New("remote inventory contains a foreign origin")
								break
							}
						}
					}
					results[i].err = e
				}
			}()
		}
		for i := range names {
			jobs <- i
		}
		close(jobs)
		for range min(4, len(names)) {
			<-done
		}
		for i, name := range names {
			r := results[i]
			remote := remotes[name]
			if r.err != nil {
				inv.Complete = false
				inv.Sources = append(inv.Sources, fleet.Source{WingID: remote.WingID, MachineLabel: name, Healthy: false, ObservedAt: at, Error: r.err.Error()})
				continue
			}
			inv.Complete = inv.Complete && r.inv.Complete
			inv.Sources = append(inv.Sources, r.inv.Sources...)
			for _, row := range r.inv.Sessions {
				row.Routes = []fleet.Route{{WingID: id, MachineLabel: label, Kind: "via", Remote: name, Healthy: true, ObservedAt: at}}
				inv.Sessions = append(inv.Sessions, row)
			}
		}
	}
	// Merge repeated configured aliases to the same proven origin, then pin first.
	merged := fleet.Merge([]fleet.Inventory{inv})
	inv.Sessions = merged.Sessions
	return inventoryMap(inv), nil
}
func inventoryMap(inv fleet.Inventory) map[string]any {
	data, _ := json.Marshal(inv)
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return out
}

type actionArgs struct {
	OriginWingID string          `json:"origin_wing_id"`
	SessionID    string          `json:"session_id"`
	Operation    string          `json:"operation"`
	Arguments    json.RawMessage `json:"arguments"`
	LocalOnly    bool            `json:"local_only"`
}

var fleetActions = map[string]bool{"session_status": true, "session_read": true, "terminal_read": true, "session_prompt": true, "terminal_send": true, "terminal_stop": true, "terminal_rename": true}

func (s *Server) ToolSessionAction(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
	var args actionArgs
	if err := decodeFleetArguments(arguments, &args); err != nil {
		return nil, err
	}
	if args.OriginWingID == "" || args.SessionID == "" {
		return nil, errors.New("origin_wing_id and full session_id are required")
	}
	if err := eggclient.ValidateSessionID(args.SessionID); err != nil {
		return nil, err
	}
	if !fleetActions[args.Operation] {
		return nil, errors.New("unsupported qualified session operation")
	}
	// Both controller and execution wing must allow the underlying operation.
	if !s.toolAllowed(args.Operation) {
		return nil, errors.New("underlying session operation is not granted")
	}
	fields := map[string]json.RawMessage{}
	if len(args.Arguments) > 0 {
		if err := json.Unmarshal(args.Arguments, &fields); err != nil || fields == nil {
			return nil, errors.New("arguments must be an object")
		}
	}
	if _, has := fields["session"]; has {
		return nil, errors.New("session selector belongs in session_id only")
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
		remotes, e := config.LoadRemotes(s.Cfg.Dir)
		if e != nil {
			return nil, e
		}
		var names []string
		for name, r := range remotes {
			if r.WingID == args.OriginWingID {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		if len(names) == 0 {
			return nil, errors.New("origin has no verified configured route")
		}
		args.LocalOnly = true
		raw, _ := json.Marshal(args)
		// Exactly one dispatch. A lost acknowledgement is not permission to resend.
		result, e := s.remoteToolCall(ctx, names[0], "session_action", raw)
		if e != nil {
			return nil, fmt.Errorf("qualified action failed; reconcile the original request before retrying: %w", e)
		}
		route.Kind = "via"
		route.Remote = names[0]
		result["route"] = route
		return result, nil
	}
	session, err := s.resolveOwnedLifecycleSession(args.SessionID)
	if err != nil {
		return nil, err
	}
	if session.ID != args.SessionID {
		return nil, errors.New("session_id must be the exact full ID; labels and prefixes are display only")
	}
	fields["session"], _ = json.Marshal(session.ID)
	raw, _ := json.Marshal(fields)
	result, denied, protocolErr := s.callTool(ctx, args.Operation, raw)
	if protocolErr != nil {
		return nil, errors.New(protocolErr.Message)
	}
	if denied {
		err := control.ToolError(result)
		if err == nil {
			err = fmt.Errorf("%v", result["error"])
		}
		return nil, err
	}
	return map[string]any{"origin_wing_id": id, "session_id": session.ID, "route": route, "result": result}, nil
}
func (s *Server) ToolCoordinatorHeartbeat(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		Session  string `json:"session"`
		State    string `json:"state"`
		LastWake string `json:"last_wake"`
		NextWake string `json:"next_wake"`
		LastNote string `json:"last_note"`
	}
	if err := decodeFleetArguments(arguments, &args); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	session, err := s.resolveOwnedLifecycleSession(args.Session)
	if err != nil {
		return nil, err
	}
	if session.ID != args.Session {
		return nil, errors.New("heartbeat requires the exact session ID")
	}
	dir := filepath.Join(s.Cfg.Dir, "eggs", session.ID)
	role, err := sessionrole.Read(dir)
	if err != nil {
		return nil, err
	}
	if role.Role != "coordinator" {
		return nil, errors.New("heartbeat requires a coordinator role session")
	}
	p := fleet.Presence{State: args.State, LastWake: args.LastWake, NextWake: args.NextWake, LastNote: args.LastNote}
	if err := sessionrole.RecordPresence(dir, p, time.Now()); err != nil {
		return nil, err
	}
	return map[string]any{"session": session.ID, "presence": sessionrole.ReadPresence(dir, session.PID > 0, "", "", time.Now())}, nil
}

func decodeFleetArguments(raw json.RawMessage, target any) error {
	if !strings.HasPrefix(strings.TrimSpace(string(raw)), "{") {
		return errors.New("arguments must be one JSON object")
	}
	return decodeStrict(raw, target)
}
