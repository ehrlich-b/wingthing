package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	webrtcpkg "github.com/ehrlich-b/wingthing/internal/webrtc"
	pionwebrtc "github.com/pion/webrtc/v4"
)

const (
	defaultDirectMCPMaxSessions      = 8
	defaultDirectMCPMaxSpawnsPerHour = 60
	// Coordinator-derived organization identity is a lease, not a permanent
	// capability. Closing direct channels periodically forces a new access check
	// and signaling exchange without interrupting the durable agent itself.
	directMCPIdentityLease = 15 * time.Minute
)

// Keep this list explicit: a new direct operation with a new grant must fail a
// compatibility test until its default remote authority is consciously reviewed.
var defaultDirectMCPGrants = []string{
	"capabilities.read",
	"message.send", "message.read",
	"sandbox.read",
	"terminal.read", "terminal.send", "terminal.start", "terminal.rename", "terminal.stop",
	"agent.run", "agent.read", "agent.stop",
}

func knownDirectMCPGrants() map[string]bool {
	known := make(map[string]bool, len(defaultDirectMCPGrants))
	for _, grant := range defaultDirectMCPGrants {
		known[grant] = true
	}
	return known
}

func validateDirectMCPGrantConfig(wingCfg *config.WingConfig) error {
	if wingCfg == nil || wingCfg.DirectMCP == nil {
		return nil
	}
	known := knownDirectMCPGrants()
	for field, values := range map[string][]string{
		"allow_grants": wingCfg.DirectMCP.AllowGrants,
		"deny_grants":  wingCfg.DirectMCP.DenyGrants,
	} {
		for _, grant := range values {
			if !known[grant] {
				return fmt.Errorf("direct_mcp %s contains unknown direct grant %q", field, grant)
			}
		}
	}
	return nil
}

type directMCPPolicy struct {
	role              string
	grants            map[string]bool
	maxSessions       int
	maxSpawnsPerHour  int
	allowedPaths      []string
	enforcePathBounds bool
	identity          EggIdentity
}

func resolveDirectMCPPolicy(wingCfg *config.WingConfig, home string, sharedHost bool, identity webrtcpkg.PeerIdentity) (directMCPPolicy, error) {
	if wingCfg == nil {
		return directMCPPolicy{}, fmt.Errorf("wing policy is unavailable")
	}
	if strings.TrimSpace(identity.UserID) == "" {
		return directMCPPolicy{}, fmt.Errorf("authenticated user identity is required")
	}
	if wingCfg.DirectMCP != nil && wingCfg.DirectMCP.Disabled {
		return directMCPPolicy{}, fmt.Errorf("direct MCP is disabled by this wing's local policy")
	}

	role := strings.TrimSpace(identity.OrgRole)
	if wingCfg.IsAdmin(identity.Email) && (role == "" || role == "member") {
		role = "admin"
	}
	if role == "" {
		if !sharedHost {
			return directMCPPolicy{}, fmt.Errorf("missing authenticated organization role")
		}
		// Existing OAuth roosts historically encode an ordinary shared-roost user
		// with an empty org role. Preserve that deployment shape at member privilege.
		role = "member"
	}
	if role != "owner" && role != "admin" && role != "member" {
		return directMCPPolicy{}, fmt.Errorf("unsupported authenticated organization role %q", role)
	}

	if err := validateDirectMCPGrantConfig(wingCfg); err != nil {
		return directMCPPolicy{}, err
	}
	knownGrants := knownDirectMCPGrants()
	grants := make(map[string]bool, len(knownGrants))
	for grant := range knownGrants {
		grants[grant] = true
	}
	maxSessions := defaultDirectMCPMaxSessions
	maxSpawnsPerHour := defaultDirectMCPMaxSpawnsPerHour
	if configured := wingCfg.DirectMCP; configured != nil {
		if configured.MaxSessions > 0 {
			maxSessions = configured.MaxSessions
		}
		if configured.MaxSpawnsPerHour > 0 {
			maxSpawnsPerHour = configured.MaxSpawnsPerHour
		}
		if len(configured.AllowGrants) > 0 {
			grants = make(map[string]bool, len(configured.AllowGrants))
			for _, grant := range configured.AllowGrants {
				grants[grant] = true
			}
		}
		for _, grant := range configured.DenyGrants {
			delete(grants, grant)
		}
	}

	member := role == "member"
	paths := canonicalPaths(pathsForRequest(wingCfg.Paths, identity.Email, role, home))
	sealedBoundary := sharedHost || member
	return directMCPPolicy{
		role: role, grants: grants,
		maxSessions: maxSessions, maxSpawnsPerHour: maxSpawnsPerHour,
		allowedPaths: paths, enforcePathBounds: member,
		identity: EggIdentity{
			UserID: identity.UserID, Email: identity.Email,
			OrgWing: wingCfg.Org != "", SharedHost: sealedBoundary,
			AllowedPaths: append([]string(nil), paths...), SealedFS: sealedBoundary,
		},
	}, nil
}

func serveDirectMCPChannel(cfg *config.Config, wingCfg *config.WingConfig, home string, sharedHost bool, allowedKeys []config.AllowKey, admission *mcpAdmissionState, identity webrtcpkg.PeerIdentity, dc *pionwebrtc.DataChannel) {
	serveDirectMCPChannelWithPolicySource(cfg, home, sharedHost, admission, identity, dc, func() (*config.WingConfig, []config.AllowKey) {
		return wingCfg.Clone(), append([]config.AllowKey(nil), allowedKeys...)
	})
}

