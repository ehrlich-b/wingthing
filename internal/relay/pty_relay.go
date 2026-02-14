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
	ID             string
	WingID         string
	UserID         string
	Agent          string
	CWD            string // working directory for this session
	EggConfig      string // serialized YAML of egg config at spawn time
	NeedsAttention bool   // terminal bell detected — session wants user attention
	Status         string // "active", "detached", "exited"
	BrowserConn    *websocket.Conn
	mu             sync.Mutex
}

// PTYRegistry tracks active PTY sessions.
type PTYRegistry struct {
	mu         sync.RWMutex
	sessions   map[string]*PTYSession // session_id -> session
	tombstones map[string]time.Time   // recently deleted IDs (skip in sync)
}

func NewPTYRegistry() *PTYRegistry {
	return &PTYRegistry{
		sessions:   make(map[string]*PTYSession),
		tombstones: make(map[string]time.Time),
	}
}

func (r *PTYRegistry) Add(s *PTYSession) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, tomb := r.tombstones[s.ID]; tomb {
		return false
	}
	r.sessions[s.ID] = s
	return true
}

// IsTombstoned returns true if the session was recently deleted.
func (r *PTYRegistry) IsTombstoned(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.tombstones[id]
	return ok
}

func (r *PTYRegistry) Remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, id)
	r.tombstones[id] = time.Now()
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

// All returns all PTY sessions.
func (r *PTYRegistry) All() []*PTYSession {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*PTYSession, 0, len(r.sessions))
	for _, s := range r.sessions {
		result = append(result, s)
	}
	return result
}

// SyncFromWing reconciles the registry with the wing's authoritative session list.
// When removeStale is true (on-demand requests), removes sessions the wing no longer reports.
// When false (heartbeat), only adds missing sessions.
func (r *PTYRegistry) SyncFromWing(wingID, userID string, sessions []ws.SessionInfo, removeStale bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Expire old tombstones (>5min)
	now := time.Now()
	for id, t := range r.tombstones {
		if now.Sub(t) > 5*time.Minute {
			delete(r.tombstones, id)
		}
	}

	// Build set of session IDs the wing reports
	live := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		live[s.SessionID] = true
		// Skip tombstoned sessions (recently deleted by user)
		if _, tomb := r.tombstones[s.SessionID]; tomb {
			continue
		}
		if existing, exists := r.sessions[s.SessionID]; exists {
			existing.NeedsAttention = s.NeedsAttention
		} else {
			sessUserID := s.UserID
			if sessUserID == "" {
				sessUserID = userID // fallback: wing owner
			}
			r.sessions[s.SessionID] = &PTYSession{
				ID:             s.SessionID,
				WingID:         wingID,
				UserID:         sessUserID,
				Agent:          s.Agent,
				CWD:            s.CWD,
				EggConfig:      s.EggConfig,
				NeedsAttention: s.NeedsAttention,
				Status:         "detached",
			}
		}
	}

	// Only remove stale sessions on explicit requests, not heartbeats
	if removeStale {
		for id, s := range r.sessions {
			if s.WingID == wingID && !live[id] {
				delete(r.sessions, id)
			}
		}
	}
}

