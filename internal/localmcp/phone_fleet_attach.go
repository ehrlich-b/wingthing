package localmcp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/fleet"
	"github.com/ehrlich-b/wingthing/internal/sshcontrol"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
)

// FleetAttachment retains one route for its whole lifetime. Reconnecting or
// falling back requires a new explicit attach; uncertain input is never replayed.
type FleetAttachment struct {
	Stream   *controlsocket.SessionStream
	Identity fleet.Identity
	Route    fleet.Route
	sessions *wingsession.Service
	owner    string
	remote   *config.Remote
	forward  *sshcontrol.Connection
	cancel   context.CancelFunc
	once     sync.Once
}

func phoneFleetServer(sessions *wingsession.Service, owner string) (*Server, error) {
	if sessions == nil || sessions.Policy == nil {
		return nil, errors.New("wing runtime unavailable")
	}
	if _, err := os.Stat(filepath.Join(sessions.Config.Dir, "local-owner.yaml")); err != nil {
		return nil, errors.New("personal owner binding unavailable")
	}
	local, err := config.EnsureLocalOwner(sessions.Config.Dir, "")
	if err != nil {
		return nil, err
	}
	if owner == "" || local.ID != owner {
		return nil, errors.New("fleet access is not delegated to this owner")
	}
	return resolveLocalWingClient("", sessions, owner, NewMCPAdmissionState(), controlsocket.Hello{})
}

// Check runs before each phone read/input. Both endpoints independently recheck
// current clients, owner, path and passkey policy through the control stream.
func (a *FleetAttachment) Check(ctx context.Context, write bool) error {
	s, err := phoneFleetServer(a.sessions, a.owner)
	if err != nil {
		return err
	}
	if !s.toolAllowed("terminal_read") || write && !s.toolAllowed("terminal_send") {
		return errors.New("terminal attachment is not granted")
	}
	if a.remote != nil {
		if !s.Sessions.Policy().Wing.PhoneFleet || s.enforcePathBounds || s.restrictWings || s.BoundConversation != "" {
			return errors.New("phone fleet attachment is no longer delegated")
		}
		remotes, err := config.LoadRemotes(s.Cfg.Dir)
		if err != nil {
			return err
		}
		current, ok := remotes[a.Route.Remote]
		if !ok || current != *a.remote {
			return errors.New("attachment route changed; refresh and explicitly attach again")
		}
		if a.forward != nil {
			raw, _ := json.Marshal(actionArgs{OriginWingID: a.Identity.OriginWingID, SessionID: a.Identity.SessionID, Operation: "session_status", Arguments: json.RawMessage(`{}`), LocalOnly: true})
			checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			_, denied, err := a.forward.Client.Call(checkCtx, "session_action", raw)
			if err != nil {
				return err
			}
			if denied {
				return errors.New("execution wing no longer grants attachment access")
			}
		}
	} else {
		origin, _, err := s.fleetIdentity()
		if err != nil {
			return err
		}
		if origin != a.Identity.OriginWingID {
			return errors.New("attachment origin changed")
		}
		session, err := s.Sessions.Resolve(ctx, s.sessionAuthority(), a.Identity.SessionID, true)
		if err != nil {
			return err
		}
		if session.ID != a.Identity.SessionID {
			return errors.New("attachment identity changed")
		}
	}
	return ctx.Err()
}
func (a *FleetAttachment) Close() error {
	a.once.Do(func() {
		a.cancel()
		_ = a.Stream.Close()
		if a.forward != nil {
			_ = a.forward.Close()
		}
	})
	return nil
}
func (a *FleetAttachment) Send(msg *pb.SessionMsg) error { return a.Stream.Send(msg) }
func (a *FleetAttachment) Recv() (*pb.SessionMsg, error) { return a.Stream.Recv() }

func OwnerFleetAttach(ctx context.Context, sessions *wingsession.Service, owner string, id fleet.Identity, readOnly, takeover bool) (*FleetAttachment, error) {
	if id.OriginWingID == "" || id.SessionID == "" {
		return nil, errors.New("origin_wing_id and full session_id are required")
	}
	if err := eggclient.ValidateSessionID(id.SessionID); err != nil {
		return nil, err
	}
	if readOnly && takeover {
		return nil, errors.New("read-only attachment cannot take over")
	}
	s, err := phoneFleetServer(sessions, owner)
	if err != nil {
		return nil, err
	}
	origin, label, err := s.fleetIdentity()
	if err != nil {
		return nil, err
	}
	lifetime, cancel := context.WithCancel(ctx)
	a := &FleetAttachment{Identity: id, sessions: sessions, owner: owner, cancel: cancel,
		Route: fleet.Route{WingID: origin, MachineLabel: label, Kind: "direct", Healthy: true, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}}
	success := false
	defer func() {
		if !success {
			cancel()
			if a.Stream != nil {
				_ = a.Stream.Close()
			}
			if a.forward != nil {
				_ = a.forward.Close()
			}
		}
	}()
	hello := controlsocket.Hello{WingID: id.OriginWingID, Attach: &controlsocket.Attachment{Session: id.SessionID, Exact: true, ReadOnly: readOnly, Takeover: takeover}}
	if !readOnly {
		hello.Attach.Rows, hello.Attach.Cols = 30, 80
	}
	path, err := controlsocket.Path(sessions.Config.Dir)
	if err != nil {
		return nil, err
	}
	if id.OriginWingID != origin {
		if !s.Sessions.Policy().Wing.PhoneFleet || s.enforcePathBounds || s.restrictWings || s.BoundConversation != "" {
			return nil, errors.New("phone fleet attachment requires an unrestricted personal gateway with phone_fleet: true")
		}
		remotes, err := config.LoadRemotes(s.Cfg.Dir)
		if err != nil {
			return nil, err
		}
		names := []string{}
		for name, remote := range remotes {
			if remote.WingID == id.OriginWingID {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		if len(names) == 0 {
			return nil, errors.New("origin has no verified configured route")
		}
		remote := remotes[names[0]]
		a.remote = &remote
		a.Route.Kind, a.Route.Remote = "via", names[0]
		if err := a.Check(ctx, !readOnly); err != nil {
			return nil, err
		}
		attempt, stop := context.WithTimeout(ctx, 5*time.Second)
		a.forward, err = (sshcontrol.Transport{}).Dial(attempt, remote, controlsocket.Hello{})
		stop()
		if err != nil {
			return nil, err
		}
		path = a.forward.SocketPath()
	}
	if err := a.Check(ctx, !readOnly); err != nil {
		return nil, err
	}
	// The timeout bounds readiness only; a successful stream uses the phone's
	// authenticated connection lifetime, not a short request context.
	timer := time.AfterFunc(5*time.Second, cancel)
	a.Stream, _, err = controlsocket.DialAttachmentPath(lifetime, path, hello)
	timer.Stop()
	if err != nil {
		return nil, err
	}
	success = true
	return a, nil
}

// DecodePhoneFleetAttachment rejects unknown fields and non-object envelopes.
func DecodePhoneFleetAttachment(raw string, target any) error {
	return decodeFleetArguments(json.RawMessage(raw), target)
}
