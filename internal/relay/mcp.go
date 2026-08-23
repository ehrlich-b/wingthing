package relay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/mcp"
)

type mcpIdentityKey struct{}

type mcpIdentity struct {
	UserID   string
	Email    string
	ClientID string
	Roles    []string
}

// EnableMCP wires owner-scoped native controls and optional role-scoped executable
// tools onto POST /mcp with OAuth bearer authentication.
func (s *Server) EnableMCP(runner *egg.ToolRunner, policy *mcp.Policy, nativeTools ...mcp.NativeTool) {
	s.mcpOAuth = newMCPOAuth()
	s.mcpMu.Lock()
	s.mcpNativeTools = append([]mcp.NativeTool(nil), nativeTools...)
	s.mcpMu.Unlock()
	s.ReloadMCP(runner, policy)

	s.mux.HandleFunc("POST /mcp", s.handleMCP)
	s.mux.HandleFunc("GET /.well-known/oauth-protected-resource", s.handleOAuthProtectedResource)
	s.mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.handleOAuthServerMetadata)
	s.mux.HandleFunc("POST /oauth/register", s.handleOAuthRegister)
	s.mux.HandleFunc("GET /oauth/authorize", s.handleOAuthAuthorize)
	s.mux.HandleFunc("POST /oauth/authorize", s.handleOAuthConsent)
	s.mux.HandleFunc("POST /oauth/token", s.handleOAuthToken)
}

// ReloadMCP atomically replaces the authorization policy and tool runner. In-flight
// requests finish on the previous immutable snapshot; new requests see both replacements.
func (s *Server) ReloadMCP(runner *egg.ToolRunner, policy *mcp.Policy) {
	s.mcpMu.RLock()
	nativeTools := append([]mcp.NativeTool(nil), s.mcpNativeTools...)
	s.mcpMu.RUnlock()
	// The MCP server reads the caller's roles from the request context, which handleMCP
	// populates after authenticating the bearer token.
	server := mcp.NewServer(runner, policy, func(r *http.Request) []string {
		identity, _ := r.Context().Value(mcpIdentityKey{}).(mcpIdentity)
		return identity.Roles
	})
	server.SetCallObserver(s.auditMCPCall)
	server.SetNativeTools(nativeTools, func(r *http.Request) mcp.Principal {
		identity, _ := r.Context().Value(mcpIdentityKey{}).(mcpIdentity)
		return mcp.Principal{
			UserID: identity.UserID, Email: identity.Email,
			ClientID: identity.ClientID, Roles: append([]string(nil), identity.Roles...),
		}
	})
	server.SetNativeCallObserver(s.auditNativeMCPCall)
	server.SetCallEnv(func(r *http.Request) map[string]string {
		identity, _ := r.Context().Value(mcpIdentityKey{}).(mcpIdentity)
		return map[string]string{
			"WT_MCP_USER":      identity.UserID,
			"WT_MCP_EMAIL":     identity.Email,
			"WT_MCP_ROLES":     strings.Join(identity.Roles, ","),
			"WT_MCP_CLIENT_ID": identity.ClientID,
		}
	})
	s.mcpMu.Lock()
	s.mcpServer = server
	s.mcpPolicy = policy
	s.mcpMu.Unlock()
}

// MCPEnabled reports whether the MCP surface is configured.
func (s *Server) MCPEnabled() bool {
	server, _ := s.mcpSnapshot()
	return server != nil
}

func (s *Server) mcpSnapshot() (*mcp.Server, *mcp.Policy) {
	s.mcpMu.RLock()
	defer s.mcpMu.RUnlock()
	return s.mcpServer, s.mcpPolicy
}

func (s *Server) mcpPolicySnapshot() *mcp.Policy {
	_, policy := s.mcpSnapshot()
	return policy
}