func serveDirectMCPChannelWithPolicySource(cfg *config.Config, home string, sharedHost bool, admission *mcpAdmissionState, identity webrtcpkg.PeerIdentity, dc *pionwebrtc.DataChannel, policySource func() (*config.WingConfig, []config.AllowKey)) {
	serveDirectMCPChannelWithPolicySourceAndLease(cfg, home, sharedHost, admission, identity, dc, policySource, directMCPIdentityLease)
}

func serveDirectMCPChannelWithPolicySourceAndLease(cfg *config.Config, home string, sharedHost bool, admission *mcpAdmissionState, identity webrtcpkg.PeerIdentity, dc *pionwebrtc.DataChannel, policySource func() (*config.WingConfig, []config.AllowKey), identityLease time.Duration) {
	actor := strings.TrimPrefix(dc.Label(), control.DirectChannelPrefix)
	if actor == dc.Label() || validateSessionName(actor) != nil || identity.UserID == "" || identityLease <= 0 {
		log.Printf("[P2P] rejected direct MCP channel %q: invalid actor or identity", dc.Label())
		_ = dc.Close()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), identityLease)
	var sendMu sync.Mutex
	requestSlots := make(chan struct{}, maxConcurrentDirectMCPRequests)
	send := func(response control.DirectResponse) {
		payload := marshalDirectMCPResponse(response)
		sendMu.Lock()
		err := dc.Send(payload)
		sendMu.Unlock()
		if err != nil {
			log.Printf("[P2P] direct MCP response: %v", err)
		}
	}
	dc.OnClose(cancel)
	go func() {
		<-ctx.Done()
		if ctx.Err() == context.DeadlineExceeded {
			log.Printf("[P2P] direct MCP identity lease expired for actor %q; reconnecting revalidates access", actor)
			_ = dc.Close()
		}
	}()
	dc.OnMessage(func(message pionwebrtc.DataChannelMessage) {
		if ctx.Err() != nil {
			return
		}
		// Keep the control plane bounded independently of SCTP implementation
		// limits. Large terminal snapshots belong in a future stream protocol.
		if len(message.Data) > maxDirectMCPEnvelopeBytes {
			send(control.DirectResponse{Version: control.ContractVersion, Error: "request exceeds 1 MiB"})
			return
		}
		var request control.DirectRequest
		if err := json.Unmarshal(message.Data, &request); err != nil {
			send(control.DirectResponse{Version: control.ContractVersion, Error: "invalid control request"})
			return
		}
		if !acquireDirectMCPRequestSlot(requestSlots) {
			send(control.DirectResponse{Version: control.ContractVersion, ID: request.ID, Error: "too many concurrent direct control requests"})
			return
		}
		go func() {
			defer func() { <-requestSlots }()
			if ctx.Err() != nil {
				return
			}
			response := control.DirectResponse{Version: control.ContractVersion, ID: request.ID}
			wingCfg, allowedKeys := policySource()
			policy, policyErr := resolveDirectMCPPolicy(wingCfg, home, sharedHost, identity)
			authorizationError := directMCPAuthorizationError(wingCfg, allowedKeys, identity.UserID)
			if policyErr != nil {
				authorizationError = policyErr.Error()
			}
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
			server := &localMCPServer{
				cfg: cfg, logs: os.Stderr,
				principal:         roostSessionPrincipal(identity.UserID),
				actor:             actor,
				surface:           control.SurfaceDirectMCP,
				grants:            policy.grants,
				maxSessions:       policy.maxSessions,
				maxSpawnsPerHour:  policy.maxSpawnsPerHour,
				admission:         admission,
				allowedPaths:      policy.allowedPaths,
				enforcePathBounds: policy.enforcePathBounds,
				identity:          policy.identity,
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

const (
	maxConcurrentDirectMCPRequests = 32
	maxDirectMCPEnvelopeBytes      = 1024 * 1024
)

func marshalDirectMCPResponse(response control.DirectResponse) []byte {
	payload, err := json.Marshal(response)
	if err == nil && len(payload) <= maxDirectMCPEnvelopeBytes {
		return payload
	}
	message := "response exceeds 1 MiB"
	if err != nil {
		message = "response could not be encoded"
	}
	fallback, _ := json.Marshal(control.DirectResponse{
		Version: control.ContractVersion,
		ID:      response.ID,
		IsError: true,
		Error:   message,
	})
	return fallback
}

func acquireDirectMCPRequestSlot(slots chan struct{}) bool {
	select {
	case slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func directMCPAuthorizationError(wingCfg *config.WingConfig, allowedKeys []config.AllowKey, userID string) string {
	if wingCfg == nil {
		return "wing policy is unavailable"
	}
	protectedUser := len(passkeysForSubject(allowedKeys, userID)) > 0
	if wingCfg.Locked && !protectedUser {
		return "direct MCP access denied by this wing's local lock policy"
	}
	if wingCfg.Locked || protectedUser {
		return "passkey authentication is required; the native direct MCP passkey ceremony is not available in this release"
	}
	return ""
}
