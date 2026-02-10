package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/ehrlich-b/wingthing/internal/ws"
)

// ConnectedWing represents a wing connected via WebSocket.
type ConnectedWing struct {
	ID         string
	UserID     string
	MachineID  string
	PublicKey  string
	Agents     []string
	Skills     []string
	Labels     []string
	Identities []string
	Conn       *websocket.Conn
	LastSeen   time.Time
}

// WingRegistry tracks all connected wings.
type WingRegistry struct {
	mu    sync.RWMutex
	wings map[string]*ConnectedWing // wingID → wing
}

func NewWingRegistry() *WingRegistry {
	return &WingRegistry{
		wings: make(map[string]*ConnectedWing),
	}
}

func (r *WingRegistry) Add(w *ConnectedWing) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.wings[w.ID] = w
}

func (r *WingRegistry) Remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.wings, id)
}

func (r *WingRegistry) FindForUser(userID string) *ConnectedWing {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, w := range r.wings {
		if w.UserID == userID {
			return w
		}
	}
	return nil
}

func (r *WingRegistry) FindByIdentity(identity string) *ConnectedWing {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, w := range r.wings {
		for _, id := range w.Identities {
			if id == identity {
				return w
			}
		}
		if w.UserID == identity {
			return w
		}
	}
	return nil
}

func (r *WingRegistry) FindByID(wingID string) *ConnectedWing {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.wings[wingID]
}

func (r *WingRegistry) Touch(wingID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if w, ok := r.wings[wingID]; ok {
		w.LastSeen = time.Now()
	}
}

// ListForUser returns all wings connected for a given user.
func (r *WingRegistry) ListForUser(userID string) []*ConnectedWing {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*ConnectedWing
	for _, w := range r.wings {
		if w.UserID == userID {
			result = append(result, w)
		}
	}
	return result
}

// CountForUser returns the number of wings connected for a given user.
func (r *WingRegistry) CountForUser(userID string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, w := range r.wings {
		if w.UserID == userID {
			n++
		}
	}
	return n
}

// handleWingWS handles the WebSocket connection from a wing.
func (s *Server) handleWingWS(w http.ResponseWriter, r *http.Request) {
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

	// Try JWT validation first, fall back to DB token
	var userID string
	var wingPublicKey string
	secret, secretErr := GenerateOrLoadSecret(s.Store, s.Config.JWTSecret)
	if secretErr == nil {
		claims, jwtErr := ValidateWingJWT(secret, token)
		if jwtErr == nil {
			userID = claims.Subject
			wingPublicKey = claims.PublicKey
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

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		log.Printf("websocket accept: %v", err)
		return
	}
	defer conn.CloseNow()

	ctx := r.Context()

	// Read registration message
	_, data, err := conn.Read(ctx)
	if err != nil {
		log.Printf("read registration: %v", err)
		return
	}

	var env ws.Envelope
	if err := json.Unmarshal(data, &env); err != nil || env.Type != ws.TypeWingRegister {
		log.Printf("expected wing.register, got: %s", string(data))
		return
	}

	var reg ws.WingRegister
	if err := json.Unmarshal(data, &reg); err != nil {
		log.Printf("bad registration: %v", err)
		return
	}

	wing := &ConnectedWing{
		ID:         uuid.New().String(),
		UserID:     userID,
		MachineID:  reg.MachineID,
		PublicKey:  wingPublicKey,
		Agents:     reg.Agents,
		Skills:     reg.Skills,
		Labels:     reg.Labels,
		Identities: reg.Identities,
		Conn:       conn,
		LastSeen:   time.Now(),
	}

	s.Wings.Add(wing)
	defer s.Wings.Remove(wing.ID)

	log.Printf("wing %s connected (user=%s machine=%s agents=%v)",
		wing.ID, userID, reg.MachineID, reg.Agents)

	// Send ack
	ack := ws.RegisteredMsg{Type: ws.TypeRegistered, WingID: wing.ID}
	ackData, _ := json.Marshal(ack)
	conn.Write(ctx, websocket.MessageText, ackData)

	// Drain any queued tasks for this user
	go s.drainQueuedTasks(ctx, wing)

	// Read loop — forward messages, never inspect content
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			log.Printf("wing %s disconnected: %v", wing.ID, err)
			return
		}

		// Meter bandwidth (applies backpressure via rate limiter)
		if s.Bandwidth != nil {
			if err := s.Bandwidth.Wait(ctx, userID, len(data)); err != nil {
				return
			}
		}

		var msg ws.Envelope
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case ws.TypeWingHeartbeat:
			s.Wings.Touch(wing.ID)

		case ws.TypeTaskChunk:
			var chunk ws.TaskChunk
			json.Unmarshal(data, &chunk)
			s.forwardChunk(chunk)

		case ws.TypeTaskDone:
			var done ws.TaskDone
			json.Unmarshal(data, &done)
			s.forwardDone(done)

		case ws.TypeTaskError:
			var errMsg ws.TaskErrorMsg
			json.Unmarshal(data, &errMsg)
			s.forwardError(errMsg)

		case ws.TypePTYStarted, ws.TypePTYOutput, ws.TypePTYExited:
			// Extract session_id and forward to browser
			var partial struct {
				SessionID string `json:"session_id"`
			}
			json.Unmarshal(data, &partial)
			s.forwardPTYToBrowser(partial.SessionID, data)

		case ws.TypeChatStarted, ws.TypeChatChunk, ws.TypeChatDone, ws.TypeChatHistory, ws.TypeChatDeleted:
			var partial struct {
				SessionID string `json:"session_id"`
			}
			json.Unmarshal(data, &partial)
			s.forwardChatToBrowser(partial.SessionID, data)
		}
	}
}

