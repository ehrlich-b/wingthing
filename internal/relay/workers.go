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

// ConnectedWing represents a wing connected via WebSocket.
type ConnectedWing struct {
	ID          string
	UserID      string
	WingID      string
	Hostname    string // display name from os.Hostname()
	Platform    string // runtime.GOOS from wing
	Version     string // build version from wing
	PublicKey   string
	Agents      []string
	Skills      []string
	Labels      []string
	Identities  []string
	Projects    []ws.WingProject
	EggConfig   string // serialized YAML of wing's egg config
	OrgID       string   // org slug this wing serves (from --org flag)
	AllowEmails []string // explicit email allow list (from --allow flag)
	RootDir     string   // root directory constraint (from --root flag)
	Conn        *websocket.Conn
	LastSeen    time.Time
}

// WingEvent is sent to dashboard subscribers when a wing connects or disconnects.
type WingEvent struct {
	Type      string           `json:"type"`       // "wing.online" or "wing.offline"
	ConnID    string           `json:"conn_id"`
	WingID    string           `json:"wing_id"`
	Hostname  string           `json:"hostname,omitempty"`
	Platform  string           `json:"platform,omitempty"`
	Version   string           `json:"version,omitempty"`
	Agents    []string         `json:"agents,omitempty"`
	Labels    []string         `json:"labels,omitempty"`
	PublicKey string           `json:"public_key,omitempty"`
	Projects  []ws.WingProject `json:"projects,omitempty"`
}

// WingRegistry tracks all connected wings.
type WingRegistry struct {
	mu    sync.RWMutex
	wings map[string]*ConnectedWing // wingID → wing

	dirMu       sync.Mutex
	dirRequests map[string]chan *ws.DirResults

	sessMu       sync.Mutex
	sessRequests map[string]chan *ws.SessionsSync // requestID → response channel

	histMu       sync.Mutex
	histRequests map[string]chan *ws.SessionsHistoryResults // requestID → response channel

	auditMu       sync.Mutex
	auditRequests map[string]chan any // requestID → channel for AuditChunk/AuditDone/AuditError

	// Dashboard subscribers: userID → list of channels
	subMu sync.RWMutex
	subs  map[string][]chan WingEvent

	// OnWingEvent is called after Add/Remove with the wing and event.
	// The Server uses this to notify org members (the registry itself
	// only knows about the wing owner).
	OnWingEvent func(wing *ConnectedWing, ev WingEvent)
}

func NewWingRegistry() *WingRegistry {
	return &WingRegistry{
		wings:         make(map[string]*ConnectedWing),
		dirRequests:   make(map[string]chan *ws.DirResults),
		sessRequests:  make(map[string]chan *ws.SessionsSync),
		histRequests:  make(map[string]chan *ws.SessionsHistoryResults),
		auditRequests: make(map[string]chan any),
		subs:          make(map[string][]chan WingEvent),
	}
}

func (r *WingRegistry) RegisterDirRequest(reqID string, ch chan *ws.DirResults) {
	r.dirMu.Lock()
	r.dirRequests[reqID] = ch
	r.dirMu.Unlock()
}

func (r *WingRegistry) UnregisterDirRequest(reqID string) {
	r.dirMu.Lock()
	delete(r.dirRequests, reqID)
	r.dirMu.Unlock()
}

func (r *WingRegistry) ResolveDirRequest(reqID string, results *ws.DirResults) {
	r.dirMu.Lock()
	ch := r.dirRequests[reqID]
	r.dirMu.Unlock()
	if ch != nil {
		select {
		case ch <- results:
		default:
		}
	}
}

func (r *WingRegistry) RegisterSessionRequest(reqID string, ch chan *ws.SessionsSync) {
	r.sessMu.Lock()
	r.sessRequests[reqID] = ch
	r.sessMu.Unlock()
}

func (r *WingRegistry) UnregisterSessionRequest(reqID string) {
	r.sessMu.Lock()
	delete(r.sessRequests, reqID)
	r.sessMu.Unlock()
}

func (r *WingRegistry) ResolveSessionRequest(reqID string, results *ws.SessionsSync) {
	r.sessMu.Lock()
	ch := r.sessRequests[reqID]
	r.sessMu.Unlock()
	if ch != nil {
		select {
		case ch <- results:
		default:
		}
	}
}

func (r *WingRegistry) RegisterHistoryRequest(reqID string, ch chan *ws.SessionsHistoryResults) {
	r.histMu.Lock()
	r.histRequests[reqID] = ch
	r.histMu.Unlock()
}

func (r *WingRegistry) UnregisterHistoryRequest(reqID string) {
	r.histMu.Lock()
	delete(r.histRequests, reqID)
	r.histMu.Unlock()
}

func (r *WingRegistry) ResolveHistoryRequest(reqID string, results *ws.SessionsHistoryResults) {
	r.histMu.Lock()
	ch := r.histRequests[reqID]
	r.histMu.Unlock()
	if ch != nil {
		select {
		case ch <- results:
		default:
		}
	}
}

