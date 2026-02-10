package relay

import (
	"io"
	"io/fs"
	"net/http"
	"strings"
	"sync"

	"github.com/ehrlich-b/wingthing/internal/embedding"
	"github.com/ehrlich-b/wingthing/web"
)

type ServerConfig struct {
	BaseURL            string
	AppHost            string // e.g. "app.wingthing.ai" — serve SPA at root
	WSHost             string // e.g. "ws.wingthing.ai" — WebSocket only
	JWTSecret          string // base64-encoded; overrides DB-stored secret
	GitHubClientID     string
	GitHubClientSecret string
	SMTPHost           string
	SMTPPort           string
	SMTPUser           string
	SMTPPass           string
	SMTPFrom           string
}

type Server struct {
	Store          *RelayStore
	Embedder       embedding.Embedder
	Config         ServerConfig
	DevTemplateDir string // if set, re-read templates from disk on each request
	DevMode        bool   // if set, auto-claim device codes with test-user
	LocalMode      bool   // if set, bypass auth — single-user, zero-config
	localUser      *SocialUser
	Wings          *WingRegistry
	PTY            *PTYRegistry
	Chat           *ChatRegistry
	Bandwidth      *BandwidthMeter
	mux            *http.ServeMux

	// Stream subscribers: taskID → list of channels receiving output chunks
	streamMu   sync.RWMutex
	streamSubs map[string][]chan string
}

func NewServer(store *RelayStore, cfg ServerConfig) *Server {
	s := &Server{
		Store:      store,
		Config:     cfg,
		Wings:      NewWingRegistry(),
		PTY:        NewPTYRegistry(),
		Chat:       NewChatRegistry(),
		mux:        http.NewServeMux(),
		streamSubs: make(map[string][]chan string),
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
	s.mux.HandleFunc("POST /api/post", s.handlePost)
	s.mux.HandleFunc("POST /api/vote", s.handleVote)
	s.mux.HandleFunc("POST /api/comment", s.handleComment)
	s.mux.HandleFunc("GET /api/comments", s.handleListComments)
	s.mux.HandleFunc("POST /api/sync/push", s.handleSyncPush)
	s.mux.HandleFunc("GET /api/sync/pull", s.handleSyncPull)

	// Relay: worker WebSocket + task API
	s.mux.HandleFunc("GET /ws/wing", s.handleWingWS)
	s.mux.HandleFunc("POST /api/tasks", s.handleSubmitTask)
	s.mux.HandleFunc("GET /api/tasks/{id}/stream", s.handleTaskStream)
	s.mux.HandleFunc("GET /ws/pty", s.handlePTYWS)

	// App dashboard API (cookie auth)
	s.mux.HandleFunc("GET /api/app/me", s.handleAppMe)
	s.mux.HandleFunc("GET /api/app/wings", s.handleAppWings)
	s.mux.HandleFunc("GET /api/app/sessions", s.handleAppSessions)
	s.mux.HandleFunc("DELETE /api/app/sessions/{id}", s.handleDeleteSession)
	s.mux.HandleFunc("GET /api/app/wings/{wingID}/ls", s.handleWingLS)
	s.mux.HandleFunc("POST /api/app/wings/{wingID}/update", s.handleWingUpdate)

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
	s.mux.HandleFunc("GET /self-host", s.handleSelfHost)
	s.mux.HandleFunc("GET /social", s.handleSocial)
	s.mux.HandleFunc("GET /w/{slug}", s.handleAnchor)
	s.mux.HandleFunc("GET /p/{postID}", s.handlePostPage)

	// Web auth
	s.mux.HandleFunc("GET /auth/github", s.handleGitHubAuth)
	s.mux.HandleFunc("GET /auth/github/callback", s.handleGitHubCallback)
	s.mux.HandleFunc("POST /auth/magic", s.handleMagicLink)
	s.mux.HandleFunc("GET /auth/magic/verify", s.handleMagicVerify)
	s.mux.HandleFunc("POST /auth/logout", s.handleLogout)
	s.mux.HandleFunc("GET /auth/dev", s.handleDevLogin)

	s.registerStaticRoutes()
	return s
}

func (s *Server) registerStaticRoutes() {
	sub, _ := fs.Sub(web.FS, "dist")
	fileServer := http.FileServer(http.FS(sub))
	s.mux.Handle("GET /app/", http.StripPrefix("/app/", fileServer))
	s.mux.HandleFunc("GET /app", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/app/", http.StatusMovedPermanently)
	})
}

func stripPort(host string) string {
	if i := strings.LastIndex(host, ":"); i != -1 {
		return host[:i]
	}
	return host
}

func (s *Server) SetLocalUser(u *SocialUser) {
	s.localUser = u
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := stripPort(r.Host)
	path := r.URL.Path

	// app.wingthing.ai: SPA at root, plus API/auth/ws/assets
	if s.Config.AppHost != "" && host == s.Config.AppHost {
		if strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/auth/") ||
			strings.HasPrefix(path, "/ws/") || strings.HasPrefix(path, "/app/") {
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
	if r.Method == "GET" && (path == "/" || path == "/social" || path == "/skills" || path == "/login" || path == "/self-host" || strings.HasPrefix(path, "/w/") || strings.HasPrefix(path, "/p/")) {
		if r.URL.RawQuery != "" {
			w.Header().Set("Cache-Control", "public, max-age=60, s-maxage=60")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=900, s-maxage=900")
		}
	}
	s.mux.ServeHTTP(w, r)
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
