package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/ehrlich-b/wingthing/internal/ntfy"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

// ConnectedWing represents a wing connected via WebSocket.
type ConnectedWing struct {
	ID             string
	UserID         string
	WingID         string
	PublicKey      string
	OrgID          string // org ID this wing serves
	Locked         bool
	AllowedCount   int
	PurposeBinding bool
	DirectMCP      bool
	HostedRelay    string
	// Revision is local to one connection generation. It starts at one and
	// increases whenever policy/capability state changes, allowing split-node
	// snapshots and real-time events to be ordered without comparing host clocks.
	Revision    uint64
	Conn        *websocket.Conn
	ConnectedAt time.Time
	LastSeen    time.Time
}

// WingEvent is sent to dashboard subscribers for wing/session lifecycle events.
type WingEvent struct {
	Type           string  `json:"type"` // "wing.online", "wing.offline", "session.attention"
	WingID         string  `json:"wing_id"`
	PublicKey      string  `json:"public_key,omitempty"`
	SessionID      string  `json:"session_id,omitempty"`
	Locked         *bool   `json:"locked,omitempty"`
	AllowedCount   *int    `json:"allowed_count,omitempty"`
	PurposeBinding *bool   `json:"purpose_binding,omitempty"`
	DirectMCP      *bool   `json:"direct_mcp,omitempty"`
	HostedRelay    *string `json:"hosted_relay,omitempty"`
	UserID         string  `json:"user_id,omitempty"`
	Owner          string  `json:"owner,omitempty"`
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

func (r *WingRegistry) Add(w *ConnectedWing) *ConnectedWing {
	stored := *w
	if stored.ConnectedAt.IsZero() {
		stored.ConnectedAt = stored.LastSeen
	}
	if stored.Revision == 0 {
		stored.Revision = 1
	}
	r.mu.Lock()
	r.wings[w.ID] = &stored
	r.mu.Unlock()
	return &stored
}

// Activate publishes a connection and removes older overlapping sockets for
// the same stable wing ID. Reconnects can briefly overlap; only the newest
// generation may publish policy or runtime events.
func (r *WingRegistry) Activate(w *ConnectedWing) (*ConnectedWing, []*ConnectedWing, bool) {
	stored := *w
	if stored.ConnectedAt.IsZero() {
		stored.ConnectedAt = time.Now()
	}
	if stored.LastSeen.IsZero() {
		stored.LastSeen = stored.ConnectedAt
	}
	if stored.Revision == 0 {
		stored.Revision = 1
	}
	r.mu.Lock()
	// Device enrollment lets each account choose its local stable wing ID. A
	// reconnect may replace only another socket authenticated as the same owner;
	// otherwise a user who learns somebody else's wing ID could evict it without
	// ever passing that wing's access checks.
	for _, candidate := range r.wings {
		if candidate.WingID == stored.WingID && candidate.UserID != stored.UserID {
			r.mu.Unlock()
			return &stored, nil, false
		}
	}
	r.wings[stored.ID] = &stored
	newest := &stored
	for _, candidate := range r.wings {
		if candidate.WingID == stored.WingID && (candidate.ConnectedAt.After(newest.ConnectedAt) ||
			(candidate.ConnectedAt.Equal(newest.ConnectedAt) && candidate.ID > newest.ID)) {
			newest = candidate
		}
	}
	var superseded []*ConnectedWing
	for connectionID, candidate := range r.wings {
		if candidate.WingID == stored.WingID && connectionID != newest.ID {
			superseded = append(superseded, candidate)
			delete(r.wings, connectionID)
		}
	}
	r.mu.Unlock()
	return &stored, superseded, newest.ID == stored.ID
}

func (r *WingRegistry) Remove(id string) *ConnectedWing {
	w, _ := r.RemoveConnection(id)
	return w
}

// RemoveConnection reports whether removing this socket took the stable wing
// offline, as opposed to completing an old side of a reconnect overlap.
func (r *WingRegistry) RemoveConnection(id string) (*ConnectedWing, bool) {
	r.mu.Lock()
	w := r.wings[id]
	delete(r.wings, id)
	logicalOffline := w != nil
	if w != nil {
		for _, candidate := range r.wings {
			if candidate.WingID == w.WingID {
				logicalOffline = false
				break
			}
		}
	}
	r.mu.Unlock()
	return w, logicalOffline
}

// UpdateConfig updates a wing's lock state. Returns the wing for event dispatch.
func (r *WingRegistry) UpdateConfig(id string, locked bool, allowedCount int, directMCP bool, hostedRelay string) *ConnectedWing {
	r.mu.Lock()
	current := r.wings[id]
	var updated *ConnectedWing
	if current != nil {
		copy := *current
		copy.Locked = locked
		copy.AllowedCount = allowedCount
		copy.DirectMCP = directMCP
		copy.HostedRelay = hostedRelay
		copy.Revision++
		updated = &copy
		r.wings[id] = updated
	}
	r.mu.Unlock()
	return updated
}

func (r *WingRegistry) FindByID(wingID string) *ConnectedWing {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var newest *ConnectedWing
	for _, wing := range r.wings {
		if wing.WingID == wingID && (newest == nil || wing.ConnectedAt.After(newest.ConnectedAt) ||
			(wing.ConnectedAt.Equal(newest.ConnectedAt) && wing.ID > newest.ID)) {
			newest = wing
		}
	}
	return newest
}

func (r *WingRegistry) Touch(connectionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if w, ok := r.wings[connectionID]; ok {
		copy := *w
		copy.LastSeen = time.Now()
		r.wings[connectionID] = &copy
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
		if err := w.Conn.Write(writeCtx, websocket.MessageText, data); err != nil {
			log.Printf("broadcast to wing %s: %v", w.WingID, err)
		}
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
		if err := w.Conn.Close(websocket.StatusGoingAway, "server shutting down"); err != nil {
			log.Printf("close wing %s during shutdown: %v", w.WingID, err)
		}
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
	var credentialWingID string
	if s.JWTPubKey() != nil {
		claims, jwtErr := ValidateWingJWT(s.JWTPubKey(), token)
		if jwtErr == nil {
			userID = claims.Subject
			wingPublicKey = claims.PublicKey
			credentialWingID = claims.WingID
		}
	}
	if userID == "" && s.Store != nil {
		var err error
		userID, credentialWingID, err = s.Store.ValidateToken(token)
		if err != nil {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
	}
	if userID == "" {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	if !s.roostUserIDAllowed(userID) {
		http.Error(w, "this account is not enrolled in this roost", http.StatusForbidden)
		return
	}

	// Wings are native clients and send no Origin header. Keep the library's
	// default Origin enforcement so an arbitrary web page cannot turn a leaked
	// or browser-visible device credential into a cross-site wing connection.
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		log.Printf("websocket accept: %v", err)
		return
	}
	conn.SetReadLimit(512 * 1024) // 512KB — replay chunks can be large
	defer func() { _ = conn.CloseNow() }()

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
	if !s.wingRegistrationAllowed(userID, credentialWingID, reg.WingID) {
		errMsg := ws.ErrorMsg{Type: ws.TypeError, Message: "wing ID does not match credential"}
		if err := writeWebSocketJSON(ctx, conn, errMsg); err != nil {
			log.Printf("report rejected wing registration: %v", err)
		}
		return
	}

	// Prefer JWT-derived public key; fall back to registration message (local/roost mode)
	if wingPublicKey == "" {
		wingPublicKey = reg.PublicKey
	}

	wing := &ConnectedWing{
		ID:             uuid.New().String(),
		UserID:         userID,
		WingID:         reg.WingID,
		PublicKey:      wingPublicKey,
		OrgID:          reg.OrgSlug,
		Locked:         reg.Locked,
		AllowedCount:   reg.AllowedCount,
		PurposeBinding: reg.PurposeBinding,
		DirectMCP:      reg.DirectMCP,
		HostedRelay:    reg.HostedRelay,
		Conn:           conn,
		ConnectedAt:    time.Now(),
		LastSeen:       time.Now(),
	}

	// Validate org membership if org specified (accepts slug or ID)
	if wing.OrgID != "" {
		if s.Store != nil {
			// Login node: resolve org reference (tries ID then slug)
			org, orgErr := s.Store.ResolveOrg(wing.OrgID, userID)
			if orgErr != nil {
				errMsg := ws.ErrorMsg{Type: ws.TypeError, Message: orgErr.Error()}
				if err := writeWebSocketJSON(ctx, conn, errMsg); err != nil {
					log.Printf("report org resolution failure: %v", err)
				}
				return
			}
			if org == nil && s.RoostMode {
				// Self-hosted: find an existing shared org without requiring prior
				// membership, or create it atomically on first connection.
				org, orgErr = s.Store.GetOrgByID(wing.OrgID)
				if orgErr == nil && org == nil {
					org, orgErr = s.Store.GetOrgBySlug(wing.OrgID)
				}
				if orgErr == nil && org == nil {
					createErr := s.Store.CreateOrgWithSeats(wing.OrgID, wing.OrgID, wing.OrgID, userID, 9999)
					org, orgErr = s.Store.GetOrgByID(wing.OrgID)
					if orgErr == nil && org == nil {
						orgErr = createErr
					}
				}
				if orgErr != nil {
					log.Printf("prepare roost org %s: %v", wing.OrgID, orgErr)
				}
			}
			if org == nil {
				errMsg := ws.ErrorMsg{Type: ws.TypeError, Message: "org not found: " + wing.OrgID}
				if err := writeWebSocketJSON(ctx, conn, errMsg); err != nil {
					log.Printf("report missing wing org: %v", err)
				}
				return
			}
			// Store the resolved org ID (not the slug)
			wing.OrgID = org.ID
			role := s.Store.GetOrgMemberRole(org.ID, userID)
			if role == "" && s.RoostMode {
				// Self-hosted: all authenticated users are org members
				if err := s.Store.AddOrgMember(org.ID, userID, "member"); err != nil {
					log.Printf("add user %s to roost org %s: %v", userID, org.ID, err)
				} else {
					role = s.Store.GetOrgMemberRole(org.ID, userID)
				}
			}
			if role == "" {
				errMsg := ws.ErrorMsg{Type: ws.TypeError, Message: "not a member of org: " + org.Name}
				if err := writeWebSocketJSON(ctx, conn, errMsg); err != nil {
					log.Printf("report rejected org membership: %v", err)
				}
				return
			}
		} else if s.Config.LoginNodeAddr != "" {
			// Edge node: proxy org check to login
			resolvedID, ok := s.validateOrgViaLogin(ctx, wing.OrgID, userID)
			if !ok {
				errMsg := ws.ErrorMsg{Type: ws.TypeError, Message: "org validation failed for: " + wing.OrgID}
				if err := writeWebSocketJSON(ctx, conn, errMsg); err != nil {
					log.Printf("report edge org validation failure: %v", err)
				}
				return
			}
			wing.OrgID = resolvedID
		}
	}

	var superseded []*ConnectedWing
	var active bool
	wing, superseded, active = s.Wings.Activate(wing)
	for _, stale := range superseded {
		if stale.Conn != nil {
			if err := stale.Conn.CloseNow(); err != nil {
				log.Printf("close superseded wing %s: %v", stale.WingID, err)
			}
		}
	}
	if !active {
		if err := writeWebSocketJSON(ctx, conn, ws.ErrorMsg{Type: ws.TypeError, Message: "wing ID is already active for another authenticated owner or newer connection"}); err != nil {
			log.Printf("report duplicate wing registration: %v", err)
		}
		return
	}
	s.dispatchWingEvent("wing.online", wing)
	defer func() {
		if w, logicalOffline := s.Wings.RemoveConnection(wing.ID); w != nil && logicalOffline {
			s.dispatchWingEvent("wing.offline", w)
		}
	}()

	log.Printf("wing %s connected (user=%s wing=%s machine=%s role=%s total_wings=%d)", wing.ID, userID, reg.WingID, s.Config.FlyMachineID, s.Config.NodeRole, len(s.Wings.All()))

	// Send ack (include relay public key for JWT verification in direct mode)
	ack := ws.RegisteredMsg{Type: ws.TypeRegistered, WingID: wing.ID}
	ack.PasskeyRPID, ack.PasskeyOrigins = s.passkeyRelyingParty()
	if s.JWTPubKey() != nil {
		if pubStr, err := MarshalECPublicKey(s.JWTPubKey()); err == nil {
			ack.RelayPubKey = pubStr
		}
	}
	if err := writeWebSocketJSON(ctx, conn, ack); err != nil {
		log.Printf("acknowledge wing %s registration: %v", wing.WingID, err)
		return
	}

	// Read loop — forward messages, never inspect content
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			log.Printf("wing %s disconnected (wing=%s machine=%s): %v", wing.ID, wing.WingID, s.Config.FlyMachineID, err)
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
			if err := json.Unmarshal(data, &cfg); err != nil {
				log.Printf("decode config from wing %s: %v", wing.WingID, err)
				continue
			}
			if updated := s.Wings.UpdateConfig(wing.ID, cfg.Locked, cfg.AllowedCount, cfg.DirectMCP, cfg.HostedRelay); updated != nil {
				wing = updated
				s.dispatchWingEvent("wing.config", updated)
			}

		case ws.TypePTYStarted, ws.TypePTYOutput, ws.TypePTYExited, ws.TypePasskeyChallenge, ws.TypePTYPreview, ws.TypePTYBrowserOpen, ws.TypePTYMigrated, ws.TypePTYFallback:
			if !ws.HostedRelayAllowed(wing.HostedRelay) {
				log.Printf("[audit] hosted relay output dropped wing=%s operation=%s policy=deny", wing.WingID, msg.Type)
				continue
			}
			// Extract session_id and forward to browser
			var partial struct {
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(data, &partial); err != nil {
				log.Printf("decode PTY message from wing %s: %v", wing.WingID, err)
				continue
			}
			s.forwardPTYToBrowser(partial.SessionID, wing.WingID, data)

		case ws.TypeError:
			var message ws.ErrorMsg
			if err := json.Unmarshal(data, &message); err != nil {
				log.Printf("decode error message from wing %s: %v", wing.WingID, err)
				continue
			}
			if message.RequestID != "" {
				s.forwardTunnelToBrowser(wing.WingID, message.RequestID, data, true)
				continue
			}
			if !ws.HostedRelayAllowed(wing.HostedRelay) {
				log.Printf("[audit] hosted relay output dropped wing=%s operation=%s policy=deny", wing.WingID, msg.Type)
				continue
			}
			s.forwardPTYToBrowser(message.SessionID, wing.WingID, data)

		case ws.TypeTunnelResponse:
			var resp ws.TunnelResponse
			if err := json.Unmarshal(data, &resp); err != nil {
				log.Printf("decode tunnel response from wing %s: %v", wing.WingID, err)
				continue
			}
			s.forwardTunnelToBrowser(wing.WingID, resp.RequestID, data, true)

		case ws.TypeTunnelStream:
			var stream ws.TunnelStream
			if err := json.Unmarshal(data, &stream); err != nil {
				log.Printf("decode tunnel stream from wing %s: %v", wing.WingID, err)
				continue
			}
			s.forwardTunnelToBrowser(wing.WingID, stream.RequestID, data, stream.Done)

		case ws.TypeSessionAttention:
			if !ws.HostedRelayAllowed(wing.HostedRelay) {
				continue
			}
			var attn ws.SessionAttention
			if err := json.Unmarshal(data, &attn); err != nil {
				log.Printf("decode attention event from wing %s: %v", wing.WingID, err)
				continue
			}
			ev := WingEvent{
				Type:      "session.attention",
				WingID:    wing.WingID,
				SessionID: attn.SessionID,
			}
			if s.IsEdge() && s.Config.LoginNodeAddr != "" {
				payload, err := json.Marshal(map[string]any{
					"type":          "session.attention",
					"wing_id":       wing.WingID,
					"connection_id": wing.ID,
					"machine_id":    s.Config.FlyMachineID,
					"user_id":       wing.UserID,
					"org_id":        wing.OrgID,
					"session_id":    attn.SessionID,
				})
				if err != nil {
					log.Printf("encode attention event for login: %v", err)
				} else {
					go s.forwardPayloadToLogin(payload)
				}
			} else {
				s.Wings.notifyWing(wing.UserID, wing.OrgID, ev)
				if s.IsLogin() && s.WingMap != nil {
					payload, err := json.Marshal(map[string]any{
						"type":          "session.attention",
						"wing_id":       wing.WingID,
						"connection_id": wing.ID,
						"machine_id":    s.Config.FlyMachineID,
						"user_id":       wing.UserID,
						"org_id":        wing.OrgID,
						"session_id":    attn.SessionID,
					})
					if err != nil {
						log.Printf("encode attention event for edges: %v", err)
					} else {
						go s.broadcastToEdges(payload)
					}
				}
			}
			// Push notification via ntfy (nonce-deduped)
			if attn.Nonce != "" {
				clickURL := ntfyClickURL(attn.SessionID)
				s.trySendNtfy(attn.Nonce, wing.UserID, func(c *ntfy.Client) {
					c.SendAttention(attn.SessionID, attn.Agent, attn.CWD, clickURL)
				})
			}

		}
	}
}

func (s *Server) wingRegistrationAllowed(userID, credentialWingID, registrationWingID string) bool {
	if registrationWingID == "" || len(registrationWingID) > 200 {
		return false
	}
	if credentialWingID == registrationWingID {
		return true
	}
	// Embedded local and shared-roost wings use service tokens whose historical
	// device IDs are "local" or "roost-wing", while their runtime WingID comes
	// from the host configuration. Dev mode has the same intentionally local
	// trust boundary and preserves its existing test/development token flow.
	return s.LocalMode || s.DevMode || (s.RoostMode && userID == roostWingServiceUserID)
}

// validateOrgViaLogin proxies org membership validation to the login node.
// Returns (resolvedOrgID, ok). The resolved ID is always a UUID.
func (s *Server) validateOrgViaLogin(ctx context.Context, orgRef, userID string) (string, bool) {
	client := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequestWithContext(ctx, "GET",
		strings.TrimRight(s.Config.LoginNodeAddr, "/")+"/internal/org-check/"+url.PathEscape(orgRef)+"/"+url.PathEscape(userID), nil)
	if err != nil {
		return "", false
	}
	s.authorizeInternalRequest(req)
	resp, err := client.Do(req)
	if err != nil {
		return "", false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	var result struct {
		OK    bool   `json:"ok"`
		OrgID string `json:"org_id"`
	}
	if err := decodeInternalJSONResponse(resp.Body, &result); err != nil {
		return "", false
	}
	return result.OrgID, result.OK
}

// dispatchWingEvent routes a wing lifecycle event through the correct path.
// Edge: register/deregister with login wingMap, forward event to login.
// Login/single-node: update wingMap, deliver locally, broadcast to edges.
func (s *Server) dispatchWingEvent(eventType string, wing *ConnectedWing) {
	// Edge: register with login (synchronous so WingMap is updated
	// before browsers are notified), then forward event.
	// wing.offline: NO deregister. The 5s sync handles cleanup.
	if s.IsEdge() && s.Config.LoginNodeAddr != "" {
		if eventType == "wing.online" || eventType == "wing.config" {
			s.registerWingWithLogin(wing)
		}
		go s.forwardWingEvent(eventType, wing)
		return
	}

	// Notify PTY browsers when a wing goes offline
	if eventType == "wing.offline" {
		s.PTY.NotifyWingOffline(wing.WingID)
	}

	// Login or single-node: update wingMap
	if s.WingMap != nil {
		switch eventType {
		case "wing.online", "wing.config":
			s.WingMap.Register(wing.WingID, WingLocation{
				MachineID:      s.Config.FlyMachineID,
				ConnectionID:   wing.ID,
				UserID:         wing.UserID,
				OrgID:          wing.OrgID,
				PublicKey:      wing.PublicKey,
				Locked:         wing.Locked,
				AllowedCount:   wing.AllowedCount,
				PurposeBinding: wing.PurposeBinding,
				DirectMCP:      wing.DirectMCP,
				HostedRelay:    wing.HostedRelay,
				GenerationAt:   wing.ConnectedAt,
				Revision:       wing.Revision,
			})
		case "wing.offline":
			if s.findAnyWingByWingID(wing.WingID) == nil {
				s.WingMap.Deregister(wing.WingID)
			}
		}
	}

	// Resolve owner display name for dashboard
	ownerName := ""
	if s.Store != nil {
		if u, err := s.Store.GetUserByID(wing.UserID); err == nil && u != nil {
			ownerName = u.DisplayName
			if ownerName == "" && u.Email != nil {
				ownerName = *u.Email
			}
		}
	}

	// Deliver locally
	var ev WingEvent
	if eventType == "wing.offline" {
		ev = WingEvent{Type: eventType, WingID: wing.WingID}
	} else {
		locked := wing.Locked
		allowedCount := wing.AllowedCount
		purposeBinding := wing.PurposeBinding
		directMCP := wing.DirectMCP
		hostedRelay := wing.HostedRelay
		ev = WingEvent{
			Type:           eventType,
			WingID:         wing.WingID,
			PublicKey:      wing.PublicKey,
			Locked:         &locked,
			AllowedCount:   &allowedCount,
			PurposeBinding: &purposeBinding,
			DirectMCP:      &directMCP,
			HostedRelay:    &hostedRelay,
			UserID:         wing.UserID,
			Owner:          ownerName,
		}
	}
	s.Wings.notifyWing(wing.UserID, wing.OrgID, ev)

	// Login: broadcast to all edges
	if s.IsLogin() && s.WingMap != nil {
		payload, err := json.Marshal(map[string]any{
			"type":                   eventType,
			"wing_id":                wing.WingID,
			"connection_id":          wing.ID,
			"machine_id":             s.Config.FlyMachineID,
			"user_id":                wing.UserID,
			"org_id":                 wing.OrgID,
			"public_key":             wing.PublicKey,
			"locked":                 wing.Locked,
			"allowed_count":          wing.AllowedCount,
			"purpose_binding":        wing.PurposeBinding,
			"direct_mcp":             wing.DirectMCP,
			"hosted_relay":           wing.HostedRelay,
			"connected_at_unix_nano": unixNanoOrZero(wing.ConnectedAt),
			"revision":               wing.Revision,
		})
		if err != nil {
			log.Printf("encode wing event for edges: %v", err)
		} else {
			go s.broadcastToEdges(payload)
		}
	}
}

// forwardWingEvent POSTs a wing event to the login node for cluster-wide propagation.
func (s *Server) forwardWingEvent(eventType string, wing *ConnectedWing) {
	payload, err := json.Marshal(map[string]any{
		"type":                   eventType,
		"wing_id":                wing.WingID,
		"connection_id":          wing.ID,
		"machine_id":             s.Config.FlyMachineID,
		"user_id":                wing.UserID,
		"org_id":                 wing.OrgID,
		"public_key":             wing.PublicKey,
		"locked":                 wing.Locked,
		"allowed_count":          wing.AllowedCount,
		"purpose_binding":        wing.PurposeBinding,
		"direct_mcp":             wing.DirectMCP,
		"hosted_relay":           wing.HostedRelay,
		"connected_at_unix_nano": unixNanoOrZero(wing.ConnectedAt),
		"revision":               wing.Revision,
	})
	if err != nil {
		log.Printf("encode wing event for login: %v", err)
		return
	}
	s.forwardPayloadToLogin(payload)
}

// forwardPayloadToLogin POSTs a raw JSON payload to the login node's wing-event endpoint.
func (s *Server) forwardPayloadToLogin(payload []byte) {
	client := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequest("POST", s.Config.LoginNodeAddr+"/internal/wing-event", bytes.NewReader(payload))
	if err != nil {
		log.Printf("build wing event request: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	s.authorizeInternalRequest(req)
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("forward wing event to login: %v", err)
		return
	}
	if err := resp.Body.Close(); err != nil {
		log.Printf("close wing event response: %v", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("forward wing event to login: unexpected status %s", resp.Status)
	}
}

// forwardTunnelToBrowser routes an encrypted tunnel response from its source wing to the
// originating browser. The source binding prevents one connected wing from consuming or
// injecting another wing's pending request by reusing its request ID.
func (s *Server) forwardTunnelToBrowser(wingID, requestID string, data []byte, done bool) {
	pending, overBudget := s.takePendingTunnelResponse(wingID, requestID, len(data), done)
	if pending == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if overBudget {
		message := ws.ErrorMsg{
			Type: ws.TypeError, RequestID: requestID,
			Message: "coordination response exceeded the hosted signaling limit",
		}
		if err := writeWebSocketJSON(ctx, pending.Browser, message); err != nil {
			log.Printf("report oversized coordination response: %v", err)
		}
		return
	}
	if pending.Coordination {
		if !s.writeCoordinationPayload(ctx, pending.Browser, data) {
			// A closed browser or exhausted sustained-rate deadline cannot consume the rest
			// of this response. Remove the request immediately so a modified
			// wing cannot keep making the relay rate-limit or notify dead clients
			// until the normal request TTL expires.
			s.discardTunnelRequest(wingID, requestID)
		}
		return
	}
	wing := s.findAnyWingByWingID(wingID)
	if wing == nil || !ws.HostedRelayAllowed(wing.HostedRelay) {
		s.discardTunnelRequest(wingID, requestID)
		s.denyHostedRelay(ctx, pending.Browser, s.browserUserID(pending.Browser), wingID, ws.TypeTunnelResponse, requestID, "")
		return
	}
	s.writeRelayPayload(ctx, pending.Browser, data, "", requestID)
}

func (s *Server) discardTunnelRequest(wingID, requestID string) {
	s.tunnelMu.Lock()
	delete(s.tunnelRequests, tunnelRequestKey{WingID: wingID, RequestID: requestID})
	s.tunnelMu.Unlock()
}

func (s *Server) pendingTunnelBrowser(wingID, requestID string, done bool) *pendingTunnelRequest {
	pending, _ := s.takePendingTunnelResponse(wingID, requestID, 0, done)
	return pending
}

func (s *Server) takePendingTunnelResponse(wingID, requestID string, responseBytes int, done bool) (*pendingTunnelRequest, bool) {
	s.tunnelMu.Lock()
	key := tunnelRequestKey{WingID: wingID, RequestID: requestID}
	pending, ok := s.tunnelRequests[key]
	if ok && time.Since(pending.CreatedAt) > pendingTunnelRequestTTL {
		delete(s.tunnelRequests, key)
		ok = false
	} else if ok {
		if pending.Coordination && responseBytes > 0 {
			pending.ResponseBytes += responseBytes
			pending.ResponseMessages++
			if pending.ResponseBytes > maxCoordinationResponseBytes || pending.ResponseMessages > maxCoordinationResponseMessages {
				delete(s.tunnelRequests, key)
				s.tunnelMu.Unlock()
				return &pending, true
			}
			s.tunnelRequests[key] = pending
		}
		if done {
			delete(s.tunnelRequests, key)
		}
	}
	s.tunnelMu.Unlock()
	if !ok {
		return nil, false
	}
	return &pending, false
}

const maxNtfyDedupNonces = 10000

// markNtfyNonce reports whether this is a new per-user notification episode.
// The insertion-order cache is deliberately bounded; nonces are only a
// best-effort duplicate suppression mechanism, not authorization state.
func (s *Server) markNtfyNonce(userID, nonce string) bool {
	if nonce == "" {
		return true
	}
	key := userID + "\x00" + nonce
	s.ntfyNonceMu.Lock()
	defer s.ntfyNonceMu.Unlock()
	if s.ntfyNonceSeen == nil {
		s.ntfyNonceSeen = make(map[string]bool)
	}
	if s.ntfyNonceSeen[key] {
		return false
	}
	if len(s.ntfyNonceOrder) >= maxNtfyDedupNonces {
		oldest := s.ntfyNonceOrder[s.ntfyNonceNext]
		delete(s.ntfyNonceSeen, oldest)
		s.ntfyNonceOrder[s.ntfyNonceNext] = key
		s.ntfyNonceNext = (s.ntfyNonceNext + 1) % maxNtfyDedupNonces
	} else {
		s.ntfyNonceOrder = append(s.ntfyNonceOrder, key)
	}
	s.ntfyNonceSeen[key] = true
	return true
}

// trySendNtfy deduplicates by nonce and sends an ntfy push notification.
// Same nonce = same attention episode → skip. Empty nonce always sends.
// Fire-and-forget: errors are logged, never returned.
func (s *Server) trySendNtfy(nonce, userID string, send func(c *ntfy.Client)) {
	if s.Store == nil {
		return
	}
	// Nonce dedup: never refire the same nonce
	if !s.markNtfyNonce(userID, nonce) {
		log.Printf("ntfy: skipping duplicate nonce=%s", nonce)
		return
	}
	cfg, err := s.Store.GetNtfyConfig(userID)
	if err != nil || cfg.Topic == "" {
		return
	}
	log.Printf("ntfy: sending push for user=%s nonce=%s", userID, nonce)
	c, err := s.newNtfyClient(cfg.Topic, cfg.Token, cfg.Events)
	if err != nil {
		log.Printf("ntfy: refusing unsafe endpoint for user=%s: %v", userID, err)
		return
	}
	go send(c)
}

// ntfyClickURL builds the click URL for a session notification.
func ntfyClickURL(sessionID string) string {
	return fmt.Sprintf("https://app.wingthing.ai/#s/%s", sessionID)
}