// SubmitTask routes a task to a connected wing or queues it for offline delivery.
// The relay only sees the task ID and routing info — never the content.
func (s *Server) SubmitTask(ctx context.Context, userID, identity, taskID string, payload []byte) error {
	if identity == "" {
		identity = userID
	}

	// Find a wing
	wing := s.Wings.FindByIdentity(identity)
	if wing == nil {
		wing = s.Wings.FindForUser(userID)
	}

	if wing != nil {
		// Wing online — forward directly, no DB write
		writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := wing.Conn.Write(writeCtx, websocket.MessageText, payload); err != nil {
			log.Printf("dispatch to wing %s failed: %v", wing.ID, err)
			return fmt.Errorf("dispatch failed: %w", err)
		}
		return nil
	}

	// Wing offline — queue for later delivery
	qt := &ws.QueuedTask{
		ID:        taskID,
		UserID:    userID,
		Identity:  identity,
		Payload:   string(payload),
		Status:    "pending",
		CreatedAt: time.Now().UTC(),
	}
	return s.Store.EnqueueTask(qt)
}

func (s *Server) drainQueuedTasks(ctx context.Context, wing *ConnectedWing) {
	tasks, err := s.Store.ListPendingTasks(wing.UserID)
	if err != nil {
		log.Printf("drain queue error: %v", err)
		return
	}
	for _, task := range tasks {
		writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := wing.Conn.Write(writeCtx, websocket.MessageText, []byte(task.Payload))
		cancel()
		if err != nil {
			log.Printf("drain to wing %s failed: %v", wing.ID, err)
			return
		}
		// Delete from queue after successful dispatch
		s.Store.DequeueTask(task.ID)
	}
}

// forwardChunk sends a chunk to SSE subscribers. No DB write.
func (s *Server) forwardChunk(chunk ws.TaskChunk) {
	s.streamMu.RLock()
	subs := s.streamSubs[chunk.TaskID]
	s.streamMu.RUnlock()
	for _, ch := range subs {
		select {
		case ch <- chunk.Text:
		default:
		}
	}
}

// forwardDone notifies SSE subscribers that the task completed. No DB write.
func (s *Server) forwardDone(done ws.TaskDone) {
	s.streamMu.Lock()
	subs := s.streamSubs[done.TaskID]
	delete(s.streamSubs, done.TaskID)
	s.streamMu.Unlock()
	for _, ch := range subs {
		close(ch)
	}
}

// forwardError notifies SSE subscribers that the task failed. No DB write.
func (s *Server) forwardError(errMsg ws.TaskErrorMsg) {
	s.streamMu.Lock()
	subs := s.streamSubs[errMsg.TaskID]
	delete(s.streamSubs, errMsg.TaskID)
	s.streamMu.Unlock()
	for _, ch := range subs {
		close(ch)
	}
}
