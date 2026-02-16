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

	"github.com/ehrlich-b/wingthing/internal/ntfy"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

// PTYRoute is a minimal routing entry for wing→browser output forwarding.
// No session metadata — the wing owns all session intelligence.
type PTYRoute struct {
	BrowserConn *websocket.Conn
	UserID      string // bandwidth metering only
	WingID      string // machine ID for offline notification
	Agent       string // agent name for ntfy notifications
	CWD         string // working directory for ntfy notifications
	mu          sync.Mutex
}

// PTYRoutes tracks active PTY routing entries.
type PTYRoutes struct {
	mu     sync.RWMutex
	routes map[string]*PTYRoute // session_id → route
}

func NewPTYRoutes() *PTYRoutes {
	return &PTYRoutes{
		routes: make(map[string]*PTYRoute),
	}
}

func (r *PTYRoutes) Set(sessionID string, route *PTYRoute) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.routes[sessionID] = route
}

func (r *PTYRoutes) Get(sessionID string) *PTYRoute {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.routes[sessionID]
}

func (r *PTYRoutes) Remove(sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.routes, sessionID)
}

// ClearBrowser nils the BrowserConn on all routes owned by this connection.
func (r *PTYRoutes) ClearBrowser(conn *websocket.Conn) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, route := range r.routes {
		route.mu.Lock()
		if route.BrowserConn == conn {
			route.BrowserConn = nil
		}
		route.mu.Unlock()
	}
}

// NotifyWingOffline sends a wing.offline message to all PTY browsers connected to the given wing.
func (r *PTYRoutes) NotifyWingOffline(wingID string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	msg := []byte(`{"type":"wing.offline"}`)
	for _, route := range r.routes {
		route.mu.Lock()
		bc := route.BrowserConn
		wid := route.WingID
		route.mu.Unlock()
		if wid == wingID && bc != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			bc.Write(ctx, websocket.MessageText, msg)
			cancel()
		}
	}
}

