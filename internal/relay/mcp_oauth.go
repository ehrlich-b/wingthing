package relay

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	maxOAuthBodyBytes = 64 << 10
	maxOAuthClients   = 1000
	maxOAuthPending   = 1000
	maxOAuthCodes     = 1000
	maxRefreshTokens  = 10000
	// DCR exposes no client-ID expiry to callers, so registrations need a long lifetime.
	// They are public identifiers (not credentials); access and refresh tokens remain short-lived.
	oauthClientTTL  = 10 * 365 * 24 * time.Hour
	refreshTokenTTL = 30 * 24 * time.Hour
)

// mcpOAuth holds ephemeral OAuth 2.1 authorization state. DCR clients are also cached here,
// but their authoritative registration is durable because Claude Code retains client IDs across
// roost restarts. Pending requests, codes, and refresh grants intentionally remain in memory.
type mcpOAuth struct {
	mu      sync.Mutex
	codes   map[string]authCode
	clients map[string]oauthClient
	pending map[string]pendingAuth
	// Refresh credentials are bearer secrets. Retain only a one-way fingerprint so an
	// accidental state dump cannot be used directly against the token endpoint.
	refresh map[[sha256.Size]byte]refreshGrant
}

type authCode struct {
	userID        string
	clientID      string
	redirectURI   string
	codeChallenge string // PKCE S256 challenge
	resource      string
	expiresAt     time.Time
}

type oauthClient struct {
	name         string
	redirectURIs []string
	expiresAt    time.Time
}

// pendingAuth holds an authorization request across the login bounce. We stash it under a
// short id and resume via /oauth/authorize?rid=<id> so the redirect target the shared login
// flow validates never contains the client's redirect_uri (which has "://" and would be
// rejected as an unsafe redirect).
type pendingAuth struct {
	clientID    string
	redirectURI string
	challenge   string
	state       string
	resource    string
	expiresAt   time.Time
}

type refreshGrant struct {
	userID    string
	clientID  string
	resource  string
	expiresAt time.Time
}

func newMCPOAuth() *mcpOAuth {
	return &mcpOAuth{
		codes:   map[string]authCode{},
		clients: map[string]oauthClient{},
		pending: map[string]pendingAuth{},
		refresh: map[[sha256.Size]byte]refreshGrant{},
	}
}

// handleOAuthServerMetadata serves RFC 8414 authorization-server metadata.
func (s *Server) handleOAuthServerMetadata(w http.ResponseWriter, r *http.Request) {
	base := s.mcpBaseURL(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                base,
		"authorization_endpoint":                base + "/oauth/authorize",
		"token_endpoint":                        base + "/oauth/token",
		"registration_endpoint":                 base + "/oauth/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
	})
}

// handleOAuthRegister is Dynamic Client Registration (RFC 7591). MCP clients self-register
// to obtain a client_id; these are public clients (no secret) that must use PKCE.
func (s *Server) handleOAuthRegister(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	r.Body = http.MaxBytesReader(w, r.Body, maxOAuthBodyBytes)
	var req struct {
		RedirectURIs            []string `json:"redirect_uris"`
		ClientName              string   `json:"client_name"`
		TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	}
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&req); err != nil || len(req.RedirectURIs) == 0 || len(req.RedirectURIs) > 10 {
		writeError(w, http.StatusBadRequest, "redirect_uris is required")
		return
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid registration document")
		return
	}
	if req.TokenEndpointAuthMethod != "" && req.TokenEndpointAuthMethod != "none" {
		writeError(w, http.StatusBadRequest, "only public clients are supported")
		return
	}
	req.ClientName = strings.TrimSpace(req.ClientName)
	if len(req.ClientName) > 200 {
		writeError(w, http.StatusBadRequest, "client_name is too long")
		return
	}
	for _, redirectURI := range req.RedirectURIs {
		if err := validateMCPRedirectURI(redirectURI); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	clientID := uuid.New().String()
	now := time.Now()
	client := oauthClient{
		name: req.ClientName, redirectURIs: append([]string(nil), req.RedirectURIs...),
		expiresAt: now.Add(oauthClientTTL),
	}
	clientCount, err := s.Store.CountMCPClientRegistrations(now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store client registration")
		return
	}
	if clientCount >= maxOAuthClients {
		writeError(w, http.StatusTooManyRequests, "client registration capacity reached")
		return
	}
	if err := s.Store.SaveMCPClientRegistration(MCPClientRegistration{
		ClientID: clientID, ClientName: client.name,
		RedirectURIs: client.redirectURIs, ExpiresAt: client.expiresAt,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "store client registration")
		return
	}
	s.mcpOAuth.mu.Lock()
	s.mcpOAuth.cleanupLocked(now)
	s.mcpOAuth.clients[clientID] = client
	s.mcpOAuth.mu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  clientID,
		"redirect_uris":              req.RedirectURIs,
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
	})
}

