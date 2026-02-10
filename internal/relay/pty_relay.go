package relay

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/ehrlich-b/wingthing/internal/ws"
)

// PTYSession tracks a browser <-> wing PTY connection through the relay.
type PTYSession struct {
	ID          string
	WingID      string
	UserID      string
	Agent       string
	CWD         string // working directory for this session
	Status      string // "active", "detached", "exited"
	BrowserConn *websocket.Conn
	mu          sync.Mutex
}

// PTYRegistry tracks active PTY sessions.
type PTYRegistry struct {
	mu       sync.RWMutex
	sessions map[string]*PTYSession // session_id -> session
}

func NewPTYRegistry() *PTYRegistry {
	return &PTYRegistry{
		sessions: make(map[string]*PTYSession),
	}
}

func (r *PTYRegistry) Add(s *PTYSession) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions[s.ID] = s
}

func (r *PTYRegistry) Remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, id)
}

func (r *PTYRegistry) Get(id string) *PTYSession {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sessions[id]
}

// CountForUser returns the number of PTY sessions for a given user.
func (r *PTYRegistry) CountForUser(userID string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, s := range r.sessions {
		if s.UserID == userID {
			n++
		}
	}
	return n
}

// ListForUser returns all PTY sessions for a given user.
func (r *PTYRegistry) ListForUser(userID string) []*PTYSession {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*PTYSession
	for _, s := range r.sessions {
		if s.UserID == userID {
			result = append(result, s)
		}
	}
	return result
}

