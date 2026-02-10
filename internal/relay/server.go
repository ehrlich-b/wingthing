package relay

import (
	"io/fs"
	"net/http"
	"strings"
	"sync"

	"github.com/ehrlich-b/wingthing/internal/embedding"
	"github.com/ehrlich-b/wingthing/web"
)

type ServerConfig struct {
	BaseURL            string
	GitHubClientID     string
	GitHubClientSecret string
	GoogleClientID     string
	GoogleClientSecret string
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
	Wings          *WingRegistry
	PTY            *PTYRegistry
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

	// Claim page
	s.mux.HandleFunc("GET /auth/claim", s.handleClaimPage)

	// Web pages
	s.mux.HandleFunc("GET /{$}", s.handleHome)
	s.mux.HandleFunc("GET /login", s.handleLogin)
	s.mux.HandleFunc("GET /skills", s.handleSkillsPage)
	s.mux.HandleFunc("GET /skills/{name}", s.handleSkillDetailPage)
	s.mux.HandleFunc("GET /social", s.handleSocial)
	s.mux.HandleFunc("GET /w/{slug}", s.handleAnchor)
	s.mux.HandleFunc("GET /p/{postID}", s.handlePostPage)

	// Web auth
	s.mux.HandleFunc("GET /auth/github", s.handleGitHubAuth)
	s.mux.HandleFunc("GET /auth/github/callback", s.handleGitHubCallback)
	s.mux.HandleFunc("GET /auth/google", s.handleGoogleAuth)
	s.mux.HandleFunc("GET /auth/google/callback", s.handleGoogleCallback)
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

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if r.Method == "GET" && (path == "/" || path == "/social" || path == "/skills" || path == "/login" || strings.HasPrefix(path, "/w/") || strings.HasPrefix(path, "/p/")) {
		if r.URL.RawQuery != "" {
			w.Header().Set("Cache-Control", "public, max-age=60, s-maxage=60")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=900, s-maxage=900")
		}
	}
	s.mux.ServeHTTP(w, r)
}