// handleOAuthAuthorize is the authorization endpoint (GET). It resolves the request (fresh,
// or resumed via an opaque rid after a login bounce), requires a logged-in web session
// (bouncing through the roost's existing Google login if needed), then shows a consent page.
// The user's approve/deny decision is handled by handleOAuthConsent (POST).
func (s *Server) handleOAuthAuthorize(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	q := r.URL.Query()
	var rid string
	var pa pendingAuth

	if q.Get("rid") != "" {
		rid = q.Get("rid")
		s.mcpOAuth.mu.Lock()
		s.mcpOAuth.cleanupLocked(time.Now())
		p, ok := s.mcpOAuth.pending[rid]
		s.mcpOAuth.mu.Unlock()
		if !ok || time.Now().After(p.expiresAt) {
			writeError(w, http.StatusBadRequest, "authorization request expired — retry")
			return
		}
		pa = p
	} else {
		pa = pendingAuth{
			clientID:    q.Get("client_id"),
			redirectURI: q.Get("redirect_uri"),
			challenge:   q.Get("code_challenge"),
			state:       q.Get("state"),
			resource:    q.Get("resource"),
			expiresAt:   time.Now().Add(10 * time.Minute),
		}
		if q.Get("response_type") != "code" {
			writeError(w, http.StatusBadRequest, "invalid_request: response_type must be code")
			return
		}
		if q.Get("code_challenge_method") != "S256" {
			writeError(w, http.StatusBadRequest, "invalid_request: code_challenge_method must be S256")
			return
		}
		if !validPKCEChallenge(pa.challenge) {
			writeError(w, http.StatusBadRequest, "invalid_request: invalid S256 code_challenge")
			return
		}
		if pa.clientID == "" || pa.redirectURI == "" || pa.challenge == "" || pa.resource == "" {
			writeError(w, http.StatusBadRequest, "invalid_request: client_id, redirect_uri, code_challenge, and resource are required")
			return
		}
		if pa.resource != s.mcpBaseURL(r)+"/mcp" {
			writeError(w, http.StatusBadRequest, "invalid_target: resource does not match this MCP server")
			return
		}
		if !s.oauthClientAllows(pa.clientID, pa.redirectURI) {
			writeError(w, http.StatusBadRequest, "invalid redirect_uri for client")
			return
		}
		rid = uuid.New().String()
		s.mcpOAuth.mu.Lock()
		s.mcpOAuth.cleanupLocked(time.Now())
		if len(s.mcpOAuth.pending) >= maxOAuthPending {
			s.mcpOAuth.mu.Unlock()
			writeError(w, http.StatusTooManyRequests, "authorization request capacity reached")
			return
		}
		s.mcpOAuth.pending[rid] = pa
		s.mcpOAuth.mu.Unlock()
	}

	// Require a logged-in session; otherwise bounce through login preserving the request via
	// the rid (the resume URL carries no "://" so it survives the safe-redirect check).
	user := s.sessionUser(r)
	if user == nil {
		next := "/oauth/authorize?rid=" + rid
		http.SetCookie(w, &http.Cookie{
			Name: "oauth_next", Value: next, Path: "/auth", MaxAge: 600,
			HttpOnly: true, SameSite: http.SameSiteLaxMode,
			Secure: strings.HasPrefix(s.Config.BaseURL, "https"),
		})
		http.Redirect(w, r, "/login?next="+url.QueryEscape(next), http.StatusSeeOther)
		return
	}
	if !s.mcpUserCanAuthorize(user) {
		s.deletePendingAuthorization(rid)
		writeError(w, http.StatusForbidden, "MCP access is not enabled for this user")
		return
	}

	s.renderMCPConsent(w, rid, pa, user)
}