// handlePTYWS handles the browser WebSocket for a PTY session.
func (s *Server) handlePTYWS(w http.ResponseWriter, r *http.Request) {
	// Auth
	var userID string
	if u := s.sessionUser(r); u != nil {
		userID = u.ID
	}
	if userID == "" {
		token := r.URL.Query().Get("token")
		if token == "" {
			auth := r.Header.Get("Authorization")
			if strings.HasPrefix(auth, "Bearer ") {
				token = strings.TrimPrefix(auth, "Bearer ")
			}
		}
		if token == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		secret, secretErr := GenerateOrLoadSecret(s.Store, s.Config.JWTSecret)
		if secretErr == nil {
			if claims, err := ValidateWingJWT(secret, token); err == nil {
				userID = claims.Subject
			}
		}
		if userID == "" {
			var err error
			userID, _, err = s.Store.ValidateToken(token)
			if err != nil {
				http.Error(w, "invalid token", http.StatusUnauthorized)
				return
			}
		}
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		log.Printf("pty websocket accept: %v", err)
		return
	}
	defer conn.CloseNow()

	ctx := r.Context()

	// Track which sessions this browser connection owns for detach on disconnect
	var ownedSessions []string

	defer func() {
		// On browser disconnect: mark owned sessions as detached, don't remove
		for _, sid := range ownedSessions {
			session := s.PTY.Get(sid)
			if session == nil {
				continue
			}
			session.mu.Lock()
			if session.Status == "active" {
				session.Status = "detached"
				session.BrowserConn = nil
				log.Printf("pty session %s: browser detached", sid)
	
			}
			session.mu.Unlock()
		}
	}()

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			log.Printf("pty browser disconnected: %v", err)
			return
		}

		// Meter bandwidth (applies backpressure via rate limiter)
		if s.Bandwidth != nil {
			if err := s.Bandwidth.Wait(ctx, userID, len(data)); err != nil {
				return
			}
		}

		var env ws.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			continue
		}

		switch env.Type {
		case ws.TypePTYStart:
			var start ws.PTYStart
			if err := json.Unmarshal(data, &start); err != nil {
				continue
			}

			var wing *ConnectedWing
			if start.WingID != "" {
				wing = s.Wings.FindByID(start.WingID)
			} else {
				wing = s.Wings.FindForUser(userID)
			}
			if wing == nil {
				errMsg, _ := json.Marshal(ws.ErrorMsg{Type: ws.TypeError, Message: "no wing connected"})
				conn.Write(ctx, websocket.MessageText, errMsg)
				continue
			}

			sessionID := uuid.New().String()[:8]
			start.SessionID = sessionID
			session := &PTYSession{
				ID:          sessionID,
				WingID:      wing.ID,
				UserID:      userID,
				Agent:       start.Agent,
				CWD:         start.CWD,
				Status:      "active",
				BrowserConn: conn,
			}
			s.PTY.Add(session)
			ownedSessions = append(ownedSessions, sessionID)

			fwd, _ := json.Marshal(start)
			wing.Conn.Write(ctx, websocket.MessageText, fwd)

			log.Printf("pty session %s started (user=%s wing=%s agent=%s)", sessionID, userID, wing.ID, start.Agent)

		case ws.TypePTYAttach:
			var attach ws.PTYAttach
			if err := json.Unmarshal(data, &attach); err != nil {
				continue
			}

			session := s.PTY.Get(attach.SessionID)
			if session == nil || session.UserID != userID {
				errMsg, _ := json.Marshal(ws.ErrorMsg{Type: ws.TypeError, Message: "session not found"})
				conn.Write(ctx, websocket.MessageText, errMsg)
				continue
			}

			session.mu.Lock()
			if session.BrowserConn != nil && session.Status == "active" {
				session.mu.Unlock()
				errMsg, _ := json.Marshal(ws.ErrorMsg{Type: ws.TypeError, Message: "session already attached"})
				conn.Write(ctx, websocket.MessageText, errMsg)
				continue
			}
			session.BrowserConn = conn
			session.Status = "active"
			session.mu.Unlock()


			ownedSessions = append(ownedSessions, attach.SessionID)

			// Forward pty.attach to wing so it can replay buffer and re-key
			wing := s.Wings.FindByID(session.WingID)
			if wing != nil {
				fwd, _ := json.Marshal(attach)
				wing.Conn.Write(ctx, websocket.MessageText, fwd)
			}

			log.Printf("pty session %s reattached (user=%s)", attach.SessionID, userID)

		case ws.TypePTYInput:
			var input ws.PTYInput
			if err := json.Unmarshal(data, &input); err != nil {
				continue
			}
			session := s.PTY.Get(input.SessionID)
			if session == nil {
				continue
			}
			wing := s.Wings.FindByID(session.WingID)
			if wing == nil {
				continue
			}
			wing.Conn.Write(ctx, websocket.MessageText, data)

		case ws.TypePTYResize:
			var resize ws.PTYResize
			if err := json.Unmarshal(data, &resize); err != nil {
				continue
			}
			session := s.PTY.Get(resize.SessionID)
			if session == nil {
				continue
			}
			wing := s.Wings.FindByID(session.WingID)
			if wing == nil {
				continue
			}
			wing.Conn.Write(ctx, websocket.MessageText, data)

		case ws.TypePTYDetach:
			var det ws.PTYDetach
			if err := json.Unmarshal(data, &det); err != nil {
				continue
			}
			session := s.PTY.Get(det.SessionID)
			if session == nil || session.UserID != userID {
				continue
			}
			session.mu.Lock()
			if session.BrowserConn == conn {
				session.Status = "detached"
				session.BrowserConn = nil
				log.Printf("pty session %s: explicit detach (user=%s)", det.SessionID, userID)

			}
			session.mu.Unlock()

		case ws.TypePTYKill:
			var kill ws.PTYKill
			if err := json.Unmarshal(data, &kill); err != nil {
				continue
			}
			session := s.PTY.Get(kill.SessionID)
			if session == nil || session.UserID != userID {
				continue
			}
			// Forward kill to wing so it terminates the PTY process
			wing := s.Wings.FindByID(session.WingID)
			if wing != nil {
				fwd, _ := json.Marshal(kill)
				wing.Conn.Write(ctx, websocket.MessageText, fwd)
			} else {
				// Wing gone — just clean up the session
				s.PTY.Remove(kill.SessionID)
			}
			log.Printf("pty session %s: kill requested (user=%s)", kill.SessionID, userID)

		case ws.TypeChatStart:
			var start ws.ChatStart
			if err := json.Unmarshal(data, &start); err != nil {
				continue
			}

			wing := s.Wings.FindForUser(userID)
			if wing == nil {
				errMsg, _ := json.Marshal(ws.ErrorMsg{Type: ws.TypeError, Message: "no wing connected"})
				conn.Write(ctx, websocket.MessageText, errMsg)
				continue
			}

			sessionID := start.SessionID
			if sessionID == "" {
				sessionID = uuid.New().String()[:8]
				start.SessionID = sessionID
			}
			cs := &ChatSession{
				ID:          sessionID,
				WingID:      wing.ID,
				UserID:      userID,
				Agent:       start.Agent,
				Status:      "active",
				BrowserConn: conn,
			}
			s.Chat.Add(cs)

			fwd, _ := json.Marshal(start)
			wing.Conn.Write(ctx, websocket.MessageText, fwd)

			log.Printf("chat session %s started (user=%s wing=%s agent=%s)", sessionID, userID, wing.ID, start.Agent)

		case ws.TypeChatMessage:
			var msg ws.ChatMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				continue
			}
			cs := s.Chat.Get(msg.SessionID)
			if cs == nil {
				continue
			}
			wing := s.Wings.FindByID(cs.WingID)
			if wing == nil {
				continue
			}
			wing.Conn.Write(ctx, websocket.MessageText, data)

		case ws.TypeChatDelete:
			var del ws.ChatDelete
			if err := json.Unmarshal(data, &del); err != nil {
				continue
			}
			cs := s.Chat.Get(del.SessionID)
			if cs == nil || cs.UserID != userID {
				continue
			}
			wing := s.Wings.FindByID(cs.WingID)
			if wing != nil {
				fwd, _ := json.Marshal(del)
				wing.Conn.Write(ctx, websocket.MessageText, fwd)
			} else {
				s.Chat.Remove(del.SessionID)
			}
			log.Printf("chat session %s: delete requested (user=%s)", del.SessionID, userID)
		}
	}
}

// forwardPTYToBrowser routes a PTY message from the wing to the browser.
func (s *Server) forwardPTYToBrowser(sessionID string, data []byte) {
	session := s.PTY.Get(sessionID)
	if session == nil {
		return
	}

	// Handle pty.exited: mark session as exited and clean up
	var env ws.Envelope
	if err := json.Unmarshal(data, &env); err == nil && env.Type == ws.TypePTYExited {
		session.mu.Lock()
		session.Status = "exited"
		bc := session.BrowserConn
		session.mu.Unlock()

		if bc != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			bc.Write(ctx, websocket.MessageText, data)
		}
		s.PTY.Remove(sessionID)
		return
	}

	session.mu.Lock()
	bc := session.BrowserConn
	session.mu.Unlock()

	if bc == nil {
		// Session detached — drop the data (wing has ring buffer for replay)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bc.Write(ctx, websocket.MessageText, data)
}
