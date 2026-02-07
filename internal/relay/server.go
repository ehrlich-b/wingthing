package relay

import (
	"io/fs"
	"net/http"

	"github.com/ehrlich-b/wingthing/web"
)

type Server struct {
	Store    *RelayStore
	sessions *SessionManager
	mux      *http.ServeMux
}

func NewServer(store *RelayStore) *Server {
	s := &Server{
		Store:    store,
		sessions: NewSessionManager(),
		mux:      http.NewServeMux(),
	}
	s.mux.HandleFunc("GET /ws/daemon", s.handleDaemonWS)
	s.mux.HandleFunc("GET /ws/client", s.handleClientWS)
	s.mux.HandleFunc("POST /auth/device", s.handleAuthDevice)
	s.mux.HandleFunc("POST /auth/token", s.handleAuthToken)
	s.mux.HandleFunc("POST /auth/claim", s.handleAuthClaim)
	s.mux.HandleFunc("POST /auth/refresh", s.handleAuthRefresh)
	s.mux.HandleFunc("GET /health", s.handleHealth)
	s.mux.HandleFunc("GET /skills", s.handleListSkills)
	s.mux.HandleFunc("GET /skills/{name}", s.handleGetSkill)
	s.mux.HandleFunc("GET /skills/{name}/raw", s.handleGetSkillRaw)
	s.registerStaticRoutes()
	return s
}

func (s *Server) registerStaticRoutes() {
	sub, _ := fs.Sub(web.FS, ".")
	fileServer := http.FileServer(http.FS(sub))
	s.mux.Handle("GET /app/", http.StripPrefix("/app/", fileServer))
	s.mux.HandleFunc("GET /app", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/app/", http.StatusMovedPermanently)
	})
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) Sessions() *SessionManager {
	return s.sessions
}
