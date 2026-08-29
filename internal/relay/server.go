package relay

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/ehrlich-b/wingthing/internal/mcp"
	"github.com/ehrlich-b/wingthing/internal/ws"
	"github.com/ehrlich-b/wingthing/web"
)

type ServerConfig struct {
	BaseURL              string
	AppHost              string // e.g. "app.wingthing.ai" — serve SPA at root
	WSHost               string // e.g. "ws.wingthing.ai" — WebSocket only
	JWTKey               string // PEM or base64-DER EC P-256 private key; overrides DB-stored key
	InternalSecret       string // optional secret for /internal/*; distinct from JWT signing material
	GitHubClientID       string
	GitHubClientSecret   string
	GoogleClientID       string
	GoogleClientSecret   string
	SMTPHost             string
	SMTPPort             string
	SMTPUser             string
	SMTPPass             string
	SMTPFrom             string
	NodeRole             string // "login", "edge", or "" (single node)
	LoginNodeAddr        string // internal address of login node (for edge nodes)
	FlyMachineID         string // from FLY_MACHINE_ID env var
	FlyRegion            string // from FLY_REGION env var
	FlyAppName           string // from FLY_APP_NAME env var
	HeroVideo            string // path to hero video file on disk (not embedded)
	RelayPolicy          string // "legacy" or "direct-free"
	RelayMigrationBefore time.Time
	// RoostAllowedEmails is an optional, exact email allowlist for private OAuth
	// gateways and all-in-one roosts.
	// Empty preserves the historical behavior where completing OAuth enrolls a
	// user. Local mode does not consult this list.
	RoostAllowedEmails []string
}

type Server struct {
	Store          *RelayStore
	Config         ServerConfig
	DevTemplateDir string // if set, re-read templates from disk on each request
	DevMode        bool   // if set, auto-claim device codes with test-user
	LocalMode      bool   // if set, bypass auth — single-user, zero-config
	RoostMode      bool   // if set, enrolled authenticated users can access the embedded service wing
	localUser      *User
	Wings          *WingRegistry
	PTY            *PTYRoutes
	Bandwidth      *BandwidthMeter
	RateLimit      *RateLimiter
	jwtKey         *ecdsa.PrivateKey
	mux            *http.ServeMux
	planMu         sync.Mutex // serializes org subscription changes with deletion

	// Latest release version cache (fetched from GitHub)
	latestVersion          string
	latestVersionAt        time.Time
	latestVersionNextFetch time.Time
	latestVersionFetching  bool
	latestVersionFetch     func(context.Context) (string, error)
	latestVersionMu        sync.Mutex

	// All browser WebSocket connections (for shutdown broadcast)
	browserMu    sync.Mutex
	browserConns map[*websocket.Conn]*browserConnection

	// Tunnel request tracking (requestID → browser WebSocket)
	tunnelMu       sync.Mutex
	tunnelRequests map[tunnelRequestKey]pendingTunnelRequest

	// Cluster routing (multi-node)
	WingMap *WingMap

	// Edge node: reverse proxy to login node + session/entitlement caches
	loginProxy       http.Handler
	sessionCache     *SessionCache
	EntitlementCache *EntitlementCache

	// MCP surface (roost mode): owner-scoped control operations plus optional
	// role-scoped executable tools, all behind the same OAuth resource server.
	mcpMu           sync.RWMutex
	mcpServer       *mcp.Server
	mcpPolicy       *mcp.Policy
	mcpNativeTools  []mcp.NativeTool
	mcpOAuth        *mcpOAuth
	oauthHTTPClient *http.Client

	// In-flight passkey ceremonies are server-local, short-lived, and bounded.
	// They are authentication state, so they must not leak across Server
	// instances or survive indefinitely when a browser abandons setup.
	passkeyMu       sync.Mutex
	passkeySessions map[string]passkeyRegistrationSession

	ntfyNonceMu    sync.Mutex
	ntfyNonceSeen  map[string]bool
	ntfyNonceOrder []string
	ntfyNonceNext  int
}

const maxRelayRequestBodyBytes int64 = 1 << 20