// handleOAuthConsent handles the approve/deny decision from the consent page (POST). Approve
// mints a PKCE-bound authorization code and redirects back to the client; deny returns an
// access_denied error to the client.
func (s *Server) handleOAuthConsent(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	r.Body = http.MaxBytesReader(w, r.Body, maxOAuthBodyBytes)
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid form")
		return
	}
	rid := r.Form.Get("rid")
	s.mcpOAuth.mu.Lock()
	pa, ok := s.mcpOAuth.pending[rid]
	if ok {
		delete(s.mcpOAuth.pending, rid)
	}
	s.mcpOAuth.mu.Unlock()
	if !ok || time.Now().After(pa.expiresAt) {
		writeError(w, http.StatusBadRequest, "authorization request expired — retry")
		return
	}
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	if !s.mcpUserCanAuthorize(user) {
		writeError(w, http.StatusForbidden, "MCP access is not enabled for this user")
		return
	}
	if !s.oauthClientAllows(pa.clientID, pa.redirectURI) {
		writeError(w, http.StatusBadRequest, "invalid client")
		return
	}

	sep := "?"
	if strings.Contains(pa.redirectURI, "?") {
		sep = "&"
	}
	if r.Form.Get("action") != "approve" {
		loc := pa.redirectURI + sep + "error=access_denied"
		if pa.state != "" {
			loc += "&state=" + url.QueryEscape(pa.state)
		}
		http.Redirect(w, r, loc, http.StatusSeeOther)
		return
	}

	code := uuid.New().String()
	s.mcpOAuth.mu.Lock()
	s.mcpOAuth.cleanupLocked(time.Now())
	if len(s.mcpOAuth.codes) >= maxOAuthCodes {
		s.mcpOAuth.mu.Unlock()
		writeError(w, http.StatusTooManyRequests, "authorization code capacity reached")
		return
	}
	s.mcpOAuth.codes[code] = authCode{
		userID: user.ID, clientID: pa.clientID, redirectURI: pa.redirectURI,
		codeChallenge: pa.challenge, resource: pa.resource, expiresAt: time.Now().Add(5 * time.Minute),
	}
	s.mcpOAuth.mu.Unlock()
	s.Store.AppendAudit(user.ID, "mcp_authorized", strPtr("client="+pa.clientID))

	loc := pa.redirectURI + sep + "code=" + url.QueryEscape(code)
	if pa.state != "" {
		loc += "&state=" + url.QueryEscape(pa.state)
	}
	http.Redirect(w, r, loc, http.StatusSeeOther)
}

type mcpConsentData struct {
	ClientName string
	Email      string
	Roles      string
	Host       string
	Redirect   string
	RID        string
}