// handlePTYWS handles the browser WebSocket for a PTY session.
func (s *Server) handlePTYWS(w http.ResponseWriter, r *http.Request) {
	// Auth
	var userID string
	var userEmail string
	var userOrgIDs []string
	if u := s.sessionUser(r); u != nil {
		userID = u.ID
		userOrgIDs = u.OrgIDs
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

	// Cross-node routing: if target wing is on another machine, fly-replay BEFORE WebSocket upgrade.
	// Retries for up to 5s to handle wing reconnection after deploy.
	targetWingID := r.URL.Query().Get("wing_id")
	if s.Config.FlyMachineID != "" {
		if targetWingID != "" && s.findAnyWingByWingID(targetWingID) == nil {
			log.Printf("[pty-route] wing %s not local on %s (%s), searching cluster...", targetWingID, s.Config.FlyMachineID, s.Config.NodeRole)
			var machineID string
			var found bool
			for range 10 {
				if s.findAnyWingByWingID(targetWingID) != nil {
					found = true
					machineID = s.Config.FlyMachineID
					break
				}
				machineID, found = s.locateWing(targetWingID)
				if found {
					break
				}
				select {
				case <-r.Context().Done():
					return
				case <-time.After(500 * time.Millisecond):
				}
			}
			if !found {
				log.Printf("[pty-route] FAIL wing %s not found anywhere after 5s retries (machine=%s role=%s local_wings=%s)",
					targetWingID, s.Config.FlyMachineID, s.Config.NodeRole, s.wingRegistrySummary())
				http.Error(w, `{"error":"wing not found","retry":true}`, http.StatusNotFound)
				return
			}
			if machineID != s.Config.FlyMachineID {
				log.Printf("[pty-route] fly-replay wing %s → machine %s (from %s)", targetWingID, machineID, s.Config.FlyMachineID)
				w.Header().Set("fly-replay", "instance="+machineID)
				return
			}
			log.Printf("[pty-route] wing %s resolved to THIS machine %s", targetWingID, s.Config.FlyMachineID)
		} else if targetWingID != "" {
			log.Printf("[pty-route] wing %s found locally on %s (%s), upgrading", targetWingID, s.Config.FlyMachineID, s.Config.NodeRole)
		}
	} else if targetWingID != "" {
		log.Printf("[pty-route] no FlyMachineID set, skipping cross-node routing for wing %s", targetWingID)
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

	// On browser disconnect: clear BrowserConn on all owned routes
	defer s.PTY.ClearBrowser(conn)

	// Wing ID from URL query param — used for all routing in this connection
	queryWingID := r.URL.Query().Get("wing_id")

	// lookupWing resolves the wing for each message (handles wing reconnect)
	lookupWing := func() *ConnectedWing {
		if queryWingID == "" {
			return nil
		}
		w := s.Wings.FindByID(queryWingID)
		if w == nil {
			w = s.findAnyWingByWingID(queryWingID)
		}
		return w
	}

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
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

			// Use wing_id from message if provided, fall back to query param
			wingID := start.WingID
			if wingID == "" {
				wingID = queryWingID
			}

			var wing *ConnectedWing
			if wingID != "" {
				wing = s.Wings.FindByID(wingID)
				if wing == nil {
					wing = s.findAnyWingByWingID(wingID)
				}
				if wing != nil && !s.canAccessWing(userID, wing, userOrgIDs) {
					wing = nil
				}
			} else {
				wing = s.findAccessibleWing(userID)
			}
			if wing == nil {
				log.Printf("[pty-start] NO WING FOUND: requested=%s query=%s user=%s userOrgs=%v machine=%s role=%s local_wings=%s",
					wingID, queryWingID, userID, userOrgIDs, s.Config.FlyMachineID, s.Config.NodeRole, s.wingRegistrySummary())
				errMsg, _ := json.Marshal(ws.ErrorMsg{Type: ws.TypeError, Message: "no wing connected"})
				conn.Write(ctx, websocket.MessageText, errMsg)
				continue
			}

			sessionID := uuid.New().String()[:8]
			start.SessionID = sessionID
			start.UserID = userID

			s.PTY.Set(sessionID, &PTYRoute{BrowserConn: conn, UserID: userID, WingID: wing.WingID, Agent: start.Agent, CWD: start.CWD})

			fwd, _ := json.Marshal(start)
			wing.Conn.Write(ctx, websocket.MessageText, fwd)

			log.Printf("pty session %s started (user=%s wing=%s agent=%s)", sessionID, userID, wing.WingID, start.Agent)

		case ws.TypePTYAttach:
			var attach ws.PTYAttach
			if err := json.Unmarshal(data, &attach); err != nil {
				continue
			}

			// Use wing_id from message if provided, fall back to query param
			wingID := attach.WingID
			if wingID == "" {
				wingID = queryWingID
			}

			wing := s.Wings.FindByID(wingID)
			if wing == nil {
				wing = s.findAnyWingByWingID(wingID)
			}
			if wing == nil || !s.canAccessWing(userID, wing, userOrgIDs) {
				errMsg, _ := json.Marshal(ws.ErrorMsg{Type: ws.TypeError, Message: "wing not found"})
				conn.Write(ctx, websocket.MessageText, errMsg)
				continue
			}

			s.PTY.Set(attach.SessionID, &PTYRoute{BrowserConn: conn, UserID: userID, WingID: wing.WingID})

			fwd, _ := json.Marshal(attach)
			wing.Conn.Write(ctx, websocket.MessageText, fwd)

			log.Printf("pty session %s reattached (user=%s)", attach.SessionID, userID)

		case ws.TypePTYInput, ws.TypePTYResize, ws.TypePTYAttentionAck, ws.TypePasskeyResponse:
			wing := lookupWing()
			if wing == nil {
				continue
			}
			wing.Conn.Write(ctx, websocket.MessageText, data)

		case ws.TypePTYDetach:
			var det ws.PTYDetach
			if err := json.Unmarshal(data, &det); err != nil {
				continue
			}
			route := s.PTY.Get(det.SessionID)
			if route == nil {
				continue
			}
			route.mu.Lock()
			if route.BrowserConn == conn {
				route.BrowserConn = nil
			}
			route.mu.Unlock()

		case ws.TypePTYKill:
			var kill ws.PTYKill
			if err := json.Unmarshal(data, &kill); err != nil {
				continue
			}
			wing := lookupWing()
			if wing != nil {
				fwd, _ := json.Marshal(kill)
				wing.Conn.Write(ctx, websocket.MessageText, fwd)
			}

		case ws.TypeTunnelRequest:
			var req ws.TunnelRequest
			if err := json.Unmarshal(data, &req); err != nil {
				continue
			}
			wing := s.findAnyWingByWingID(req.WingID)
			if wing == nil || !s.canAccessWing(userID, wing, userOrgIDs) {
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
	route := s.PTY.Get(sessionID)
	if route == nil {
		return
	}

	// Handle pty.exited: clean up route + send ntfy exit notification
	var env ws.Envelope
	if err := json.Unmarshal(data, &env); err == nil && env.Type == ws.TypePTYExited {
		route.mu.Lock()
		bc := route.BrowserConn
		userID := route.UserID
		agent := route.Agent
		cwd := route.CWD
		route.mu.Unlock()

		if bc != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			bc.Write(ctx, websocket.MessageText, data)
		}

		// Send ntfy exit notification (sessionID as nonce — one exit per session)
		var exited ws.PTYExited
		if err := json.Unmarshal(data, &exited); err == nil {
			clickURL := ntfyClickURL(sessionID)
			s.trySendNtfy("exit:"+sessionID, userID, func(c *ntfy.Client) {
				c.SendExit(sessionID, agent, cwd, exited.ExitCode, clickURL)
			})
		}

		s.PTY.Remove(sessionID)
		return
	}

	route.mu.Lock()
	bc := route.BrowserConn
	userID := route.UserID
	route.mu.Unlock()

	if bc == nil {
		// Detached — drop the data (wing has ring buffer for replay)
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
			route.mu.Lock()
			route.BrowserConn = nil
			route.mu.Unlock()
			bc.Close(websocket.StatusNormalClosure, "bandwidth exceeded")
			return
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bc.Write(ctx, websocket.MessageText, data)
}