func NewServer(store *RelayStore, cfg ServerConfig) *Server {
	s := &Server{
		Store:           store,
		Config:          cfg,
		Wings:           NewWingRegistry(),
		PTY:             NewPTYRoutes(),
		mux:             http.NewServeMux(),
		browserConns:    make(map[*websocket.Conn]*browserConnection),
		tunnelRequests:  make(map[tunnelRequestKey]pendingTunnelRequest),
		oauthHTTPClient: newOAuthHTTPClient(),
		passkeySessions: make(map[string]passkeyRegistrationSession),
		ntfyNonceSeen:   make(map[string]bool),
	}

	// API routes
	s.mux.HandleFunc("POST /auth/device", s.handleAuthDevice)
	s.mux.HandleFunc("POST /auth/token", s.handleAuthToken)
	s.mux.HandleFunc("POST /auth/claim", s.handleAuthClaim)
	s.mux.HandleFunc("POST /auth/refresh", s.handleAuthRefresh)
	s.mux.HandleFunc("GET /health", s.handleHealth)
	s.mux.HandleFunc("GET /auth/check", s.handleAuthCheck)
	// Relay: worker WebSocket
	s.mux.HandleFunc("GET /ws/wing", s.handleWingWS)
	s.mux.HandleFunc("GET /ws/pty", s.handlePTYWS)
	s.mux.HandleFunc("GET /ws/relay", s.handlePTYWS)

	// App dashboard API (cookie auth)
	s.mux.HandleFunc("GET /api/app/me", s.handleAppMe)
	s.mux.HandleFunc("GET /api/app/wings", s.handleAppWings)
	s.mux.HandleFunc("GET /ws/app", s.handleAppWS)
	s.mux.HandleFunc("GET /api/app/usage", s.handleAppUsage)
	s.mux.HandleFunc("POST /api/app/upgrade", s.handleAppUpgrade)
	s.mux.HandleFunc("POST /api/app/downgrade", s.handleAppDowngrade)
	// Wing detail page API
	s.mux.HandleFunc("PUT /api/app/wings/{wingID}/label", s.handleWingLabel)
	s.mux.HandleFunc("DELETE /api/app/wings/{wingID}/label", s.handleDeleteWingLabel)

	// CLI API (Bearer token auth)
	s.mux.HandleFunc("GET /api/app/resolve-email", s.handleResolveEmail)

	// ntfy push notifications (cookie auth)
	s.mux.HandleFunc("GET /api/app/ntfy", s.handleNtfyGet)
	s.mux.HandleFunc("POST /api/app/ntfy", s.handleNtfySet)
	s.mux.HandleFunc("POST /api/app/ntfy/test", s.handleNtfyTest)
	s.mux.HandleFunc("POST /api/app/ntfy/generate", s.handleNtfyGenerate)

	// Passkey management (cookie auth)
	s.mux.HandleFunc("POST /api/app/passkey/register/begin", s.handlePasskeyRegisterBegin)
	s.mux.HandleFunc("POST /api/app/passkey/register/finish", s.handlePasskeyRegisterFinish)
	s.mux.HandleFunc("GET /api/app/passkey", s.handlePasskeyList)
	s.mux.HandleFunc("DELETE /api/app/passkey/{id}", s.handlePasskeyDelete)

	// Claim page
	s.mux.HandleFunc("GET /auth/claim", s.handleClaimPage)

	// Static files
	s.mux.HandleFunc("GET /install.sh", s.handleInstallScript)
	s.mux.HandleFunc("GET /hero.mp4", s.handleHeroVideo)

	// Web pages
	s.mux.HandleFunc("GET /{$}", s.handleHome)
	s.mux.HandleFunc("GET /login", s.handleLogin)
	s.mux.HandleFunc("GET /install", s.handleInstallPage)
	s.mux.HandleFunc("GET /docs", s.handleDocs)
	s.mux.HandleFunc("GET /patterns", s.handlePatterns)
	s.mux.HandleFunc("GET /patterns/SKILL.md", s.handlePatternSkill)
	s.mux.HandleFunc("GET /patterns/{slug}/INSTRUCTIONS.md", s.handlePatternInstructions)
	s.mux.HandleFunc("GET /terms", s.handleTerms)
	s.mux.HandleFunc("GET /privacy", s.handlePrivacy)
	s.mux.HandleFunc("GET /abuse", s.handleAbuse)
	s.mux.HandleFunc("GET /self-host", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/docs", http.StatusMovedPermanently)
	})
	// Web auth
	s.mux.HandleFunc("GET /auth/github", s.handleGitHubAuth)
	s.mux.HandleFunc("GET /auth/github/callback", s.handleGitHubCallback)
	s.mux.HandleFunc("GET /auth/google", s.handleGoogleAuth)
	s.mux.HandleFunc("GET /auth/google/callback", s.handleGoogleCallback)
	s.mux.HandleFunc("POST /auth/magic", s.handleMagicLink)
	s.mux.HandleFunc("GET /auth/magic/verify", s.handleMagicVerify)
	s.mux.HandleFunc("POST /auth/logout", s.handleLogout)
	s.mux.HandleFunc("GET /auth/dev", s.handleDevLogin)

	// Org management API (cookie auth)
	s.mux.HandleFunc("POST /api/orgs", s.handleCreateOrg)
	s.mux.HandleFunc("GET /api/orgs", s.handleListOrgs)
	s.mux.HandleFunc("GET /api/orgs/{orgID}", s.handleGetOrg)
	s.mux.HandleFunc("DELETE /api/orgs/{orgID}", s.handleDeleteOrg)
	s.mux.HandleFunc("GET /api/orgs/{orgID}/members", s.handleListOrgMembers)
	s.mux.HandleFunc("POST /api/orgs/{orgID}/invite", s.handleOrgInvite)
	s.mux.HandleFunc("DELETE /api/orgs/{orgID}/members/{userID}", s.handleRemoveOrgMember)
	s.mux.HandleFunc("POST /api/orgs/{orgID}/upgrade", s.handleOrgUpgrade)
	s.mux.HandleFunc("POST /api/orgs/{orgID}/cancel", s.handleOrgCancel)
	s.mux.HandleFunc("POST /api/orgs/{orgID}/invites/{token}/revoke", s.handleRevokeInvite)
	s.mux.HandleFunc("GET /invite/{token}", s.handleAcceptInvite)
	s.mux.HandleFunc("POST /invite/{token}", s.handleConsumeInvite)

	s.registerStaticRoutes()
	s.registerInternalRoutes()
	return s
}

