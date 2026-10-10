package localmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
// its relay connection. The owner comes from durable local state, never IPC.
func ListenLocalWingControl(ctx context.Context, version string, sessions *wingsession.Service, ownerUserID string, admission *AdmissionState) (*controlsocket.Server, error) {
	policy := sessions.Policy()
	if sessions.SharedHost || policy.Wing == nil || policy.Wing.Org != "" {
		return nil, nil
	}
	if ownerUserID == "" {
		return nil, errors.New("local wing control requires the wing's local owner identity")
	}
	wingID := policy.Wing.WingID
	if wingID == "" {
		wingID = sessions.Config.WingID
	}
	remotes := newRememberedWings(ctx, sessions.Config.Dir, wingID)
	if admission == nil {
		admission = NewMCPAdmissionState()
	}
	admission.Sessions = sessions
	mailboxes := newWingMailboxes(ctx, version, ownerUserID, sessions, admission, remotes)
	listener, err := controlsocket.Listen(ctx, sessions.Config.Dir, wingID, func(hello controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
		server, err := resolveLocalWingClient(version, sessions, ownerUserID, admission, hello)
		if err != nil {
			return controlsocket.Welcome{}, nil, err
		}
		if hello.Aggregate && (hello.Conversation != "" || hello.Execution != "") {
			return controlsocket.Welcome{}, nil, errors.New("aggregate control is unavailable on a conversation-bound MCP connection")
		}
		mailboxes.configure(server)
		welcome := controlsocket.Welcome{RemoteAllowed: !server.enforcePathBounds && server.BoundConversation == "", Principal: server.clientPrincipal(), Actor: server.clientActor(), Grants: server.Grants, Tools: server.tools, Isolation: server.sessionIsolationMode()}
		return welcome, func(callCtx context.Context, request control.DirectRequest) control.DirectResponse {
			// Resolve each call against live wing policy and clients.yaml. A connection
			// is not a lease on permissions revoked after its handshake.
			current, err := resolveLocalWingClient(version, sessions, ownerUserID, admission, hello)
			if err != nil {
				return control.DirectResponse{Version: control.ContractVersion, ID: request.ID, Error: err.Error(), ErrorKind: control.ErrorKindOf(err)}
			}
			mailboxes.configure(current)
			current.remoteToolCall = func(ctx context.Context, name, tool string, args []byte) (map[string]any, error) {
				return remotes.remoteByName(ctx, hello, name, tool, args)
			}
			var response control.DirectResponse
			if current.broker != nil && current.broker.Scoped {
				response = mailboxes.dispatch(callCtx, current, request)
			} else if hello.Aggregate {
				response = remotes.dispatch(callCtx, current, hello, request)
			} else {
				response = current.handleDirectRequest(callCtx, request)
			}
			if response.Result != nil {
				if _, qualified := response.Result["wing_id"]; !qualified {
					response.Result = control.QualifyResult(wingID, response.Result)
				}
			}
			return response
		}, nil
	}, bindWingAttachment(version, sessions, ownerUserID, admission))
	if err != nil {
		_ = mailboxes.Close()
		_ = remotes.Close()
		return nil, err
	}
	listener.AddCloser(mailboxes)
	listener.AddCloser(remotes)
	mailboxes.restore()
	return listener, nil
}

func (s *Server) toolLocalWingList(arguments []byte) (map[string]any, error) {
	var empty struct{}
	if err := decodeStrict(arguments, &empty); err != nil {
		return nil, fmt.Errorf("wing_list arguments: %w", err)
	}
	if s.controlSurface() != control.SurfaceLocalMCP || s.Sessions == nil {
		return nil, errors.New("wing directory is supplied by the MCP transport adapter")
	}
	wingID := s.Sessions.Policy().Wing.WingID
	if wingID == "" {
		wingID = s.Cfg.WingID
	}
	return map[string]any{"wings": []map[string]any{{
		"wing_id": wingID, "hostname": s.Cfg.Hostname, "online": true,
		"mcp_control": true, "mcp_transport": "local-socket", "paths": s.allowedPaths,
	}}, "count": 1, "control_scope": "local"}, nil
}

