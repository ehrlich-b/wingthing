package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

var browserConversationAdmission = newMCPAdmissionState()

func sessionLifecycleSummary(ctx context.Context, cfg *config.Config, id string) map[string]any {
	view, err := readSessionLifecycleView(ctx, cfg, id, 0, 1)
	if err != nil {
		return nil
	}
	return map[string]any{"state": view.State, "state_source": view.StateSource, "ready": view.Ready, "process_alive": view.ProcessAlive, "head_cursor": view.HeadCursor}
}

// browserSessionControl is a narrow adapter over the same typed MCP handlers.
// Authentication/passkey/purpose checks occur before this dispatcher; artifact
// access is checked independently before selecting a legacy session principal.
func browserSessionControl(ctx context.Context, cfg *config.Config, wc *config.WingConfig, req ws.TunnelRequest, operation string, arguments json.RawMessage, home string, sharedHost bool) (map[string]any, error) {
	switch operation {
	case "session_status", "session_read", "session_wait", "session_prompt", "terminal_send", "conversation_list", "conversation_bootstrap", "conversation_read", "conversation_checkpoint", "conversation_wake", "agent_start":
	default:
		return nil, errors.New("unsupported browser session operation")
	}
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	if len(arguments) > 1<<20 {
		return nil, errors.New("session operation arguments exceed 1 MiB")
	}
	paths := canonicalPaths(pathsForRequest(wc.Paths, req.SenderEmail, req.SenderOrgRole, home))
	server := &localMCPServer{cfg: cfg, logs: os.Stderr, principal: roostSessionPrincipal(req.SenderUserID), actor: "browser", surface: control.SurfaceHTTPMCP,
		grants: grantSet(defaultDirectMCPGrants), maxSessions: defaultDirectMCPMaxSessions, maxSpawnsPerHour: defaultDirectMCPMaxSpawnsPerHour, admission: browserConversationAdmission,
		allowedPaths: paths, enforcePathBounds: len(paths) > 0 || isMemberFiltered(req), identity: EggIdentity{UserID: req.SenderUserID, Email: req.SenderEmail, OrgWing: wc.Org != "", SharedHost: sharedHost, SealedFS: sharedHost, AllowedPaths: paths}}
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
	if operation == "session_status" || operation == "session_read" || operation == "session_wait" || (operation == "session_prompt" || operation == "terminal_send") {
		var selector struct {
			Session string `json:"session"`
		}
		if err := json.Unmarshal(arguments, &selector); err != nil {
			return nil, err
		}
		if err := validateSessionID(selector.Session); err != nil {
			return nil, err
		}
		dir := filepath.Join(cfg.Dir, "eggs", selector.Session)
		if _, err := os.Stat(dir); err != nil || !canAccessSessionArtifact(req, dir, paths) {
			return nil, errors.New("session not found or not owned by caller")
		}
		if operation == "session_prompt" || operation == "terminal_send" {
			if !canAttachSession(req.SenderUserID, req.SenderOrgRole, readEggOwner(dir)) {
				return nil, errors.New("session not found or not owned by caller")
			}
		}
		// Legacy browser sessions have no MCP principal. Preserve access only
		// after the current browser ownership and workspace checks above.
		server.principal = readSessionPrincipal(dir)
		if server.principal == "" {
			server.principal = "default"
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