func (r *WingRegistry) RegisterAuditRequest(reqID string, ch chan any) {
	r.auditMu.Lock()
	r.auditRequests[reqID] = ch
	r.auditMu.Unlock()
}

func (r *WingRegistry) UnregisterAuditRequest(reqID string) {
	r.auditMu.Lock()
	delete(r.auditRequests, reqID)
	r.auditMu.Unlock()
}

func (r *WingRegistry) ResolveAuditMessage(reqID string, msg any) {
	r.auditMu.Lock()
	ch := r.auditRequests[reqID]
	r.auditMu.Unlock()
	if ch != nil {
		select {
		case ch <- msg:
		default:
		}
	}
}

func (r *WingRegistry) Subscribe(userID string, ch chan WingEvent) {
	r.subMu.Lock()
	r.subs[userID] = append(r.subs[userID], ch)
	r.subMu.Unlock()
}

func (r *WingRegistry) Unsubscribe(userID string, ch chan WingEvent) {
	r.subMu.Lock()
	defer r.subMu.Unlock()
	list := r.subs[userID]
	for i, c := range list {
		if c == ch {
			r.subs[userID] = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(r.subs[userID]) == 0 {
		delete(r.subs, userID)
	}
}

func (r *WingRegistry) notify(userID string, ev WingEvent) {
	r.subMu.RLock()
	defer r.subMu.RUnlock()
	for _, ch := range r.subs[userID] {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (r *WingRegistry) Add(w *ConnectedWing) {
	r.mu.Lock()
	r.wings[w.ID] = w
	r.mu.Unlock()
	ev := WingEvent{
		Type:      "wing.online",
		ConnID:    w.ID,
		WingID:    w.WingID,
		Hostname:  w.Hostname,
		Platform:  w.Platform,
		Version:   w.Version,
		Agents:    w.Agents,
		Labels:    w.Labels,
		PublicKey: w.PublicKey,
		Projects:  w.Projects,
	}
	r.notify(w.UserID, ev)
	if r.OnWingEvent != nil {
		r.OnWingEvent(w, ev)
	}
}

func (r *WingRegistry) Remove(id string) {
	r.mu.Lock()
	w := r.wings[id]
	delete(r.wings, id)
	r.mu.Unlock()
	if w != nil {
		ev := WingEvent{
			Type:   "wing.offline",
			ConnID: w.ID,
			WingID: w.WingID,
		}
		r.notify(w.UserID, ev)
		if r.OnWingEvent != nil {
			r.OnWingEvent(w, ev)
		}
	}
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

// All returns all connected wings.
func (r *WingRegistry) All() []*ConnectedWing {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*ConnectedWing, 0, len(r.wings))
	for _, w := range r.wings {
		result = append(result, w)
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

// BroadcastAll sends a message to every connected wing.
func (r *WingRegistry) BroadcastAll(ctx context.Context, data []byte) {
	r.mu.RLock()
	wings := make([]*ConnectedWing, 0, len(r.wings))
	for _, w := range r.wings {
		wings = append(wings, w)
	}
	r.mu.RUnlock()

	for _, w := range wings {
		writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		w.Conn.Write(writeCtx, websocket.MessageText, data)
		cancel()
	}
}

// CloseAll closes all connected wing WebSockets.
func (r *WingRegistry) CloseAll() {
	r.mu.RLock()
	wings := make([]*ConnectedWing, 0, len(r.wings))
	for _, w := range r.wings {
		wings = append(wings, w)
	}
	r.mu.RUnlock()

	for _, w := range wings {
		w.Conn.Close(websocket.StatusGoingAway, "server shutting down")
	}
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
	if userID == "" && s.Store != nil {
		var err error
		userID, _, err = s.Store.ValidateToken(token)
		if err != nil {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
	}
	if userID == "" {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		log.Printf("websocket accept: %v", err)
		return
	}
	conn.SetReadLimit(512 * 1024) // 512KB — replay chunks can be large
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
		ID:          uuid.New().String(),
		UserID:      userID,
		WingID:      reg.WingID,
		Hostname:    reg.Hostname,
		Platform:    reg.Platform,
		Version:     reg.Version,
		PublicKey:    wingPublicKey,
		Agents:      reg.Agents,
		Skills:      reg.Skills,
		Labels:      reg.Labels,
		Identities:  reg.Identities,
		Projects:    reg.Projects,
		OrgID:       reg.OrgSlug,
		AllowEmails: reg.AllowEmails,
		RootDir:     reg.RootDir,
		Conn:        conn,
		LastSeen:    time.Now(),
	}

	// Validate org membership if org specified (accepts slug or ID)
	if wing.OrgID != "" {
		if s.Store != nil {
			// Login node: resolve org reference (tries ID then slug)
			org, orgErr := s.Store.ResolveOrg(wing.OrgID, userID)
			if orgErr != nil {
				errMsg := ws.ErrorMsg{Type: ws.TypeError, Message: orgErr.Error()}
				data, _ := json.Marshal(errMsg)
				conn.Write(ctx, websocket.MessageText, data)
				return
			}
			if org == nil {
				errMsg := ws.ErrorMsg{Type: ws.TypeError, Message: "org not found: " + wing.OrgID}
				data, _ := json.Marshal(errMsg)
				conn.Write(ctx, websocket.MessageText, data)
				return
			}
			// Store the resolved org ID (not the slug)
			wing.OrgID = org.ID
			role := s.Store.GetOrgMemberRole(org.ID, userID)
			if role != "owner" && role != "admin" {
				errMsg := ws.ErrorMsg{Type: ws.TypeError, Message: "not authorized to register wing for org: " + org.Name}
				data, _ := json.Marshal(errMsg)
				conn.Write(ctx, websocket.MessageText, data)
				return
			}
		} else if s.Config.LoginNodeAddr != "" {
			// Edge node: proxy org check to login
			resolvedID, ok := s.validateOrgViaLogin(ctx, wing.OrgID, userID)
			if !ok {
				errMsg := ws.ErrorMsg{Type: ws.TypeError, Message: "org validation failed for: " + wing.OrgID}
				data, _ := json.Marshal(errMsg)
				conn.Write(ctx, websocket.MessageText, data)
				return
			}
			wing.OrgID = resolvedID
		}
	}

	s.Wings.Add(wing)
	defer func() {
		s.Wings.Remove(wing.ID)
	}()

	log.Printf("wing %s connected (user=%s wing=%s agents=%v)",
		wing.ID, userID, reg.WingID, reg.Agents)

	// Send ack
	ack := ws.RegisteredMsg{Type: ws.TypeRegistered, WingID: wing.ID}
	ackData, _ := json.Marshal(ack)
	conn.Write(ctx, websocket.MessageText, ackData)

	// Read loop — forward messages, never inspect content
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

		case ws.TypePTYReclaim:
			var reclaim ws.PTYReclaim
			json.Unmarshal(data, &reclaim)

			// If session was recently deleted, kill it on the wing instead of reclaiming
			if s.PTY.IsTombstoned(reclaim.SessionID) {
				kill := ws.PTYKill{Type: ws.TypePTYKill, SessionID: reclaim.SessionID}
				killData, _ := json.Marshal(kill)
				wing.Conn.Write(ctx, websocket.MessageText, killData)
				log.Printf("pty session %s: tombstoned, sending kill to wing %s", reclaim.SessionID, wing.ID)
				break
			}

			session := s.PTY.Get(reclaim.SessionID)
			if session != nil {
				session.mu.Lock()
				session.WingID = wing.ID
				session.mu.Unlock()
				log.Printf("pty session %s reclaimed by wing %s", reclaim.SessionID, wing.ID)
			} else {
				// Session unknown (relay restarted) — recreate from wing's egg data
				s.PTY.Add(&PTYSession{
					ID:     reclaim.SessionID,
					WingID: wing.ID,
					UserID: wing.UserID,
					Agent:  reclaim.Agent,
					CWD:    reclaim.CWD,
					Status: "detached",
				})
				log.Printf("pty session %s restored from wing %s (agent=%s)", reclaim.SessionID, wing.ID, reclaim.Agent)
			}

		case ws.TypeDirResults:
			var results ws.DirResults
			json.Unmarshal(data, &results)
			s.Wings.ResolveDirRequest(results.RequestID, &results)

		case ws.TypeSessionsSync:
			var sync ws.SessionsSync
			json.Unmarshal(data, &sync)
			// Only remove stale sessions on explicit requests, not heartbeats
			s.PTY.SyncFromWing(wing.ID, wing.UserID, sync.Sessions, sync.RequestID != "")
			if sync.RequestID != "" {
				s.Wings.ResolveSessionRequest(sync.RequestID, &sync)
			}

		case ws.TypeSessionsHistoryResults:
			var results ws.SessionsHistoryResults
			json.Unmarshal(data, &results)
			s.Wings.ResolveHistoryRequest(results.RequestID, &results)

		case ws.TypeAuditChunk:
			var chunk ws.AuditChunk
			json.Unmarshal(data, &chunk)
			s.Wings.ResolveAuditMessage(chunk.RequestID, &chunk)

		case ws.TypeAuditDone:
			var done ws.AuditDone
			json.Unmarshal(data, &done)
			s.Wings.ResolveAuditMessage(done.RequestID, &done)

		case ws.TypeAuditError:
			var auditErr ws.AuditError
			json.Unmarshal(data, &auditErr)
			s.Wings.ResolveAuditMessage(auditErr.RequestID, &auditErr)
		}
	}
}

// validateOrgViaLogin proxies org membership validation to the login node.
// Returns (resolvedOrgID, ok). The resolved ID is always a UUID.
func (s *Server) validateOrgViaLogin(ctx context.Context, orgRef, userID string) (string, bool) {
	client := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequestWithContext(ctx, "GET",
		s.Config.LoginNodeAddr+"/internal/org-check/"+orgRef+"/"+userID, nil)
	if err != nil {
		return "", false
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	var result struct {
		OK    bool   `json:"ok"`
		OrgID string `json:"org_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", false
	}
	return result.OrgID, result.OK
}