func resolveLocalWingClient(version string, sessions *wingsession.Service, ownerUserID string, admission *AdmissionState, hello controlsocket.Hello) (*Server, error) {
	policy := sessions.Policy()
	if sessions.SharedHost || policy.Wing == nil || policy.Wing.Org != "" {
		return nil, errors.New("local wing control is available only on personal wings")
	}
	if hello.Unsandboxed && !policy.Wing.AllowUnsandboxed {
		return nil, errors.New("unsandboxed launch requires allow_unsandboxed: true in wing.yaml")
	}
	if hello.Execution != "" {
		reg, err := loadConversationBrokerRegistration(sessions.Config, hello.Execution)
		if err != nil {
			return nil, err
		}
		if hello.Client != "" || hello.Conversation != reg.ConversationID || reg.UserID != "" && reg.UserID != ownerUserID {
			return nil, errors.New("host mailbox binding does not match this personal wing")
		}
		captured, wc, err := reg.server(version, sessions.Config, admission)
		if err != nil {
			return nil, err
		}
		captured.mailboxLocked = wc.Locked
		captured.Sessions = sessions
		captured.identity.UserID = ownerUserID
		captured.sessionRole = "owner"
		return captured, nil
	}
	if policy.Wing.Locked || len(wingpolicy.PasskeysForSubject(policy.Keys, ownerUserID)) > 0 {
		return nil, errors.New("passkey authentication is required; MCP passkey ceremony is unavailable")
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
		BoundConversation: hello.Conversation, admission: admission, Unsandboxed: hello.Unsandboxed,
		legacyLocalDefault: owner == wingsession.UserPrincipal(ownerUserID),
	}
	if configured {
		server.Grants = GrantSet(client.Grants)
		server.wingGrants = client.Wings
		server.restrictWings = true
		server.MaxSessions = client.Bounds.MaxSessions
		server.MaxSpawnsPerHour = client.Bounds.MaxSpawnsPerHour
	}
	if hello.Scope != "" {
		var scope wingCallScope
		if err := decodeStrict(json.RawMessage(hello.Scope), &scope); err != nil {
			return nil, err
		}
		if len(scope.Paths) == 0 || len(scope.Tools) == 0 || scope.MaxSessions <= 0 || scope.MaxSpawnsPerHour <= 0 {
			return nil, errors.New("invalid receiving wing scope")
		}
		for _, path := range scope.Paths {
			if !filepath.IsAbs(path) || filepath.Clean(path) != path {
				return nil, errors.New("scope paths must be clean and absolute on the execution wing")
			}
		}
		server.allowedPaths, _ = intersectBrokerPaths(scope.Paths, true, server.allowedPaths)
		server.enforcePathBounds = true
		server.identity.AllowedPaths = server.allowedPaths
		server.tools = map[string]bool{}
		for _, name := range scope.Tools {
			if server.Grants == nil || func() bool { tool, ok := control.Lookup(name); return ok && server.Grants[tool.Grant] }() {
				server.tools[name] = true
			}
		}
		if server.MaxSessions <= 0 || scope.MaxSessions < server.MaxSessions {
			server.MaxSessions = scope.MaxSessions
		}
		if server.MaxSpawnsPerHour <= 0 || scope.MaxSpawnsPerHour < server.MaxSpawnsPerHour {
			server.MaxSpawnsPerHour = scope.MaxSpawnsPerHour
		}
	}
	server.launchConfig = func(cwd string) (*egg.EggConfig, error) { return server.loadSessionLaunchConfig(cwd) }
	if err := ValidateBoundConversation(server); err != nil {
		return nil, err
	}
	return server, nil
}