// handleMCP authenticates the caller, resolves their role from the policy, and delegates
// to the MCP server. An unauthenticated request gets the MCP 401 challenge so the client
// knows where to begin OAuth.
func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	// Origin validation is a transport requirement and must run before bearer authentication.
	// Otherwise a cross-origin request without a token receives an OAuth challenge instead of
	// the required DNS-rebinding rejection.
	if !mcp.OriginAllowed(r) {
		http.Error(w, "invalid origin", http.StatusForbidden)
		return
	}
	server, _ := s.mcpSnapshot()
	if server == nil {
		http.NotFound(w, r)
		return
	}
	claims := s.mcpBearerClaims(r)
	if claims == nil {
		s.mcpChallenge(w, r)
		return
	}
	u, _ := s.Store.GetUserByID(claims.Subject)
	if u == nil {
		writeError(w, http.StatusForbidden, "MCP access is not enabled for this user")
		return
	}
	roles := s.mcpRolesForUser(u)
	if len(roles) == 0 && !(s.RoostMode && server.HasNativeTools()) {
		writeError(w, http.StatusForbidden, "MCP access is not enabled for this user")
		return
	}
	email := ""
	if u.Email != nil {
		email = *u.Email
	}
	ctx := context.WithValue(r.Context(), mcpIdentityKey{}, mcpIdentity{
		UserID: claims.Subject, Email: email, ClientID: claims.ClientID, Roles: roles,
	})
	server.ServeHTTP(w, r.WithContext(ctx))
}

func (s *Server) auditNativeMCPCall(r *http.Request, tool string, arguments json.RawMessage, result map[string]any, isError bool) {
	identity, _ := r.Context().Value(mcpIdentityKey{}).(mcpIdentity)
	if identity.UserID == "" {
		return
	}
	detail := map[string]any{
		"owner_id": identity.UserID,
		"actor_id": identity.ClientID,
		"roles":    identity.Roles,
		"tool":     tool,
		"is_error": isError,
	}
	sum := sha256.Sum256(arguments)
	detail["argument_sha256"] = hex.EncodeToString(sum[:])
	for _, key := range []string{"session", "run_id", "task_id", "message_id"} {
		if value, ok := result[key].(string); ok && value != "" {
			detail["target"] = value
			break
		}
	}
	raw, _ := json.Marshal(detail)
	s.Store.AppendAudit(identity.UserID, "mcp_control_call", strPtr(string(raw)))
}

func (s *Server) auditMCPCall(r *http.Request, tool string, args []string, resp egg.ToolResponse) {
	identity, _ := r.Context().Value(mcpIdentityKey{}).(mcpIdentity)
	if identity.UserID == "" {
		return
	}
	detail := map[string]any{
		"client_id": identity.ClientID,
		"roles":     identity.Roles,
		"tool":      tool,
		"exit_code": resp.ExitCode,
		"is_error":  resp.ExitCode != 0 || resp.Error != "",
	}
	rawArgs, _ := json.Marshal(args)
	if len(rawArgs) <= 16<<10 {
		detail["args"] = args
	} else {
		sum := sha256.Sum256(rawArgs)
		detail["args_truncated"] = true
		detail["args_sha256"] = hex.EncodeToString(sum[:])
	}
	raw, _ := json.Marshal(detail)
	s.Store.AppendAudit(identity.UserID, "mcp_tool_call", strPtr(string(raw)))
}

// mcpBearerClaims accepts only a dedicated, audience-bound MCP JWT. General wing and
// database API tokens must never cross into the MCP resource server.
func (s *Server) mcpBearerClaims(r *http.Request) *MCPClaims {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return nil
	}
	token := strings.TrimPrefix(auth, "Bearer ")
	if s.JWTPubKey() == nil {
		return nil
	}
	base := s.mcpBaseURL(r)
	claims, err := ValidateMCPJWT(s.JWTPubKey(), token, base, base+"/mcp")
	if err != nil {
		return nil
	}
	return claims
}

// mcpChallenge emits the 401 that points an MCP client at our OAuth metadata (RFC 9728).
func (s *Server) mcpChallenge(w http.ResponseWriter, r *http.Request) {
	base := s.mcpBaseURL(r)
	w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata=%q`, base+"/.well-known/oauth-protected-resource"))
	writeError(w, http.StatusUnauthorized, "authentication required")
}

// mcpBaseURL is the roost's externally-reachable base URL (no trailing slash).
func (s *Server) mcpBaseURL(r *http.Request) string {
	if s.Config.BaseURL != "" {
		return strings.TrimRight(s.Config.BaseURL, "/")
	}
	proto := "https"
	if r.TLS == nil {
		proto = "http"
	}
	return proto + "://" + r.Host
}

// handleOAuthProtectedResource serves RFC 9728 protected-resource metadata: it names this
// MCP resource and which authorization server issues tokens for it.
func (s *Server) handleOAuthProtectedResource(w http.ResponseWriter, r *http.Request) {
	base := s.mcpBaseURL(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":              base + "/mcp",
		"authorization_servers": []string{base},
	})
}