// handlePTYWS handles the browser WebSocket for a PTY session.
func (s *Server) handlePTYWS(w http.ResponseWriter, r *http.Request) {
	// Auth
	var userID string
	var userEmail string
	if u := s.sessionUser(r); u != nil {
		userID = u.ID
		if u.Email != nil {
			userEmail = *u.Email
		}
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
	}

	// Cross-node routing: if target wing is on a peer node, fly-replay BEFORE WebSocket upgrade.
	// Check wing_id query param (explicit target) or session_id (reattach to known session).
	if s.Peers != nil && s.Config.FlyMachineID != "" {
		targetWingID := r.URL.Query().Get("wing_id")
		if targetWingID == "" {
			// Check if this is a session reattach — look up the session's wing
			if sid := r.URL.Query().Get("session_id"); sid != "" {
				if sess := s.PTY.Get(sid); sess != nil {
					targetWingID = sess.WingID
				}
			}
		}
		if targetWingID != "" {
			// Wing not local? Check by connection ID first, then persistent wing_id.
			// No access check here — the wing handles authz via E2E tunnel.
			localWing := s.Wings.FindByID(targetWingID)
			if localWing == nil {
				localWing = s.findAnyWingByWingID(targetWingID)
			}
			if localWing == nil {
				// Check peers by connection ID, then persistent wing_id
				pw := s.Peers.FindWing(targetWingID)
				if pw == nil {
					pw = s.Peers.FindByWingID(targetWingID)
				}
				if pw != nil {
					// Anti-loop: only replay if target is a different machine
					if pw.MachineID != s.Config.FlyMachineID {
						w.Header().Set("fly-replay", "instance="+pw.MachineID)
						return
					}
				}
				// Wing not found anywhere — try WaitForWing with 5s timeout
				machineID, found := WaitForWing(r.Context(), s.Wings, s.Peers, targetWingID, 5*time.Second)
				if found && machineID != "" && machineID != s.Config.FlyMachineID {
					w.Header().Set("fly-replay", "instance="+machineID)
					return
				}
				if !found {
					http.Error(w, `{"error":"wing not found","retry":true}`, http.StatusNotFound)
					return
				}
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

	s.trackBrowser(conn)
	defer s.untrackBrowser(conn)

	ctx := r.Context()

	// Track which sessions this browser connection owns for detach on disconnect
	var ownedSessions []string

	defer func() {
		// On browser disconnect: mark owned sessions as detached, don't remove.
		// Only clear BrowserConn if it still points to THIS connection — a new
		// browser may have already reattached (race between old WS cleanup and
		// new WS attach).
		for _, sid := range ownedSessions {
			session := s.PTY.Get(sid)
			if session == nil {
				continue
			}
			session.mu.Lock()
			if session.BrowserConn == conn {
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
				if wing == nil {
					wing = s.findAnyWingByWingID(start.WingID)
				}
				if wing != nil && !s.canAccessWing(userID, wing) {
					wing = nil
				}
			} else {
				wing = s.findAccessibleWing(userID)
			}
			if wing == nil {
				errMsg, _ := json.Marshal(ws.ErrorMsg{Type: ws.TypeError, Message: "no wing connected"})
				conn.Write(ctx, websocket.MessageText, errMsg)
				continue
			}

			sessionID := uuid.New().String()[:8]
			start.SessionID = sessionID
			start.UserID = userID
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

			session, wing := s.getAuthorizedPTY(userID, attach.SessionID)
			if session == nil {
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
			_, wing := s.getAuthorizedPTY(userID, input.SessionID)
			if wing == nil {
				continue
			}
			wing.Conn.Write(ctx, websocket.MessageText, data)

		case ws.TypePTYResize:
			var resize ws.PTYResize
			if err := json.Unmarshal(data, &resize); err != nil {
				continue
			}
			_, wing := s.getAuthorizedPTY(userID, resize.SessionID)
			if wing == nil {
				continue
			}
			wing.Conn.Write(ctx, websocket.MessageText, data)

		case ws.TypePTYAttentionAck:
			var ack ws.PTYAttentionAck
			if err := json.Unmarshal(data, &ack); err != nil {
				continue
			}
			session, wing := s.getAuthorizedPTY(userID, ack.SessionID)
			if session == nil {
				continue
			}
			session.mu.Lock()
			session.NeedsAttention = false
			session.mu.Unlock()
			// Forward to wing so it clears wingAttention
			if wing != nil {
				wing.Conn.Write(ctx, websocket.MessageText, data)
			}

		case ws.TypePTYDetach:
			var det ws.PTYDetach
			if err := json.Unmarshal(data, &det); err != nil {
				continue
			}
			session, _ := s.getAuthorizedPTY(userID, det.SessionID)
			if session == nil {
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
			session, wing := s.getAuthorizedPTY(userID, kill.SessionID)
			if session == nil {
				continue
			}
			// Forward kill to wing so it terminates the PTY process
			if wing != nil {
				fwd, _ := json.Marshal(kill)
				wing.Conn.Write(ctx, websocket.MessageText, fwd)
			} else {
				// Wing gone — just clean up the session
				s.PTY.Remove(kill.SessionID)
			}
			log.Printf("pty session %s: kill requested (user=%s)", kill.SessionID, userID)

		case ws.TypePasskeyResponse:
			var resp ws.PasskeyResponse
			if err := json.Unmarshal(data, &resp); err != nil {
				continue
			}
			_, wing := s.getAuthorizedPTY(userID, resp.SessionID)
			if wing == nil {
				continue
			}
			wing.Conn.Write(ctx, websocket.MessageText, data)

		case ws.TypeTunnelRequest:
			var req ws.TunnelRequest
			if err := json.Unmarshal(data, &req); err != nil {
				continue
			}
			wing := s.findAnyWingByWingID(req.WingID)
			if wing == nil {
				errMsg, _ := json.Marshal(ws.ErrorMsg{Type: ws.TypeError, Message: "wing not found"})
				conn.Write(ctx, websocket.MessageText, errMsg)
				continue
			}
			// Inject user identity into tunnel request envelope
			req.SenderUserID = userID
			req.SenderEmail = userEmail
			if wing.UserID == userID {
				req.SenderOrgRole = "owner"
			} else if wing.OrgID != "" && s.Store != nil {
				req.SenderOrgRole = s.Store.GetOrgMemberRole(wing.OrgID, userID)
			}
			s.tunnelMu.Lock()
			s.tunnelRequests[req.RequestID] = conn
			s.tunnelMu.Unlock()
			fwdTunnel, _ := json.Marshal(req)
			wing.Conn.Write(ctx, websocket.MessageText, fwdTunnel)
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
	userID := session.UserID
	session.mu.Unlock()

	if bc == nil {
		// Session detached — drop the data (wing has ring buffer for replay)
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
			// Detach browser so subsequent forwards are dropped (send once)
			session.mu.Lock()
			session.BrowserConn = nil
			session.Status = "detached"
			session.mu.Unlock()
			bc.Close(websocket.StatusNormalClosure, "bandwidth exceeded")
			return
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bc.Write(ctx, websocket.MessageText, data)
}
