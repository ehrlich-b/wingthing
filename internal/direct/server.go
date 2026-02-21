package direct

import (
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/coder/websocket"
	"github.com/golang-jwt/jwt/v5"

	"github.com/ehrlich-b/wingthing/internal/ws"
)

// HandoffClaims are the JWT claims for browser direct-mode connections.
type HandoffClaims struct {
	jwt.RegisteredClaims
	Email   string `json:"email,omitempty"`
	OrgRole string `json:"org_role,omitempty"`
}

// Server is a lightweight HTTP server for direct-mode wing connections.
// Browsers connect directly to the wing via WebSocket, bypassing the relay for PTY I/O.
type Server struct {
	RelayPubKey *ecdsa.PublicKey           // relay's ES256 public key for JWT verification
	OnPTY       ws.PTYHandler

	mu       sync.Mutex
	listener net.Listener
}

// Start begins listening on the given address.
func (s *Server) Start(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws/pty", s.handleDirectPTY)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"mode":"direct"}`))
	})

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("direct listen: %w", err)
	}
	s.mu.Lock()
	s.listener = ln
	s.mu.Unlock()

	log.Printf("[direct] listening on %s", addr)
	return http.Serve(ln, mux)
}

// Close stops the listener.
func (s *Server) Close() error {
	s.mu.Lock()
	ln := s.listener
	s.mu.Unlock()
	if ln != nil {
		return ln.Close()
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

	if s.RelayPubKey == nil {
		http.Error(w, "direct mode not configured (no relay public key)", http.StatusServiceUnavailable)
		return
	}

	claims, err := validateHandoffJWT(s.RelayPubKey, tokenStr)
	if err != nil {
		log.Printf("[direct] JWT validation failed: %v", err)
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		log.Printf("[direct] websocket accept: %v", err)
		return
	}
	conn.SetReadLimit(512 * 1024)
	defer conn.CloseNow()

	ctx := r.Context()

	// Read first message — expect pty.start or pty.attach
	_, data, err := conn.Read(ctx)
	if err != nil {
		return
	}

	var env ws.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return
	}

	if env.Type != ws.TypePTYStart && env.Type != ws.TypePTYAttach {
		log.Printf("[direct] expected pty.start or pty.attach, got %s", env.Type)
		return
	}

	var start ws.PTYStart
	if err := json.Unmarshal(data, &start); err != nil {
		return
	}

	// Inject identity from JWT claims
	start.UserID = claims.Subject
	start.Email = claims.Email
	start.OrgRole = claims.OrgRole

	inputCh := make(chan []byte, 64)
	writeFn := func(v any) error {
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
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
		if _, ok := t.Method.(*jwt.SigningMethodECDSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return pubKey, nil
	})
	if err != nil {
		return nil, fmt.Errorf("parse handoff jwt: %w", err)
	}
	claims, ok := token.Claims.(*HandoffClaims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid handoff jwt claims")
	}
	return claims, nil
}
