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
		// Also match by userID as default identity
		if w.UserID == identity {
			return w
		}
	}
	return nil
}

func (r *WingRegistry) Touch(wingID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if w, ok := r.wings[wingID]; ok {
		w.LastSeen = time.Now()
	}
}

// handleWingWS handles the WebSocket connection from a wing.
func (s *Server) handleWingWS(w http.ResponseWriter, r *http.Request) {
	// Auth: extract token from query param or header
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

	userID, _, err := s.Store.ValidateToken(token)
	if err != nil {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // allow any origin for now
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

	// Read loop — handle heartbeats, task results, chunks
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			log.Printf("wing %s disconnected: %v", wing.ID, err)
			return
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
			s.handleTaskChunk(chunk)

		case ws.TypeTaskDone:
			var done ws.TaskDone
			json.Unmarshal(data, &done)
			s.handleTaskDone(done)

		case ws.TypeTaskError:
			var errMsg ws.TaskErrorMsg
			json.Unmarshal(data, &errMsg)
			s.handleTaskError(errMsg)
		}
	}
}

// SubmitRelayTask creates a task and routes it to a connected wing.
func (s *Server) SubmitRelayTask(ctx context.Context, userID, prompt, skill, agent, isolation, target string) (*ws.RelayTask, error) {
	identity := target
	if identity == "" {
		identity = userID
	}

	task := &ws.RelayTask{
		ID:        fmt.Sprintf("rt-%s", time.Now().Format("20060102-150405")),
		UserID:    userID,
		Identity:  identity,
		Prompt:    prompt,
		Skill:     skill,
		Agent:     agent,
		Isolation: isolation,
		Status:    "pending",
		CreatedAt: time.Now().UTC(),
	}

	if err := s.Store.CreateRelayTask(task); err != nil {
		return nil, fmt.Errorf("create relay task: %w", err)
	}

	// Find a wing
	wing := s.Wings.FindByIdentity(identity)
	if wing == nil {
		wing = s.Wings.FindForUser(userID)
	}

	if wing != nil {
		s.dispatchTask(ctx, wing, task)
	}
	// If no wing online, task stays queued — delivered when wing connects

	return task, nil
}

func (s *Server) dispatchTask(ctx context.Context, wing *ConnectedWing, task *ws.RelayTask) {
	submit := ws.TaskSubmit{
		Type:      ws.TypeTaskSubmit,
		TaskID:    task.ID,
		Prompt:    task.Prompt,
		Skill:     task.Skill,
		Agent:     task.Agent,
		Isolation: task.Isolation,
	}
	data, _ := json.Marshal(submit)
	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := wing.Conn.Write(writeCtx, websocket.MessageText, data); err != nil {
		log.Printf("dispatch to wing %s failed: %v", wing.ID, err)
		return
	}

	now := time.Now().UTC()
	task.Status = "running"
	task.WingID = wing.ID
	task.StartedAt = &now
	s.Store.UpdateRelayTask(task)
}

func (s *Server) drainQueuedTasks(ctx context.Context, wing *ConnectedWing) {
	tasks, err := s.Store.ListPendingRelayTasks(wing.UserID)
	if err != nil {
		log.Printf("drain queue error: %v", err)
		return
	}
	for _, task := range tasks {
		s.dispatchTask(ctx, wing, task)
	}
}

func (s *Server) handleTaskChunk(chunk ws.TaskChunk) {
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

func (s *Server) handleTaskDone(done ws.TaskDone) {
	now := time.Now().UTC()
	s.Store.CompleteRelayTask(done.TaskID, done.Output, &now)

	s.streamMu.Lock()
	subs := s.streamSubs[done.TaskID]
	delete(s.streamSubs, done.TaskID)
	s.streamMu.Unlock()
	for _, ch := range subs {
		close(ch)
	}
}

func (s *Server) handleTaskError(errMsg ws.TaskErrorMsg) {
	now := time.Now().UTC()
	s.Store.FailRelayTask(errMsg.TaskID, errMsg.Error, &now)

	s.streamMu.Lock()
	subs := s.streamSubs[errMsg.TaskID]
	delete(s.streamSubs, errMsg.TaskID)
	s.streamMu.Unlock()
	for _, ch := range subs {
		close(ch)
	}
}