// InitJWTKey loads the ES256 signing key. In server mode (non-local, non-roost),
// WT_JWT_KEY env var is required and the app refuses to start without it.
// In local/roost mode, the caller provides the key via Config.JWTKey (from wing.yaml).
func (s *Server) InitJWTKey() error {
	if s.Config.JWTKey != "" {
		key, err := ParseECKeyFromEnv(s.Config.JWTKey)
		if err != nil {
			return fmt.Errorf("jwt key: %w", err)
		}
		s.jwtKey = key
		return nil
	}
	// No key provided — local/roost callers set it before calling InitJWTKey.
	// If we get here in server mode, that's a fatal misconfiguration.
	return nil
}

// SetJWTKey directly sets the signing key (used by roost mode after loading from wing.yaml).
func (s *Server) SetJWTKey(key *ecdsa.PrivateKey) {
	s.jwtKey = key
}

// JWTKey returns the ES256 private key for JWT signing.
func (s *Server) JWTKey() *ecdsa.PrivateKey { return s.jwtKey }

// JWTPubKey returns the ES256 public key for JWT verification.
func (s *Server) JWTPubKey() *ecdsa.PublicKey {
	if s.jwtKey == nil {
		return nil
	}
	return &s.jwtKey.PublicKey
}

func (s *Server) registerStaticRoutes() {
	sub, _ := fs.Sub(web.FS, "dist")
	fileServer := http.FileServer(http.FS(sub))
	s.mux.Handle("GET /app/", http.StripPrefix("/app/", fileServer))
	s.mux.HandleFunc("GET /app", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/app/", http.StatusMovedPermanently)
	})
	// Serve /assets/ for app.wingthing.ai SPA (Vite puts hashed bundles here)
	s.mux.Handle("GET /assets/", fileServer)
}

func stripPort(host string) string {
	parsed, err := url.Parse("//" + strings.TrimSpace(host))
	if err == nil && parsed.Hostname() != "" {
		return parsed.Hostname()
	}
	return strings.TrimSpace(host)
}

func (s *Server) SetLocalUser(u *User) { s.localUser = u }

// browserWebSocketAcceptOptions permits same-host browser handshakes plus the
// configured site/app origins used by historical split-host deployments.
// Native clients without an Origin header remain compatible. Arbitrary web
// origins are never accepted, including on hosted deployments.
func (s *Server) browserWebSocketAcceptOptions() *websocket.AcceptOptions {
	options := &websocket.AcceptOptions{}
	if s.LocalMode {
		return options
	}
	options.OriginPatterns = s.configuredBrowserOrigins()
	return options
}

