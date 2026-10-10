package localmcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

// ListenLocalWingControl is called only by the personal wing, independently of
// its relay connection. The owner comes from the wing credential, never IPC.
func ListenLocalWingControl(ctx context.Context, version string, sessions *wingsession.Service, ownerUserID string, admission *AdmissionState) (*controlsocket.Server, error) {
	policy := sessions.Policy()
	if sessions.SharedHost || policy.Wing == nil || policy.Wing.Org != "" {
		return nil, nil
	}
	if ownerUserID == "" {
		return nil, errors.New("local wing control requires the wing owner's user identity; log in again")
	}
	wingID := policy.Wing.WingID
	if wingID == "" {
		wingID = sessions.Config.WingID
	}
	return controlsocket.Listen(ctx, sessions.Config.Dir, wingID, func(hello controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
		server, err := resolveLocalWingClient(version, sessions, ownerUserID, admission, hello)
		if err != nil {
			return controlsocket.Welcome{}, nil, err
		}
		welcome := controlsocket.Welcome{Principal: server.clientPrincipal(), Actor: server.clientActor(), Grants: server.Grants}
		return welcome, func(callCtx context.Context, request control.DirectRequest) control.DirectResponse {
			// Resolve each call against live wing policy and clients.yaml. A connection
			// is not a lease on permissions revoked after its handshake.
			current, err := resolveLocalWingClient(version, sessions, ownerUserID, admission, hello)
			if err != nil {
				return control.DirectResponse{Version: control.ContractVersion, ID: request.ID, Error: err.Error()}
			}
			return current.handleDirectRequest(callCtx, request)
		}, nil
	})
}

func resolveLocalWingClient(version string, sessions *wingsession.Service, ownerUserID string, admission *AdmissionState, hello controlsocket.Hello) (*Server, error) {
	policy := sessions.Policy()
	if sessions.SharedHost || policy.Wing == nil || policy.Wing.Org != "" {
		return nil, errors.New("local wing control is available only on personal wings")
	}
	if policy.Wing.Locked || len(wingpolicy.PasskeysForSubject(policy.Keys, ownerUserID)) > 0 {
		return nil, errors.New("passkey authentication is required; MCP passkey ceremony is unavailable")
	}
	if hello.Unsandboxed {
		return nil, errors.New("unsandboxed launch requires allow_unsandboxed: true in wing.yaml")
	}
	principal := strings.TrimSpace(hello.Client)
	explicitClient := principal != ""
	if principal == "" {
		principal = "default"
	}
	if err := eggclient.ValidateSessionName(principal); err != nil {
		return nil, fmt.Errorf("invalid MCP client name: %w", err)
	}
	clients, err := LoadLocalMCPClientsConfig(sessions.Config)
	if err != nil {
		return nil, err
	}
	if clients.RequireClient && !explicitClient {
		return nil, errors.New("clients.yaml requires an explicit MCP client; pass --client or WT_MCP_CLIENT")
	}
	client, configured := clients.Clients[principal]
	if (clients.RequireClient || len(clients.Clients) > 0) && !configured {
		return nil, fmt.Errorf("MCP client %q is not configured in clients.yaml", principal)
	}
	owner := principal
	if configured && strings.TrimSpace(client.Owner) != "" {
		owner = strings.TrimSpace(client.Owner)
		if err := eggclient.ValidateSessionName(owner); err != nil {
			return nil, fmt.Errorf("invalid MCP owner name: %w", err)
		}
	}
	if owner == "default" {
		owner = wingsession.UserPrincipal(ownerUserID)
	}
	paths := eggclient.BrowserEggIdentity(policy.Wing, ws.PTYStart{UserID: ownerUserID, OrgRole: "owner"}, sessions.Home, false).AllowedPaths
	server := &Server{Version: version, Cfg: sessions.Config, Logs: os.Stderr, Sessions: sessions,
		Principal: owner, Actor: principal, MCPClient: principal, Surface: control.SurfaceLocalMCP, sessionRole: "owner",
		identity: eggclient.EggIdentity{UserID: ownerUserID, AllowedPaths: paths}, allowedPaths: paths, enforcePathBounds: len(policy.Wing.Paths) > 0,
		BoundConversation: hello.Conversation, admission: admission,
		legacyLocalDefault: owner == wingsession.UserPrincipal(ownerUserID),
	}
	if configured {
		server.Grants = GrantSet(client.Grants)
		server.MaxSessions = client.Bounds.MaxSessions
		server.MaxSpawnsPerHour = client.Bounds.MaxSpawnsPerHour
	}
	server.launchConfig = func(cwd string) (*egg.EggConfig, error) { return server.loadSessionLaunchConfig(cwd) }
	if err := ValidateBoundConversation(server); err != nil {
		return nil, err
	}
	return server, nil
}
