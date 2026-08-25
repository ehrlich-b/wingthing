package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	webrtcpkg "github.com/ehrlich-b/wingthing/internal/webrtc"
	pionwebrtc "github.com/pion/webrtc/v4"
)

func serveDirectMCPChannel(cfg *config.Config, wingCfg *config.WingConfig, home string, allowedKeys []config.AllowKey, identity webrtcpkg.PeerIdentity, dc *pionwebrtc.DataChannel) {
	actor := strings.TrimPrefix(dc.Label(), control.DirectChannelPrefix)
	if actor == dc.Label() || validateSessionName(actor) != nil || identity.UserID == "" {
		log.Printf("[P2P] rejected direct MCP channel %q: invalid actor or identity", dc.Label())
		_ = dc.Close()
		return
	}
	member := isMemberRole(identity.OrgRole)
	paths := canonicalPaths(pathsForRequest(wingCfg.Paths, identity.Email, identity.OrgRole, home))
	server := &localMCPServer{
		cfg: cfg, logs: os.Stderr,
		principal:         roostSessionPrincipal(identity.UserID),
		actor:             actor,
		surface:           control.SurfaceDirectMCP,
		allowedPaths:      paths,
		enforcePathBounds: member,
		identity: EggIdentity{
			UserID: identity.UserID, Email: identity.Email,
			OrgWing: identity.OrgRole != "", SharedHost: member,
			AllowedPaths: append([]string(nil), paths...), SealedFS: member,
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	authorizationError := directMCPAuthorizationError(wingCfg, allowedKeys, identity.UserID)
	var sendMu sync.Mutex
	send := func(response control.DirectResponse) {
		payload, err := json.Marshal(response)
		if err != nil {
			return
		}
		sendMu.Lock()
		err = dc.Send(payload)
		sendMu.Unlock()
		if err != nil {
			log.Printf("[P2P] direct MCP response: %v", err)
		}
	}
	dc.OnClose(cancel)
	dc.OnMessage(func(message pionwebrtc.DataChannelMessage) {
		// Keep the control plane bounded independently of SCTP implementation
		// limits. Large terminal snapshots belong in a future stream protocol.
		if len(message.Data) > 1024*1024 {
			send(control.DirectResponse{Version: control.ContractVersion, Error: "request exceeds 1 MiB"})
			return
		}
		var request control.DirectRequest
		if err := json.Unmarshal(message.Data, &request); err != nil {
			send(control.DirectResponse{Version: control.ContractVersion, Error: "invalid control request"})
			return
		}
		go func() {
			response := control.DirectResponse{Version: control.ContractVersion, ID: request.ID}
			if authorizationError != "" {
				response.Error = authorizationError
				send(response)
				return
			}
			tool, known := control.Lookup(request.Tool)
			if request.Version != control.ContractVersion || request.ID == "" || !known || tool.Authority != control.AuthorityWing || !tool.Supports(control.SurfaceDirectMCP) {
				response.Error = fmt.Sprintf("unsupported %s control operation %q", request.Version, request.Tool)
				send(response)
				return
			}
			arguments := request.Arguments
			if len(arguments) == 0 {
				arguments = json.RawMessage(`{}`)
			}
			result, isError, protocolErr := server.callTool(ctx, request.Tool, arguments)
			response.Result = result
			response.IsError = isError
			if protocolErr != nil {
				response.Error = protocolErr.Message
			}
			send(response)
		}()
	})
}

func directMCPAuthorizationError(wingCfg *config.WingConfig, allowedKeys []config.AllowKey, userID string) string {
	protectedUser := len(passkeysForSubject(allowedKeys, userID)) > 0
	if wingCfg.Locked && !protectedUser {
		return "direct MCP access denied by this wing's local lock policy"
	}
	if wingCfg.Locked || protectedUser {
		return "passkey authentication is required; the native direct MCP passkey ceremony is not available in this release"
	}
	return ""
}
