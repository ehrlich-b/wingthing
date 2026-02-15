package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/ehrlich-b/wingthing/internal/ws"
	"github.com/ehrlich-b/wingthing/web"
)

type ServerConfig struct {
	BaseURL            string
	AppHost            string // e.g. "app.wingthing.ai" — serve SPA at root
	WSHost             string // e.g. "ws.wingthing.ai" — WebSocket only
	JWTSecret          string // base64-encoded; overrides DB-stored secret
	GitHubClientID     string
	GitHubClientSecret string
	GoogleClientID     string
	GoogleClientSecret string
	SMTPHost           string
	SMTPPort           string
	SMTPUser           string
	SMTPPass           string
	SMTPFrom           string
	NodeRole           string // "login", "edge", or "" (single node)
	LoginNodeAddr      string // internal address of login node (for edge nodes)
	FlyMachineID       string // from FLY_MACHINE_ID env var
	FlyRegion          string // from FLY_REGION env var
	FlyAppName         string // from FLY_APP_NAME env var
}

type Server struct {
	Store          *RelayStore
	Config         ServerConfig
	DevTemplateDir string // if set, re-read templates from disk on each request
	DevMode        bool   // if set, auto-claim device codes with test-user
	LocalMode      bool   // if set, bypass auth — single-user, zero-config
	localUser      *User
	Wings          *WingRegistry
	PTY            *PTYRoutes
	Bandwidth      *BandwidthMeter
	RateLimit      *RateLimiter
	mux            *http.ServeMux

	// Latest release version cache (fetched from GitHub)
	latestVersion   string
	latestVersionAt time.Time
	latestVersionMu sync.RWMutex

	// All browser WebSocket connections (for shutdown broadcast)
	browserMu    sync.Mutex
	browserConns map[*websocket.Conn]struct{}

	// Tunnel request tracking (requestID → browser WebSocket)
	tunnelMu       sync.Mutex
	tunnelRequests map[string]*websocket.Conn

	// Cluster routing (multi-node)
	WingMap *WingMap

	// Edge node: reverse proxy to login node + session/entitlement caches
	loginProxy       http.Handler
	sessionCache     *SessionCache
	EntitlementCache *EntitlementCache
}

func NewServer(store *RelayStore, cfg ServerConfig) *Server {
	s := &Server{
		Store:        store,
		Config:       cfg,
		Wings:          NewWingRegistry(),
		PTY:            NewPTYRoutes(),
		mux:            http.NewServeMux(),
		browserConns:   make(map[*websocket.Conn]struct{}),
		tunnelRequests: make(map[string]*websocket.Conn),
	}

	// API routes
	s.mux.HandleFunc("POST /auth/device", s.handleAuthDevice)
	s.mux.HandleFunc("POST /auth/token", s.handleAuthToken)
	s.mux.HandleFunc("POST /auth/claim", s.handleAuthClaim)
	s.mux.HandleFunc("POST /auth/refresh", s.handleAuthRefresh)
	s.mux.HandleFunc("GET /health", s.handleHealth)
	s.mux.HandleFunc("GET /api/skills", s.handleListSkills)
	s.mux.HandleFunc("GET /api/skills/{name}", s.handleGetSkill)
	s.mux.HandleFunc("GET /api/skills/{name}/raw", s.handleGetSkillRaw)
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

	// Passkey management (cookie auth)
	s.mux.HandleFunc("POST /api/app/passkey/register/begin", s.handlePasskeyRegisterBegin)
	s.mux.HandleFunc("POST /api/app/passkey/register/finish", s.handlePasskeyRegisterFinish)
	s.mux.HandleFunc("GET /api/app/passkey", s.handlePasskeyList)
	s.mux.HandleFunc("DELETE /api/app/passkey/{id}", s.handlePasskeyDelete)

	// Claim page
	s.mux.HandleFunc("GET /auth/claim", s.handleClaimPage)

	// Static files
	s.mux.HandleFunc("GET /install.sh", s.handleInstallScript)

	// Web pages
	s.mux.HandleFunc("GET /{$}", s.handleHome)
	s.mux.HandleFunc("GET /login", s.handleLogin)
	s.mux.HandleFunc("GET /skills", s.handleSkillsPage)
	s.mux.HandleFunc("GET /skills/{name}", s.handleSkillDetailPage)
	s.mux.HandleFunc("GET /install", s.handleInstallPage)
	s.mux.HandleFunc("GET /docs", s.handleDocs)
	s.mux.HandleFunc("GET /terms", s.handleTerms)
	s.mux.HandleFunc("GET /privacy", s.handlePrivacy)
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
	if i := strings.LastIndex(host, ":"); i != -1 {
		return host[:i]
	}
	return host
}

