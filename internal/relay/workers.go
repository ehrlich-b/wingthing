package relay

import (
	"bytes"
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
	ID           string
	UserID       string
	WingID       string
	PublicKey    string
	OrgID        string // org ID this wing serves
	Locked       bool
	AllowedCount int
	Conn         *websocket.Conn
	LastSeen     time.Time
}

// WingEvent is sent to dashboard subscribers for wing/session lifecycle events.
type WingEvent struct {
	Type         string `json:"type"`                    // "wing.online", "wing.offline", "session.attention"
	WingID       string `json:"wing_id"`
	PublicKey    string `json:"public_key,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
	Locked       *bool  `json:"locked,omitempty"`
	AllowedCount *int   `json:"allowed_count,omitempty"`
}

// eventSub is a dashboard subscriber with its org memberships.
type eventSub struct {
	userID string
	orgIDs []string
	ch     chan WingEvent
}

// WingRegistry tracks all connected wings.
type WingRegistry struct {
	mu    sync.RWMutex
	wings map[string]*ConnectedWing // wingID → wing

	// Dashboard subscribers: userID → list of subs
	subMu sync.RWMutex
	subs  map[string][]*eventSub
}

func NewWingRegistry() *WingRegistry {
	return &WingRegistry{
		wings: make(map[string]*ConnectedWing),
		subs:  make(map[string][]*eventSub),
	}
}

// Subscribe registers a dashboard subscriber with its org memberships.
// Events are delivered if the subscriber's userID matches the wing owner
// OR if the wing's orgID appears in the subscriber's orgIDs.
func (r *WingRegistry) Subscribe(userID string, orgIDs []string, ch chan WingEvent) {
	r.subMu.Lock()
	r.subs[userID] = append(r.subs[userID], &eventSub{userID: userID, orgIDs: orgIDs, ch: ch})
	r.subMu.Unlock()
}

func (r *WingRegistry) Unsubscribe(userID string, ch chan WingEvent) {
	r.subMu.Lock()
	defer r.subMu.Unlock()
	list := r.subs[userID]
	for i, s := range list {
		if s.ch == ch {
			r.subs[userID] = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(r.subs[userID]) == 0 {
		delete(r.subs, userID)
	}
}

// notify sends an event to all subscribers of a specific userID.
func (r *WingRegistry) notify(userID string, ev WingEvent) {
	r.subMu.RLock()
	defer r.subMu.RUnlock()
	for _, s := range r.subs[userID] {
		select {
		case s.ch <- ev:
		default:
		}
	}
}

// UpdateUserOrgs updates the org list for all active subscribers of a user.
// Returns true if the user had any active subscribers (i.e., was worth updating).
func (r *WingRegistry) UpdateUserOrgs(userID string, orgIDs []string) bool {
	r.subMu.Lock()
	defer r.subMu.Unlock()
	subs := r.subs[userID]
	if len(subs) == 0 {
		return false
	}
	for _, s := range subs {
		s.orgIDs = orgIDs
	}
	return true
}

// notifyWing sends an event to the wing owner and any org members subscribed.
func (r *WingRegistry) notifyWing(ownerID, orgID string, ev WingEvent) {
	r.subMu.RLock()
	defer r.subMu.RUnlock()
	// Notify owner
	for _, s := range r.subs[ownerID] {
		select {
		case s.ch <- ev:
		default:
		}
	}
	// Notify org members (skip owner, already notified)
	if orgID == "" {
		return
	}
	for uid, subs := range r.subs {
		if uid == ownerID {
			continue
		}
		for _, s := range subs {
			for _, oid := range s.orgIDs {
				if oid == orgID {
					select {
					case s.ch <- ev:
					default:
					}
					break
				}
			}
		}
	}
}

func (r *WingRegistry) Add(w *ConnectedWing) {
	r.mu.Lock()
	r.wings[w.ID] = w
	r.mu.Unlock()
	locked := w.Locked
	allowedCount := w.AllowedCount
	ev := WingEvent{
		Type:         "wing.online",
		WingID:       w.WingID,
		PublicKey:    w.PublicKey,
		Locked:       &locked,
		AllowedCount: &allowedCount,
	}
	r.notifyWing(w.UserID, w.OrgID, ev)
}

func (r *WingRegistry) Remove(id string) {
	r.mu.Lock()
	w := r.wings[id]
	delete(r.wings, id)
	r.mu.Unlock()
	if w != nil {
		ev := WingEvent{
			Type:   "wing.offline",
			WingID: w.WingID,
		}
		r.notifyWing(w.UserID, w.OrgID, ev)
	}
}

// UpdateConfig updates a wing's lock state and notifies subscribers.
func (r *WingRegistry) UpdateConfig(id string, locked bool, allowedCount int) {
	r.mu.Lock()
	w := r.wings[id]
	if w != nil {
		w.Locked = locked
		w.AllowedCount = allowedCount
	}
	r.mu.Unlock()
	if w != nil {
		locked := w.Locked
		allowedCount := w.AllowedCount
		ev := WingEvent{
			Type:         "wing.config",
			WingID:       w.WingID,
			PublicKey:    w.PublicKey,
			Locked:       &locked,
			AllowedCount: &allowedCount,
		}
		r.notifyWing(w.UserID, w.OrgID, ev)
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
		ID:           uuid.New().String(),
		UserID:       userID,
		WingID:       reg.WingID,
		PublicKey:    wingPublicKey,
		OrgID:        reg.OrgSlug,
		Locked:       reg.Locked,
		AllowedCount: reg.AllowedCount,
		Conn:         conn,
		LastSeen:     time.Now(),
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

	log.Printf("wing %s connected (user=%s wing=%s)", wing.ID, userID, reg.WingID)

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

		case ws.TypeWingConfig:
			var cfg ws.WingConfig
			json.Unmarshal(data, &cfg)
			s.Wings.UpdateConfig(wing.ID, cfg.Locked, cfg.AllowedCount)
			// Edge: forward config event to login for cluster-wide propagation
			if s.Config.LoginNodeAddr != "" {
				go s.forwardWingEvent(wing, cfg.Locked, cfg.AllowedCount)
			}

		case ws.TypePTYStarted, ws.TypePTYOutput, ws.TypePTYExited, ws.TypePasskeyChallenge:
			// Extract session_id and forward to browser
			var partial struct {
				SessionID string `json:"session_id"`
			}
			json.Unmarshal(data, &partial)
			s.forwardPTYToBrowser(partial.SessionID, data)

		case ws.TypeTunnelResponse:
			var resp ws.TunnelResponse
			json.Unmarshal(data, &resp)
			s.forwardTunnelToBrowser(resp.RequestID, data, true)

		case ws.TypeTunnelStream:
			var stream ws.TunnelStream
			json.Unmarshal(data, &stream)
			s.forwardTunnelToBrowser(stream.RequestID, data, stream.Done)

		case ws.TypeSessionAttention:
			var attn ws.SessionAttention
			json.Unmarshal(data, &attn)
			ev := WingEvent{
				Type:      "session.attention",
				WingID:    wing.WingID,
				SessionID: attn.SessionID,
			}
			s.Wings.notifyWing(wing.UserID, wing.OrgID, ev)

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

// forwardWingEvent POSTs a wing config event to the login node for cluster-wide propagation.
func (s *Server) forwardWingEvent(wing *ConnectedWing, locked bool, allowedCount int) {
	body, _ := json.Marshal(map[string]any{
		"type":          "wing.config",
		"wing_id":       wing.WingID,
		"user_id":       wing.UserID,
		"org_id":        wing.OrgID,
		"public_key":    wing.PublicKey,
		"locked":        locked,
		"allowed_count": allowedCount,
	})
	client := &http.Client{Timeout: 3 * time.Second}
	req, _ := http.NewRequest("POST", s.Config.LoginNodeAddr+"/internal/wing-event", bytes.NewReader(body))
	if req == nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
}

// forwardTunnelToBrowser routes an encrypted tunnel response from wing to the originating browser.
func (s *Server) forwardTunnelToBrowser(requestID string, data []byte, done bool) {
	s.tunnelMu.Lock()
	bc := s.tunnelRequests[requestID]
	if done {
		delete(s.tunnelRequests, requestID)
	}
	s.tunnelMu.Unlock()
	if bc == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bc.Write(ctx, websocket.MessageText, data)
}
