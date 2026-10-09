package localmcp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

var browserConversationAdmission = NewMCPAdmissionState()

type BrowserLaunchConfig struct {
	Sessions             *wingsession.Service
	PublicKey, AuthToken string
	EggConfig            *egg.EggConfig
	Tools                []*config.ToolConfig
}

// browserSessionControl is a narrow adapter over the same typed MCP handlers.
// Authentication/passkey/purpose checks occur before this dispatcher; artifact
// access is checked independently before selecting a legacy session principal.
func BrowserSessionControl(version string, ctx context.Context, cfg *config.Config, wc *config.WingConfig, req ws.TunnelRequest, operation string, arguments json.RawMessage, home string, sharedHost bool, launchConfig ...BrowserLaunchConfig) (map[string]any, error) {
	switch operation {
	case "session_fork", "session_status", "session_read", "session_wait", "session_prompt", "terminal_send", "conversation_list", "conversation_bootstrap", "conversation_read", "conversation_checkpoint", "conversation_wake", "agent_start":
	default:
		return nil, errors.New("unsupported browser session operation")
	}
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	if len(arguments) > 1<<20 {
		return nil, errors.New("session operation arguments exceed 1 MiB")
	}
	if wc.IsAdmin(req.SenderEmail) && wingpolicy.IsMemberRole(req.SenderOrgRole) {
		req.SenderOrgRole = "admin"
	}
	paths := wingpolicy.CanonicalPaths(wingpolicy.PathsForRequest(wc.Paths, req.SenderEmail, req.SenderOrgRole, home))
	server := &Server{Version: version, Cfg: cfg, Logs: os.Stderr, Principal: roostSessionPrincipal(req.SenderUserID), Actor: "browser", Surface: control.SurfaceHTTPMCP,
		Grants: GrantSet(defaultDirectMCPGrants), MaxSessions: defaultDirectMCPMaxSessions, MaxSpawnsPerHour: defaultDirectMCPMaxSpawnsPerHour, admission: browserConversationAdmission,
		allowedPaths: paths, enforcePathBounds: len(paths) > 0 || wingpolicy.IsMemberFiltered(req), identity: eggclient.EggIdentity{UserID: req.SenderUserID, Email: req.SenderEmail, OrgWing: wc.Org != "", SharedHost: sharedHost, SealedFS: sharedHost, AllowedPaths: paths}}
	if len(launchConfig) > 0 {
		server.Sessions = launchConfig[0].Sessions
		server.sessionPublicKey = launchConfig[0].PublicKey
		server.sessionAuthToken = launchConfig[0].AuthToken
	}
	server.sessionBrowser = true
	server.sessionRole = req.SenderOrgRole
	if operation == "session_fork" {
		var current BrowserLaunchConfig
		if len(launchConfig) > 0 {
			current = launchConfig[0]
		}
		configureBrowserFork(server, wc, req, home, sharedHost, current.EggConfig, current.Tools)
	}
	if operation == "agent_start" || operation == "conversation_checkpoint" || operation == "conversation_wake" {
		if wc.Org != "" || sharedHost {
			return nil, errors.New("personal conversation mutations are unavailable on organization or shared wings")
		}
	}
	if operation == "agent_start" {
		var args map[string]json.RawMessage
		if err := json.Unmarshal(arguments, &args); err != nil {
			return nil, err
		}
		if len(args["conversation_role"]) == 0 && len(args["parent_conversation_id"]) == 0 {
			return nil, errors.New("browser agent_start requires a linked personal conversation")
		}
	}
	if operation == "session_fork" || operation == "session_status" || operation == "session_read" || operation == "session_wait" || (operation == "session_prompt" || operation == "terminal_send") {
		var selector struct {
			Session string `json:"session"`
		}
		if err := json.Unmarshal(arguments, &selector); err != nil {
			return nil, err
		}
		if err := eggclient.ValidateSessionID(selector.Session); err != nil {
			return nil, err
		}
		dir := filepath.Join(cfg.Dir, "eggs", selector.Session)
		if _, err := os.Stat(dir); err != nil || !eggclient.CanAccessSessionArtifact(req, dir, paths) {
			return nil, errors.New("session not found or not owned by caller")
		}
		if operation == "session_fork" {
			if eggclient.ReadEggOwner(dir) != req.SenderUserID {
				return nil, errors.New("session not found or not owned by caller")
			}
		}
		if operation == "session_prompt" || operation == "terminal_send" {
			if !wingpolicy.CanAttachSession(req.SenderUserID, req.SenderOrgRole, eggclient.ReadEggOwner(dir)) {
				return nil, errors.New("session not found or not owned by caller")
			}
		}
		// Reads and input retain legacy logical ownership after the checks
		// above. A fork launches as the current authenticated caller.
		if operation != "session_fork" {
			server.Principal = eggclient.ReadSessionPrincipal(dir)
			if server.Principal == "" {
				server.Principal = "default"
			}
		}
	}
	result, isError, protocolErr := server.callTool(ctx, operation, arguments)
	if protocolErr != nil {
		return nil, errors.New(protocolErr.Message)
	}
	if isError {
		if message, ok := result["error"].(string); ok {
			return nil, errors.New(message)
		}
		return nil, errors.New("session operation failed")
	}
	if operation == "agent_start" {
		// This result is encrypted by the tunnel handler. The relay's outer
		// request ID alone cannot correlate a saved launch receipt safely.
		var correlation struct {
			RequestID string `json:"request_id"`
		}
		if err := json.Unmarshal(arguments, &correlation); err != nil {
			return nil, err
		}
		result["request_id"] = correlation.RequestID
	}
	return result, nil
}

func configureBrowserFork(s *Server, wc *config.WingConfig, req ws.TunnelRequest, home string, sharedHost bool, wingDefault *egg.EggConfig, tools []*config.ToolConfig) {
	start := ws.PTYStart{UserID: req.SenderUserID, Email: req.SenderEmail, OrgRole: req.SenderOrgRole}
	if wc.IsAdmin(start.Email) && wingpolicy.IsMemberRole(start.OrgRole) {
		start.OrgRole = "admin"
	}
	s.identity = eggclient.BrowserEggIdentity(wc, start, home, sharedHost)
	s.allowedPaths = s.identity.AllowedPaths
	s.enforcePathBounds = len(s.allowedPaths) > 0 || wingpolicy.IsMemberRole(start.OrgRole)
	if req.SenderUserID != "" && (wc.Org != "" || sharedHost) {
		s.enforcePathBounds = wingpolicy.IsMemberRole(start.OrgRole)
	}
	s.forkIdleTimeout, _ = time.ParseDuration(wc.IdleTimeout)
	s.forkTrace = true
	s.forkTools = append([]*config.ToolConfig(nil), tools...)
	s.launchConfig = func(cwd string) (*egg.EggConfig, error) {
		launch := start
		launch.CWD = cwd
		cfg, _, err := eggclient.PrepareBrowserLaunch(wc, &launch, home, sharedHost, wingDefault)
		if err != nil {
			return nil, err
		}
		if wingpolicy.CanonicalSessionPath(launch.CWD) != cwd {
			return nil, errors.New("source working directory is outside current browser launch paths")
		}
		if wc.Audit || wingDefault != nil && wingDefault.Audit {
			copyCfg := *cfg
			cfg = &copyCfg
			cfg.Audit = true
		}
		return cfg, nil
	}
}
