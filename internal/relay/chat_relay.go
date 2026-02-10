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
	session.mu.Unlock()

	if bc == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bc.Write(ctx, websocket.MessageText, data)
}
