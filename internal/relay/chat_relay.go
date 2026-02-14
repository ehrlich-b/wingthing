package relay

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/ehrlich-b/wingthing/internal/ws"
)

// ChatSession tracks a browser <-> wing chat connection through the relay.
type ChatSession struct {
	ID          string
	WingID      string
	UserID      string
	Agent       string
	Status      string // "active", "exited"
	BrowserConn *websocket.Conn
	mu          sync.Mutex
}

// ChatRegistry tracks active chat sessions.
type ChatRegistry struct {
	mu       sync.RWMutex
	sessions map[string]*ChatSession // session_id -> session
}

func NewChatRegistry() *ChatRegistry {
	return &ChatRegistry{
		sessions: make(map[string]*ChatSession),
	}
}

func (r *ChatRegistry) Add(s *ChatSession) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions[s.ID] = s
}

func (r *ChatRegistry) Remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, id)
}

func (r *ChatRegistry) Get(id string) *ChatSession {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sessions[id]
}

// CountForUser returns the number of chat sessions for a given user.
func (r *ChatRegistry) CountForUser(userID string) int {
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

// All returns all chat sessions.
func (r *ChatRegistry) All() []*ChatSession {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*ChatSession, 0, len(r.sessions))
	for _, s := range r.sessions {
		result = append(result, s)
	}
	return result
}

// ListForUser returns all chat sessions for a given user.
func (r *ChatRegistry) ListForUser(userID string) []*ChatSession {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*ChatSession
	for _, s := range r.sessions {
		if s.UserID == userID {
			result = append(result, s)
		}
	}
	return result
}

// forwardChatToBrowser routes a chat message from the wing to the browser.
func (s *Server) forwardChatToBrowser(sessionID string, data []byte) {
	session := s.Chat.Get(sessionID)
	if session == nil {
		return
	}

	// Handle chat.deleted: clean up session
	var env ws.Envelope
	if err := json.Unmarshal(data, &env); err == nil && env.Type == ws.TypeChatDeleted {
		s.Chat.Remove(sessionID)
	}

	session.mu.Lock()
	bc := session.BrowserConn
	userID := session.UserID
	session.mu.Unlock()

	if bc == nil {
		return
	}

	// Meter outbound bandwidth (relay → browser is what costs on Fly)
	if s.Bandwidth != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.Bandwidth.Wait(ctx, userID, len(data)); err != nil {
			msg, _ := json.Marshal(ws.BandwidthExceeded{
				Type:    ws.TypeBandwidthExceeded,
				Message: "Monthly bandwidth limit exceeded. Upgrade to pro for higher limits.",
			})
			bc.Write(ctx, websocket.MessageText, msg)
			session.mu.Lock()
			session.BrowserConn = nil
			session.mu.Unlock()
			bc.Close(websocket.StatusNormalClosure, "bandwidth exceeded")
			return
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bc.Write(ctx, websocket.MessageText, data)
}
