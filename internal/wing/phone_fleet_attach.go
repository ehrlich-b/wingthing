package wing

import (
	"context"
	"errors"
	"sync"

	"github.com/ehrlich-b/wingthing/internal/egg"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/fleet"
	"github.com/ehrlich-b/wingthing/internal/localmcp"
)

type fleetPhoneAttachment struct {
	bridge                   *localmcp.FleetAttachment
	readOnly                 bool
	mu                       sync.Mutex
	output                   []byte
	terminal                 *egg.VTerm
	ended, truncated, closed bool
	done                     chan struct{}
}

func (h *phoneSessions) executeFleetAttachment(ctx context.Context, connection, op, raw string) (any, error) {
	var args struct {
		OriginWingID string `json:"origin_wing_id"`
		SessionID    string `json:"session_id"`
		Input        string `json:"input,omitempty"`
		ReadOnly     bool   `json:"read_only,omitempty"`
		Takeover     bool   `json:"takeover,omitempty"`
	}
	if err := localmcp.DecodePhoneFleetAttachment(raw, &args); err != nil {
		return nil, err
	}
	id := fleet.Identity{OriginWingID: args.OriginWingID, SessionID: args.SessionID}
	if id.OriginWingID == "" || id.SessionID == "" {
		return nil, errors.New("origin_wing_id and full session_id are required")
	}
	if op == "fleet_attach" {
		if args.Input != "" {
			return nil, errors.New("attach does not accept input")
		}
		h.detach(connection)
		bridge, err := localmcp.OwnerFleetAttach(ctx, h.service, h.owner, id, args.ReadOnly, args.Takeover)
		if err != nil {
			return nil, err
		}
		a := &fleetPhoneAttachment{bridge: bridge, readOnly: args.ReadOnly, terminal: egg.NewVTerm(80, 30), done: make(chan struct{})}
		// The first frame acknowledges the actual egg attachment/writer claim.
		first, err := bridge.Recv()
		if err != nil {
			_ = bridge.Close()
			_ = a.terminal.Close()
			return nil, err
		}
		a.accept(first)
		h.mu.Lock()
		if h.fleetAttachments == nil {
			h.fleetAttachments = map[string]*fleetPhoneAttachment{}
		}
		h.fleetAttachments[connection] = a
		h.mu.Unlock()
		go func() {
			defer close(a.done)
			defer bridge.Close()
			for {
				msg, err := bridge.Recv()
				a.mu.Lock()
				if err != nil {
					a.ended = true
					a.mu.Unlock()
					return
				}
				a.accept(msg)
				ended := a.ended
				a.mu.Unlock()
				if ended {
					return
				}
			}
		}()
		return map[string]any{"ok": true, "origin_wing_id": id.OriginWingID, "session_id": id.SessionID, "route": bridge.Route, "read_only": a.readOnly}, nil
	}
	if args.ReadOnly || args.Takeover || op == "fleet_read" && args.Input != "" {
		return nil, errors.New("read/input cannot change attachment options")
	}
	h.mu.Lock()
	a := h.fleetAttachments[connection]
	h.mu.Unlock()
	if a == nil || a.bridge.Identity != id {
		return nil, errors.New("not attached to this exact origin/session")
	}
	if op == "fleet_detach" {
		if args.Input != "" {
			return nil, errors.New("detach does not accept input")
		}
		route := a.bridge.Route
		h.detachFleet(connection)
		return map[string]any{"ok": true, "origin_wing_id": id.OriginWingID, "session_id": id.SessionID, "route": route, "read_only": true}, nil
	}
	if err := a.bridge.Check(ctx, op == "fleet_input"); err != nil {
		h.detachFleet(connection)
		return nil, err
	}
	if op == "fleet_input" {
		if a.readOnly {
			return nil, errors.New("read-only attachment cannot input")
		}
		if len(args.Input) > 16<<10 {
			return nil, errors.New("too large")
		}
		err := a.bridge.Send(&pb.SessionMsg{SessionId: id.SessionID, Payload: &pb.SessionMsg_Input{Input: []byte(args.Input)}})
		if err != nil {
			h.detachFleet(connection)
			return nil, errors.New("attachment input outcome unknown; read state before explicit retry")
		}
		return map[string]any{"ok": true, "origin_wing_id": id.OriginWingID, "session_id": id.SessionID, "route": a.bridge.Route}, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, errors.New("attachment closed")
	}
	result := map[string]any{"origin_wing_id": id.OriginWingID, "session_id": id.SessionID, "route": a.bridge.Route, "output": string(a.output), "screen": a.terminal.ScreenText(), "ended": a.ended, "truncated": a.truncated}
	a.output, a.truncated = nil, false
	return result, nil
}
func (a *fleetPhoneAttachment) accept(msg *pb.SessionMsg) {
	if output, ok := msg.Payload.(*pb.SessionMsg_Output); ok {
		_, _ = a.terminal.Write(output.Output)
		a.output = append(a.output, output.Output...)
		if len(a.output) > 64<<10 {
			a.output = a.output[len(a.output)-(64<<10):]
			a.truncated = true
		}
	}
	if _, ok := msg.Payload.(*pb.SessionMsg_ExitCode); ok {
		a.ended = true
	}
}
func (h *phoneSessions) detachFleet(connection string) {
	h.mu.Lock()
	a := h.fleetAttachments[connection]
	delete(h.fleetAttachments, connection)
	h.mu.Unlock()
	if a != nil {
		_ = a.bridge.Close()
		<-a.done
		a.mu.Lock()
		a.closed = true
		_ = a.terminal.Close()
		a.mu.Unlock()
	}
}