func (s *Server) renderMCPConsent(w http.ResponseWriter, rid string, pa pendingAuth, user *User) {
	client, _ := s.registeredOAuthClient(pa.clientID)
	name := client.name
	if name == "" {
		name = "An application"
	}
	email := ""
	if user.Email != nil {
		email = *user.Email
	}
	host := strings.TrimPrefix(strings.TrimPrefix(s.Config.BaseURL, "https://"), "http://")
	roles := s.mcpRolesForUser(user)
	data := mcpConsentData{
		ClientName: name,
		Email:      email,
		Roles:      strings.Join(roles, ", "),
		Host:       host,
		Redirect:   pa.redirectURI,
		RID:        rid,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := mcpConsentTmpl.Execute(w, data); err != nil {
		writeError(w, http.StatusInternalServerError, "render consent")
	}
}

var mcpConsentTmpl = template.Must(template.New("mcpConsent").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Authorize access · wingthing</title>
<style>
  :root { color-scheme: dark; }
  body { margin:0; min-height:100vh; display:flex; align-items:center; justify-content:center;
    background:#0d0f12; color:#e6e8eb; font:16px/1.5 system-ui, -apple-system, sans-serif; }
  .card { width:min(92vw, 420px); background:#16191d; border:1px solid #262b31; border-radius:14px;
    padding:28px 30px; box-shadow:0 8px 40px rgba(0,0,0,.4); }
  .brand { font-size:13px; letter-spacing:.08em; text-transform:uppercase; color:#7d8590; margin-bottom:18px; }
  h1 { font-size:20px; margin:0 0 12px; }
  p { color:#b3bac1; margin:0 0 16px; }
  code { background:#0d0f12; border:1px solid #262b31; border-radius:5px; padding:1px 6px; font-size:13px; }
  .who { font-size:14px; color:#b3bac1; background:#0d0f12; border:1px solid #262b31; border-radius:8px;
    padding:10px 12px; margin:0 0 20px; }
  .who strong { color:#e6e8eb; }
  .warn { color:#e0a458; }
  .row { display:flex; gap:10px; }
  button { flex:1; padding:11px; font-size:15px; font-weight:600; border-radius:8px; cursor:pointer; border:1px solid transparent; }
  .approve { background:#2f81f7; color:#fff; }
  .approve:hover { background:#4a90ff; }
  .deny { background:transparent; color:#b3bac1; border-color:#30363d; }
  .deny:hover { color:#e6e8eb; border-color:#484f58; }
</style></head>
<body>
  <div class="card">
    <div class="brand">wingthing</div>
    <h1>{{.ClientName}} wants access</h1>
    <p>This will let <strong>{{.ClientName}}</strong> connect to your wingthing instance at
      <code>{{.Host}}</code> and run your tools on your behalf.</p>
    <div class="who">Signed in as <strong>{{.Email}}</strong> · roles <strong>{{.Roles}}</strong><br>
      Redirects to <code>{{.Redirect}}</code></div>
    <form method="post" action="/oauth/authorize" class="row">
      <input type="hidden" name="rid" value="{{.RID}}">
      <button class="deny" name="action" value="deny" type="submit">Deny</button>
      <button class="approve" name="action" value="approve" type="submit">Approve</button>
    </form>
  </div>
</body></html>`))

// handleOAuthToken validates authorization-code or refresh grants and returns a short-lived,
// audience-bound MCP access token. Refresh tokens are opaque, single-use, and rotated.
func (s *Server) handleOAuthToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	r.Body = http.MaxBytesReader(w, r.Body, maxOAuthBodyBytes)
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid form")
		return
	}
	switch r.Form.Get("grant_type") {
	case "authorization_code":
		s.handleAuthorizationCodeGrant(w, r)
	case "refresh_token":
		s.handleRefreshTokenGrant(w, r)
	default:
		writeError(w, http.StatusBadRequest, "unsupported_grant_type")
	}
}

func (s *Server) handleAuthorizationCodeGrant(w http.ResponseWriter, r *http.Request) {
	if s.jwtKey == nil {
		writeError(w, http.StatusInternalServerError, "jwt key not initialized")
		return
	}
	code := r.Form.Get("code")
	s.mcpOAuth.mu.Lock()
	s.mcpOAuth.cleanupLocked(time.Now())
	ac, ok := s.mcpOAuth.codes[code]
	if ok && ac.redirectURI == r.Form.Get("redirect_uri") &&
		ac.clientID == r.Form.Get("client_id") &&
		ac.resource == r.Form.Get("resource") &&
		verifyPKCE(ac.codeChallenge, r.Form.Get("code_verifier")) {
		delete(s.mcpOAuth.codes, code) // single-use
	} else {
		ok = false
	}
	s.mcpOAuth.mu.Unlock()
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	if ac.resource != s.mcpBaseURL(r)+"/mcp" {
		writeError(w, http.StatusBadRequest, "invalid_target")
		return
	}
	if !s.mcpUserEnabled(ac.userID) {
		writeError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	refreshToken, err := randomOpaqueToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "generate refresh token")
		return
	}
	accessToken, exp, err := IssueMCPJWT(s.jwtKey, ac.userID, s.mcpBaseURL(r), ac.resource, ac.clientID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "issue token: "+err.Error())
		return
	}
	s.mcpOAuth.mu.Lock()
	s.mcpOAuth.cleanupLocked(time.Now())
	if len(s.mcpOAuth.refresh) >= maxRefreshTokens {
		s.mcpOAuth.mu.Unlock()
		writeError(w, http.StatusTooManyRequests, "refresh token capacity reached")
		return
	}
	s.mcpOAuth.refresh[refreshTokenKey(refreshToken)] = refreshGrant{
		userID: ac.userID, clientID: ac.clientID, resource: ac.resource,
		expiresAt: time.Now().Add(refreshTokenTTL),
	}
	s.mcpOAuth.mu.Unlock()
	s.Store.AppendAudit(ac.userID, "mcp_token_issued", strPtr("client="+ac.clientID))
	writeMCPTokenResponse(w, accessToken, refreshToken, exp)
}

func (s *Server) handleRefreshTokenGrant(w http.ResponseWriter, r *http.Request) {
	if s.jwtKey == nil {
		writeError(w, http.StatusInternalServerError, "jwt key not initialized")
		return
	}
	oldToken := r.Form.Get("refresh_token")
	oldTokenKey := refreshTokenKey(oldToken)
	clientID := r.Form.Get("client_id")
	resource := r.Form.Get("resource")
	if oldToken == "" || clientID == "" || resource != s.mcpBaseURL(r)+"/mcp" {
		writeError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	s.mcpOAuth.mu.Lock()
	s.mcpOAuth.cleanupLocked(time.Now())
	grant, ok := s.mcpOAuth.refresh[oldTokenKey]
	s.mcpOAuth.mu.Unlock()
	if !ok || grant.clientID != clientID || grant.resource != resource {
		writeError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	if !s.oauthClientRegistered(clientID) {
		writeError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	if !s.mcpUserEnabled(grant.userID) {
		writeError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	accessToken, exp, err := IssueMCPJWT(s.jwtKey, grant.userID, s.mcpBaseURL(r), grant.resource, grant.clientID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "issue token: "+err.Error())
		return
	}
	newToken, err := randomOpaqueToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "generate refresh token")
		return
	}
	s.mcpOAuth.mu.Lock()
	current, stillValid := s.mcpOAuth.refresh[oldTokenKey]
	if !stillValid || current != grant {
		s.mcpOAuth.mu.Unlock()
		writeError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	delete(s.mcpOAuth.refresh, oldTokenKey)
	s.mcpOAuth.refresh[refreshTokenKey(newToken)] = refreshGrant{
		userID: grant.userID, clientID: grant.clientID, resource: grant.resource,
		expiresAt: time.Now().Add(refreshTokenTTL),
	}
	s.mcpOAuth.mu.Unlock()
	s.Store.AppendAudit(grant.userID, "mcp_token_refreshed", strPtr("client="+grant.clientID))
	writeMCPTokenResponse(w, accessToken, newToken, exp)
}

func writeMCPTokenResponse(w http.ResponseWriter, accessToken, refreshToken string, exp time.Time) {
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  accessToken,
		"refresh_token": refreshToken,
		"token_type":    "Bearer",
		"expires_in":    int(time.Until(exp).Seconds()),
	})
}

func (s *Server) oauthClientAllows(clientID, redirectURI string) bool {
	c, ok := s.registeredOAuthClient(clientID)
	if !ok {
		return false
	}
	for _, u := range c.redirectURIs {
		if redirectURIMatches(u, redirectURI) {
			return true
		}
	}
	return false
}

func (s *Server) oauthClientRegistered(clientID string) bool {
	_, ok := s.registeredOAuthClient(clientID)
	return ok
}

func (s *Server) registeredOAuthClient(clientID string) (oauthClient, bool) {
	now := time.Now()
	s.mcpOAuth.mu.Lock()
	s.mcpOAuth.cleanupLocked(now)
	client, ok := s.mcpOAuth.clients[clientID]
	s.mcpOAuth.mu.Unlock()
	if ok {
		return client, true
	}
	reg, err := s.Store.GetMCPClientRegistration(clientID, now)
	if err != nil || reg == nil {
		return oauthClient{}, false
	}
	client = oauthClient{
		name: reg.ClientName, redirectURIs: append([]string(nil), reg.RedirectURIs...),
		expiresAt: reg.ExpiresAt,
	}
	s.mcpOAuth.mu.Lock()
	s.mcpOAuth.clients[clientID] = client
	s.mcpOAuth.mu.Unlock()
	return client, true
}

// verifyPKCE checks a code_verifier against a stored S256 challenge (RFC 7636).
func verifyPKCE(challenge, verifier string) bool {
	if !validPKCEChallenge(challenge) || !validPKCEVerifier(verifier) {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:]) == challenge
}

func validPKCEChallenge(challenge string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(challenge)
	return err == nil && len(challenge) == 43 && len(decoded) == sha256.Size
}

func validPKCEVerifier(verifier string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	for _, c := range verifier {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '.' || c == '_' || c == '~' {
			continue
		}
		return false
	}
	return true
}

func validateMCPRedirectURI(raw string) error {
	if len(raw) == 0 || len(raw) > 2048 {
		return fmt.Errorf("invalid redirect_uri")
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("invalid redirect_uri %q", raw)
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme != "http" {
		return fmt.Errorf("redirect_uri must use HTTPS or loopback HTTP")
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("HTTP redirect_uri must use localhost or a loopback address")
}

// RFC 8252 section 8.4 requires exact redirect matching except that native loopback
// redirects may vary the port selected at authorization time. All other URI components
// remain exact, so this exception cannot turn into a general wildcard redirect.
func redirectURIMatches(registered, requested string) bool {
	if registered == requested {
		return true
	}
	reg, regErr := url.Parse(registered)
	req, reqErr := url.Parse(requested)
	if regErr != nil || reqErr != nil || !isLoopbackHTTPURL(reg) || !isLoopbackHTTPURL(req) {
		return false
	}
	return strings.EqualFold(reg.Hostname(), req.Hostname()) &&
		reg.EscapedPath() == req.EscapedPath() &&
		reg.RawQuery == req.RawQuery && reg.ForceQuery == req.ForceQuery &&
		reg.Fragment == req.Fragment && reg.User == nil && req.User == nil
}

func isLoopbackHTTPURL(u *url.URL) bool {
	if u == nil || !strings.EqualFold(u.Scheme, "http") {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func randomOpaqueToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func refreshTokenKey(token string) [sha256.Size]byte {
	return sha256.Sum256([]byte(token))
}

func (o *mcpOAuth) cleanupLocked(now time.Time) {
	for id, client := range o.clients {
		if now.After(client.expiresAt) {
			delete(o.clients, id)
		}
	}
	for id, pending := range o.pending {
		if now.After(pending.expiresAt) {
			delete(o.pending, id)
		}
	}
	for code, grant := range o.codes {
		if now.After(grant.expiresAt) {
			delete(o.codes, code)
		}
	}
	for token, grant := range o.refresh {
		if now.After(grant.expiresAt) {
			delete(o.refresh, token)
		}
	}
}

func (s *Server) mcpRolesForUser(user *User) []string {
	if user == nil || user.Email == nil {
		return nil
	}
	policy := s.mcpPolicySnapshot()
	if policy == nil {
		return nil
	}
	return policy.EnabledRoles(policy.RolesForEmail(*user.Email))
}

func (s *Server) mcpUserCanAuthorize(user *User) bool {
	if user == nil {
		return false
	}
	server, _ := s.mcpSnapshot()
	return len(s.mcpRolesForUser(user)) > 0 || (s.RoostMode && server != nil && server.HasNativeTools())
}

func (s *Server) mcpUserEnabled(userID string) bool {
	user, _ := s.Store.GetUserByID(userID)
	return s.mcpUserCanAuthorize(user)
}

func (s *Server) deletePendingAuthorization(rid string) {
	s.mcpOAuth.mu.Lock()
	delete(s.mcpOAuth.pending, rid)
	s.mcpOAuth.mu.Unlock()
}
