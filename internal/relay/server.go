package relay

import (
	"net/http"
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
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) Sessions() *SessionManager {
	return s.sessions
}
