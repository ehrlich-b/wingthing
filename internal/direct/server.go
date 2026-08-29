package direct

import (
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/golang-jwt/jwt/v5"

	"github.com/ehrlich-b/wingthing/internal/ws"
)

// HandoffClaims are the JWT claims for browser direct-mode connections.
type HandoffClaims struct {
	jwt.RegisteredClaims
	Email    string `json:"email,omitempty"`
	OrgRole  string `json:"org_role,omitempty"`
	TokenUse string `json:"token_use"`
}

// Server is a lightweight HTTP server for direct-mode wing connections.
// Browsers connect directly to the wing via WebSocket, bypassing the relay for PTY I/O.
type Server struct {
	OnPTY ws.PTYHandler

	mu          sync.Mutex
	listener    net.Listener
	httpServer  *http.Server
	connections map[*websocket.Conn]struct{}
	relayPubKey *ecdsa.PublicKey
	closed      bool
}

// SetRelayPublicKey atomically publishes the coordinator key used to verify
// short-lived browser handoff tokens.
func (s *Server) SetRelayPublicKey(key *ecdsa.PublicKey) {
	s.mu.Lock()
	s.relayPubKey = key
	s.mu.Unlock()
}

// Start begins listening on the given address.
func (s *Server) Start(addr string) error {
	server, listener, err := s.prepare(addr)
	if err != nil {
		return err
	}
	log.Printf("[direct] listening on %s", listener.Addr())
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
		return err
	}
	return nil
}

// StartAsync binds synchronously, then serves in the background. Callers can
// safely defer Close after this returns without racing a not-yet-created
// listener.
func (s *Server) StartAsync(addr string) error {
	server, listener, err := s.prepare(addr)
	if err != nil {
		return err
	}
	log.Printf("[direct] listening on %s", listener.Addr())
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			log.Printf("[direct] server error: %v", err)
		}
	}()
	return nil
}

func (s *Server) prepare(addr string) (*http.Server, net.Listener, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws/pty", s.handleDirectPTY)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"mode":"direct"}`))
	})

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("direct listen: %w", err)
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	s.mu.Lock()
	if s.closed || s.listener != nil {
		s.mu.Unlock()
		_ = ln.Close()
		return nil, nil, fmt.Errorf("direct server already started or closed")
	}
	s.listener = ln
	s.httpServer = server
	s.connections = make(map[*websocket.Conn]struct{})
	s.mu.Unlock()
	return server, ln, nil
}

// Addr returns the bound address after Start or StartAsync succeeds.
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return nil
	}
	return s.listener.Addr()
}

// Close stops the listener and every upgraded direct WebSocket.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	server := s.httpServer
	connections := make([]*websocket.Conn, 0, len(s.connections))
	for conn := range s.connections {
		connections = append(connections, conn)
	}
	s.mu.Unlock()
	for _, conn := range connections {
		_ = conn.Close(websocket.StatusGoingAway, "direct server shutting down")
	}
	if server != nil {
		return server.Close()
	}
	return nil
}

func (s *Server) handleDirectPTY(w http.ResponseWriter, r *http.Request) {
	// Authenticate via handoff JWT in Authorization header
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	tokenStr := strings.TrimPrefix(auth, "Bearer ")

	s.mu.Lock()
	relayPubKey := s.relayPubKey
	s.mu.Unlock()
	if relayPubKey == nil {
		http.Error(w, "direct mode not configured (no relay public key)", http.StatusServiceUnavailable)
		return
	}

	claims, err := validateHandoffJWT(relayPubKey, tokenStr)
	if err != nil {
		log.Printf("[direct] JWT validation failed: %v", err)
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	// Native direct clients send no Origin. Retain default Origin enforcement
	// so a credential exposed to a browser cannot be replayed cross-site.
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		log.Printf("[direct] websocket accept: %v", err)
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = conn.CloseNow()
		return
	}
	s.connections[conn] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.connections, conn)
		s.mu.Unlock()
	}()
	conn.SetReadLimit(512 * 1024)
	defer func() { _ = conn.CloseNow() }()

	ctx := r.Context()

	// A direct connection owns one newly-started session. Reattachment requires
	// transferring the existing session's output writer to the new connection,
	// which this legacy transport has never implemented. Reject attach instead
	// of mis-decoding it as PTYStart and accidentally launching another agent.
	_, data, err := conn.Read(ctx)
	if err != nil {
		return
	}

	var env ws.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return
	}

	if env.Type != ws.TypePTYStart {
		log.Printf("[direct] expected pty.start, got %s", env.Type)
		_ = conn.Close(websocket.StatusUnsupportedData, "expected pty.start")
		return
	}

	var start ws.PTYStart
	if err := json.Unmarshal(data, &start); err != nil {
		return
	}
	if !ws.ValidSessionID(start.SessionID) {
		log.Printf("[direct] rejected invalid session ID %q", start.SessionID)
		return
	}

	// Inject identity from JWT claims
	start.UserID = claims.Subject
	start.Email = claims.Email
	start.OrgRole = claims.OrgRole

	inputCh := make(chan []byte, 64)
	var writeMu sync.Mutex
	writeFn := func(v any) error {
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		// coder/websocket permits one concurrent writer. PTY handlers can emit
		// output and lifecycle messages from different goroutines.
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.Write(ctx, websocket.MessageText, data)
	}

	// Start input reader
	go func() {
		for {
			_, msg, err := conn.Read(ctx)
			if err != nil {
				close(inputCh)
				return
			}
			select {
			case inputCh <- msg:
			default:
			}
		}
	}()

	if s.OnPTY != nil {
		s.OnPTY(ctx, start, writeFn, inputCh)
	}
}

func validateHandoffJWT(pubKey *ecdsa.PublicKey, tokenStr string) (*HandoffClaims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &HandoffClaims{}, func(t *jwt.Token) (any, error) {
		return pubKey, nil
	}, jwt.WithValidMethods([]string{"ES256"}), jwt.WithExpirationRequired(), jwt.WithIssuedAt())
	if err != nil {
		return nil, fmt.Errorf("parse handoff jwt: %w", err)
	}
	claims, ok := token.Claims.(*HandoffClaims)
	if !ok || !token.Valid || claims.TokenUse != "handoff" || claims.Subject == "" {
		return nil, fmt.Errorf("invalid handoff jwt claims")
	}
	return claims, nil
}