func (s *Server) configuredBrowserOrigins() []string {
	origins := make([]string, 0, 2)
	seen := make(map[string]bool)
	baseScheme := "https"
	if parsed, err := url.Parse(s.Config.BaseURL); err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		baseScheme = parsed.Scheme
		origin := parsed.Scheme + "://" + parsed.Host
		origins = append(origins, origin)
		seen[origin] = true
	}
	if s.Config.AppHost != "" {
		origin := baseScheme + "://" + s.Config.AppHost
		if !seen[origin] {
			origins = append(origins, origin)
		}
	}
	return origins
}

// IsEdge returns true if this node is an edge relay (no SQLite).
func (s *Server) IsEdge() bool { return s.Config.NodeRole == "edge" }

// IsLogin returns true if this node is the login/DB node.
func (s *Server) IsLogin() bool { return s.Config.NodeRole == "login" }

// MachineID returns this node's unique machine identifier.
func (s *Server) MachineID() string { return s.Config.FlyMachineID }

// SetLoginProxy sets the reverse proxy used by edge nodes to forward requests to the login node.
func (s *Server) SetLoginProxy(p http.Handler) { s.loginProxy = p }

// SetSessionCache sets the session cache for edge nodes.
func (s *Server) SetSessionCache(sc *SessionCache) { s.sessionCache = sc }

// GetSessionCache returns the session cache (edge nodes only).
func (s *Server) GetSessionCache() *SessionCache { return s.sessionCache }

type browserConnection struct {
	userID        string
	relayNotified map[string]bool
}

const maxRelayNotificationResources = 1024

func (s *Server) trackBrowser(conn *websocket.Conn, userID string) {
	s.browserMu.Lock()
	s.browserConns[conn] = &browserConnection{userID: userID, relayNotified: make(map[string]bool)}
	s.browserMu.Unlock()
}

func (s *Server) untrackBrowser(conn *websocket.Conn) {
	s.browserMu.Lock()
	delete(s.browserConns, conn)
	s.browserMu.Unlock()
}

// browserRelayPayloadAccess re-evaluates entitlement for long-lived sockets.
// The bools report allowed and whether this denial episode still needs one
// explicit protocol error; payload chunks themselves are always dropped.
func (s *Server) browserRelayPayloadAccess(conn *websocket.Conn, resource string) (bool, bool) {
	s.browserMu.Lock()
	tracked := s.browserConns[conn]
	s.browserMu.Unlock()
	if tracked == nil {
		return false, false
	}
	allowed := s.relayAccess(tracked.userID).Allowed
	s.browserMu.Lock()
	defer s.browserMu.Unlock()
	current := s.browserConns[conn]
	if current == nil {
		return false, false
	}
	if allowed {
		clear(current.relayNotified)
		return true, false
	}
	notify := !current.relayNotified[resource]
	if notify && len(current.relayNotified) >= maxRelayNotificationResources {
		clear(current.relayNotified)
	}
	current.relayNotified[resource] = true
	return false, notify
}

func (s *Server) browserUserID(conn *websocket.Conn) string {
	s.browserMu.Lock()
	defer s.browserMu.Unlock()
	if tracked := s.browserConns[conn]; tracked != nil {
		return tracked.userID
	}
	return ""
}