func (s *Server) SetLocalUser(u *User) { s.localUser = u }

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

func (s *Server) trackBrowser(conn *websocket.Conn) {
	s.browserMu.Lock()
	s.browserConns[conn] = struct{}{}
	s.browserMu.Unlock()
}

func (s *Server) untrackBrowser(conn *websocket.Conn) {
	s.browserMu.Lock()
	delete(s.browserConns, conn)
	s.browserMu.Unlock()
}

// GracefulShutdown sends relay.restart to all connected WebSockets, then shuts down the HTTP server.
func (s *Server) GracefulShutdown(httpSrv *http.Server, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	msg := ws.RelayRestart{Type: ws.TypeRelayRestart}
	data, _ := json.Marshal(msg)

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
		conn.Write(writeCtx, websocket.MessageText, data)
		wcancel()
	}

	log.Printf("sent relay.restart to %d wings, %d browsers", len(s.Wings.All()), len(browsers))

	// Close all wing connections
	s.Wings.CloseAll()

	// Graceful HTTP shutdown
	return httpSrv.Shutdown(ctx)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := stripPort(r.Host)
	path := r.URL.Path

	// Rate limit auth and mutating API endpoints
	if s.RateLimit != nil && s.shouldRateLimit(r.Method, path) {
		ip := clientIP(r)
		if !s.RateLimit.Allow(ip) {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
	}

	// Edge node proxying: serve WS/static/internal locally, proxy everything else to login
	if s.IsEdge() && s.loginProxy != nil {
		if strings.HasPrefix(path, "/ws/") || strings.HasPrefix(path, "/app/") ||
			strings.HasPrefix(path, "/assets/") || strings.HasPrefix(path, "/internal/") ||
			path == "/health" {
			s.mux.ServeHTTP(w, r)
			return
		}
		s.loginProxy.ServeHTTP(w, r)
		return
	}

	// app.wingthing.ai: SPA at root, plus API/auth/ws/assets
	if s.Config.AppHost != "" && host == s.Config.AppHost {
		if strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/auth/") ||
			strings.HasPrefix(path, "/ws/") || strings.HasPrefix(path, "/app/") ||
			strings.HasPrefix(path, "/assets/") {
			s.mux.ServeHTTP(w, r)
			return
		}
		s.serveAppIndex(w, r)
		return
	}

	// ws.wingthing.ai: WebSocket + health only
	if s.Config.WSHost != "" && host == s.Config.WSHost {
		if strings.HasPrefix(path, "/ws/") || path == "/health" {
			s.mux.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
		return
	}

	// Default host: full site with caching
	if r.Method == "GET" && (path == "/" || path == "/skills" || path == "/login" || path == "/docs" || path == "/terms" || path == "/privacy") {
		if r.URL.RawQuery != "" {
			w.Header().Set("Cache-Control", "public, max-age=60, s-maxage=60")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=900, s-maxage=900")
		}
	}
	s.mux.ServeHTTP(w, r)
}

// shouldRateLimit returns true for endpoints that should be rate limited.
// Auth endpoints, mutating API calls, and WebSocket upgrades.
func (s *Server) shouldRateLimit(method, path string) bool {
	// All auth endpoints (login, token exchange, magic link, device auth)
	if strings.HasPrefix(path, "/auth/") {
		return true
	}
	// Mutating API endpoints
	if method == "POST" && strings.HasPrefix(path, "/api/") {
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
	defer f.Close()
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
		go func(machineID string) {
			url := fmt.Sprintf("http://%s.vm.%s.internal:8080/internal/wing-event", machineID, s.Config.FlyAppName)
			client := &http.Client{Timeout: 3 * time.Second}
			req, _ := http.NewRequest("POST", url, bytes.NewReader(payload))
			if req == nil {
				return
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err != nil {
				return
			}
			resp.Body.Close()
		}(mid)
	}
}
