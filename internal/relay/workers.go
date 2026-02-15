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

	// Dashboard subscribers: dual-indexed by userID and orgID for O(1) lookups
	subMu   sync.RWMutex
	subs    map[string][]*eventSub // userID → subs
	orgSubs map[string][]*eventSub // orgID → subs
}

func NewWingRegistry() *WingRegistry {
	return &WingRegistry{
		wings:   make(map[string]*ConnectedWing),
		subs:    make(map[string][]*eventSub),
		orgSubs: make(map[string][]*eventSub),
	}
}

// Subscribe registers a dashboard subscriber with its org memberships.
// Events are delivered if the subscriber's userID matches the wing owner
// OR if the wing's orgID appears in the subscriber's orgIDs.
func (r *WingRegistry) Subscribe(userID string, orgIDs []string, ch chan WingEvent) {
	r.subMu.Lock()
	sub := &eventSub{userID: userID, orgIDs: orgIDs, ch: ch}
	r.subs[userID] = append(r.subs[userID], sub)
	for _, oid := range orgIDs {
		r.orgSubs[oid] = append(r.orgSubs[oid], sub)
	}
	r.subMu.Unlock()
}

func (r *WingRegistry) Unsubscribe(userID string, ch chan WingEvent) {
	r.subMu.Lock()
	defer r.subMu.Unlock()
	list := r.subs[userID]
	for i, s := range list {
		if s.ch == ch {
			// Remove from orgSubs
			for _, oid := range s.orgIDs {
				oList := r.orgSubs[oid]
				for j, os := range oList {
					if os.ch == ch {
						r.orgSubs[oid] = append(oList[:j], oList[j+1:]...)
						break
					}
				}
				if len(r.orgSubs[oid]) == 0 {
					delete(r.orgSubs, oid)
				}
			}
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
		// Remove from old orgSubs
		for _, oid := range s.orgIDs {
			oList := r.orgSubs[oid]
			for j, os := range oList {
				if os == s {
					r.orgSubs[oid] = append(oList[:j], oList[j+1:]...)
					break
				}
			}
			if len(r.orgSubs[oid]) == 0 {
				delete(r.orgSubs, oid)
			}
		}
		// Update and add to new orgSubs
		s.orgIDs = orgIDs
		for _, oid := range orgIDs {
			r.orgSubs[oid] = append(r.orgSubs[oid], s)
		}
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
	// Notify org members via orgSubs index (skip owner, already notified)
	if orgID == "" {
		return
	}
	for _, s := range r.orgSubs[orgID] {
		if s.userID == ownerID {
			continue
		}
		select {
		case s.ch <- ev:
		default:
		}
	}
}

func (r *WingRegistry) Add(w *ConnectedWing) {
	r.mu.Lock()
	r.wings[w.ID] = w
	r.mu.Unlock()
}

func (r *WingRegistry) Remove(id string) *ConnectedWing {
	r.mu.Lock()
	w := r.wings[id]
	delete(r.wings, id)
	r.mu.Unlock()
	return w
}

// UpdateConfig updates a wing's lock state. Returns the wing for event dispatch.
func (r *WingRegistry) UpdateConfig(id string, locked bool, allowedCount int) *ConnectedWing {
	r.mu.Lock()
	w := r.wings[id]
	if w != nil {
		w.Locked = locked
		w.AllowedCount = allowedCount
	}
	r.mu.Unlock()
	return w
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
	s.dispatchWingEvent("wing.online", wing)
	defer func() {
		if w := s.Wings.Remove(wing.ID); w != nil {
			s.dispatchWingEvent("wing.offline", w)
		}
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
			if w := s.Wings.UpdateConfig(wing.ID, cfg.Locked, cfg.AllowedCount); w != nil {
				s.dispatchWingEvent("wing.config", w)
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
			if s.IsEdge() && s.Config.LoginNodeAddr != "" {
				payload, _ := json.Marshal(map[string]any{
					"type":       "session.attention",
					"wing_id":    wing.WingID,
					"user_id":    wing.UserID,
					"org_id":     wing.OrgID,
					"session_id": attn.SessionID,
				})
				go s.forwardPayloadToLogin(payload)
			} else {
				s.Wings.notifyWing(wing.UserID, wing.OrgID, ev)
				if s.IsLogin() && s.WingMap != nil {
					payload, _ := json.Marshal(map[string]any{
						"type":       "session.attention",
						"wing_id":    wing.WingID,
						"user_id":    wing.UserID,
						"org_id":     wing.OrgID,
						"session_id": attn.SessionID,
					})
					go s.broadcastToEdges(payload)
				}
			}

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

// dispatchWingEvent routes a wing lifecycle event through the correct path.
// Edge: register/deregister with login wingMap, forward event to login.
// Login/single-node: update wingMap, deliver locally, broadcast to edges.
func (s *Server) dispatchWingEvent(eventType string, wing *ConnectedWing) {
	// Edge: register/deregister with login (synchronous so WingMap is updated
	// before browsers are notified), then forward event.
	if s.IsEdge() && s.Config.LoginNodeAddr != "" {
		switch eventType {
		case "wing.online":
			s.registerWingWithLogin(wing)
		case "wing.offline":
			s.deregisterWingWithLogin(wing.WingID)
		}
		go s.forwardWingEvent(eventType, wing)
		return
	}

	// Login or single-node: update wingMap
	if s.WingMap != nil {
		switch eventType {
		case "wing.online", "wing.config":
			s.WingMap.Register(wing.WingID, WingLocation{
				MachineID:    s.Config.FlyMachineID,
				UserID:       wing.UserID,
				OrgID:        wing.OrgID,
				PublicKey:    wing.PublicKey,
				Locked:       wing.Locked,
				AllowedCount: wing.AllowedCount,
			})
		case "wing.offline":
			s.WingMap.Deregister(wing.WingID)
		}
	}

	// Deliver locally
	var ev WingEvent
	if eventType == "wing.offline" {
		ev = WingEvent{Type: eventType, WingID: wing.WingID}
	} else {
		locked := wing.Locked
		allowedCount := wing.AllowedCount
		ev = WingEvent{
			Type:         eventType,
			WingID:       wing.WingID,
			PublicKey:    wing.PublicKey,
			Locked:       &locked,
			AllowedCount: &allowedCount,
		}
	}
	s.Wings.notifyWing(wing.UserID, wing.OrgID, ev)

	// Login: broadcast to all edges
	if s.IsLogin() && s.WingMap != nil {
		payload, _ := json.Marshal(map[string]any{
			"type":          eventType,
			"wing_id":       wing.WingID,
			"user_id":       wing.UserID,
			"org_id":        wing.OrgID,
			"public_key":    wing.PublicKey,
			"locked":        wing.Locked,
			"allowed_count": wing.AllowedCount,
		})
		go s.broadcastToEdges(payload)
	}
}

// forwardWingEvent POSTs a wing event to the login node for cluster-wide propagation.
func (s *Server) forwardWingEvent(eventType string, wing *ConnectedWing) {
	payload, _ := json.Marshal(map[string]any{
		"type":          eventType,
		"wing_id":       wing.WingID,
		"user_id":       wing.UserID,
		"org_id":        wing.OrgID,
		"public_key":    wing.PublicKey,
		"locked":        wing.Locked,
		"allowed_count": wing.AllowedCount,
	})
	s.forwardPayloadToLogin(payload)
}

// forwardPayloadToLogin POSTs a raw JSON payload to the login node's wing-event endpoint.
func (s *Server) forwardPayloadToLogin(payload []byte) {
	client := &http.Client{Timeout: 3 * time.Second}
	req, _ := http.NewRequest("POST", s.Config.LoginNodeAddr+"/internal/wing-event", bytes.NewReader(payload))
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