// GracefulShutdown sends relay.restart to all connected WebSockets, then shuts down the HTTP server.
func (s *Server) GracefulShutdown(httpSrv *http.Server, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	msg := ws.RelayRestart{Type: ws.TypeRelayRestart}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("encode relay restart: %w", err)
	}

	// Broadcast to all wings
	s.Wings.BroadcastAll(ctx, data)

	// Broadcast to all browser connections (app dashboard + PTY)
	s.browserMu.Lock()
	browsers := make([]*websocket.Conn, 0, len(s.browserConns))
	for conn := range s.browserConns {
		browsers = append(browsers, conn)
	}
	s.browserMu.Unlock()

	for _, conn := range browsers {
		writeCtx, wcancel := context.WithTimeout(ctx, 2*time.Second)
		if err := conn.Write(writeCtx, websocket.MessageText, data); err != nil {
			log.Printf("broadcast relay restart to browser: %v", err)
		}
		wcancel()
	}

	log.Printf("sent relay.restart to %d wings, %d browsers", len(s.Wings.All()), len(browsers))

	// Close all wing connections
	s.Wings.CloseAll()

	// Graceful HTTP shutdown
	return httpSrv.Shutdown(ctx)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// The portal is an interactive control surface and must never be embedded by
	// another origin. Apply these headers before any early rejection so error and
	// authentication responses keep the same browser boundary as successful pages.
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")

	// Authenticated control responses and credential-bearing flows must not be
	// retained by a browser or intermediary cache. Apply this before routing so
	// early errors, edge-proxied responses, and successful handlers all share the
	// same boundary. Vary documents both authentication mechanisms used here.
	path := r.URL.Path
	if isPrivateControlPath(path) {
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Add("Vary", "Cookie")
		w.Header().Add("Vary", "Authorization")
	}

	// Bound every HTTP request before it reaches a decoder or FormValue. Most
	// endpoints consume only a few kilobytes; without a shared cap, one slow or
	// oversized auth/org request can retain a server goroutine and arbitrary
	// buffering. WebSocket upgrades have empty bodies and are unaffected.
	if r.ContentLength > maxRelayRequestBodyBytes {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, maxRelayRequestBodyBytes)
	}
	// LocalMode deliberately bypasses login, so it is safe only as a loopback
	// service. Reject hostile Host headers as well as relying on the CLI's bind
	// check: otherwise DNS rebinding can make an attacker-controlled origin look
	// same-origin while the TCP connection lands on 127.0.0.1.
	if s.LocalMode && !localRequestHost(r.Host) {
		http.Error(w, "local mode accepts only localhost requests", http.StatusForbidden)
		return
	}
	if s.LocalMode && !localBrowserMutationAllowed(r) {
		http.Error(w, "local mode rejects cross-origin browser mutations", http.StatusForbidden)
		return
	}
	if !s.LocalMode && !s.hostedBrowserMutationAllowed(r) {
		http.Error(w, "hosted mode rejects cross-origin browser mutations", http.StatusForbidden)
		return
	}
	host := stripPort(r.Host)

	// Rate limit auth and mutating API endpoints
	if s.RateLimit != nil && s.shouldRateLimit(r.Method, path) {
		ip := clientIP(r)
		if !s.RateLimit.Allow(ip) {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
	}

	// Edge node proxying: keep only connection-local WebSockets, health, and
	// authenticated node APIs on the edge. HTML and its hashed assets must come
	// from the same login-node release; serving assets from each edge makes a
	// rolling deployment pair a new index with an old bundle (or vice versa).
	if s.IsEdge() && s.loginProxy != nil {
		if strings.HasPrefix(path, "/ws/") || strings.HasPrefix(path, "/internal/") || path == "/health" {
			s.mux.ServeHTTP(w, r)
			return
		}
		s.loginProxy.ServeHTTP(w, r)
		return
	}

	// app.wingthing.ai: SPA at root, plus API/auth/ws/assets
	if s.Config.AppHost != "" && strings.EqualFold(host, stripPort(s.Config.AppHost)) {
		if strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/auth/") ||
			strings.HasPrefix(path, "/ws/") || strings.HasPrefix(path, "/app/") ||
			strings.HasPrefix(path, "/assets/") ||
			path == "/mcp" || strings.HasPrefix(path, "/oauth/") ||
			strings.HasPrefix(path, "/.well-known/") {
			s.mux.ServeHTTP(w, r)
			return
		}
		s.serveAppIndex(w, r)
		return
	}

	// ws.wingthing.ai: WebSocket + health + auth check (wings validate tokens before connecting)
	if s.Config.WSHost != "" && strings.EqualFold(host, stripPort(s.Config.WSHost)) {
		if strings.HasPrefix(path, "/ws/") || path == "/auth/check" || path == "/health" {
			s.mux.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
		return
	}

	// The default-host pages render a user-specific navigation bar when a
	// session cookie is present. Never let that representation enter a shared
	// cache. Anonymous pages remain cacheable, but Vary prevents an intermediary
	// from serving that representation to a request carrying a session. Login is
	// always private because it may set short-lived OAuth flow cookies.
	if r.Method == http.MethodGet && isDynamicSitePage(path) {
		w.Header().Add("Vary", "Cookie")
		_, sessionCookieErr := r.Cookie(sessionCookieName)
		if path == "/login" || sessionCookieErr == nil {
			w.Header().Set("Cache-Control", "private, no-store")
		} else if r.URL.RawQuery != "" {
			w.Header().Set("Cache-Control", "public, max-age=60, s-maxage=60")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=900, s-maxage=900")
		}
	}
	s.mux.ServeHTTP(w, r)
}

func isPrivateControlPath(path string) bool {
	return strings.HasPrefix(path, "/api/") ||
		strings.HasPrefix(path, "/auth/") ||
		strings.HasPrefix(path, "/oauth/") ||
		strings.HasPrefix(path, "/internal/") ||
		strings.HasPrefix(path, "/invite/") ||
		path == "/mcp"
}

func isDynamicSitePage(path string) bool {
	switch path {
	case "/", "/login", "/docs", "/patterns", "/terms", "/privacy", "/abuse":
		return true
	default:
		return false
	}
}

func localRequestHost(hostport string) bool {
	host := hostport
	if parsedHost, _, err := net.SplitHostPort(hostport); err == nil {
		host = parsedHost
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func localBrowserMutationAllowed(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site") {
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		// Native clients and wings do not send browser Origin metadata. Their
		// local API compatibility is retained; browser requests do send Origin
		// for unsafe methods and are checked below.
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" {
		return false
	}
	wantScheme := "http"
	if r.TLS != nil {
		wantScheme = "https"
	}
	if !strings.EqualFold(parsed.Scheme, wantScheme) {
		return false
	}
	return canonicalOriginHost(parsed.Host, parsed.Scheme) == canonicalOriginHost(r.Host, wantScheme)
}

func (s *Server) hostedBrowserMutationAllowed(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site") {
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		// CLI, wing, OAuth, and MCP clients do not send browser Origin metadata.
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	for _, allowed := range s.configuredBrowserOrigins() {
		allowedURL, err := url.Parse(allowed)
		if err == nil && strings.EqualFold(parsed.Scheme, allowedURL.Scheme) &&
			canonicalOriginHost(parsed.Host, parsed.Scheme) == canonicalOriginHost(allowedURL.Host, allowedURL.Scheme) {
			return true
		}
	}
	return false
}

func canonicalOriginHost(hostport, scheme string) string {
	parsed, err := url.Parse("//" + hostport)
	if err != nil || parsed.Hostname() == "" {
		return ""
	}
	port := parsed.Port()
	if port == "" {
		if strings.EqualFold(scheme, "https") {
			port = "443"
		} else {
			port = "80"
		}
	}
	return net.JoinHostPort(strings.ToLower(parsed.Hostname()), port)
}

// shouldRateLimit returns true for endpoints that should be rate limited.
// Auth endpoints, mutating API calls, and WebSocket upgrades.
func (s *Server) shouldRateLimit(method, path string) bool {
	// All auth endpoints (login, token exchange, magic link, device auth)
	if strings.HasPrefix(path, "/auth/") {
		return true
	}
	// OAuth registration/token endpoints and MCP calls are externally reachable and can
	// otherwise be used to exhaust in-memory grants or privileged-tool concurrency.
	if strings.HasPrefix(path, "/oauth/") || (method == http.MethodPost && path == "/mcp") {
		return true
	}
	// Mutating API endpoints. Keep all current and future REST mutation verbs on
	// the same abuse boundary; labels and org membership already use PUT/DELETE.
	if strings.HasPrefix(path, "/api/") &&
		(method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete) {
		return true
	}
	// WebSocket upgrades
	if strings.HasPrefix(path, "/ws/") {
		return true
	}
	return false
}

func (s *Server) serveAppIndex(w http.ResponseWriter, r *http.Request) {
	f, err := web.FS.Open("dist/index.html")
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer func() { _ = f.Close() }()
	stat, _ := f.Stat()
	http.ServeContent(w, r, "index.html", stat.ModTime(), f.(io.ReadSeeker))
}

// broadcastToEdges POSTs a JSON payload to all known edge nodes.
// Fire-and-forget goroutines, 3s timeout per edge.
func (s *Server) broadcastToEdges(payload []byte) {
	if s.WingMap == nil || s.Config.FlyAppName == "" {
		return
	}
	for _, mid := range s.WingMap.EdgeIDs() {
		if mid == s.Config.FlyMachineID {
			continue // never broadcast to self — causes infinite loop
		}
		go func(machineID string) {
			url := fmt.Sprintf("http://%s.vm.%s.internal:8080/internal/wing-event", machineID, s.Config.FlyAppName)
			client := &http.Client{Timeout: 3 * time.Second}
			req, _ := http.NewRequest("POST", url, bytes.NewReader(payload))
			if req == nil {
				return
			}
			req.Header.Set("Content-Type", "application/json")
			s.authorizeInternalRequest(req)
			resp, err := client.Do(req)
			if err != nil {
				return
			}
			_ = resp.Body.Close()
		}(mid)
	}
}
