package relay

import (
	"context"
	"encoding/base64"
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
	BrowserConn       *websocket.Conn            // authorized controller (can send input)
	PendingController *websocket.Conn            // attach awaiting wing authorization
	PendingUserID     string                     // promoted with PendingController
	Viewers           map[string]*websocket.Conn // viewer_id → spectator conn (read-only)
	UserID            string                     // bandwidth metering only
	WingID            string                     // machine ID for offline notification
	Agent             string                     // agent name for ntfy notifications
	CWD               string                     // working directory for ntfy notifications
	Provisional       bool                       // route recreated by attach, not yet confirmed by wing
	CreatedAt         time.Time                  // set on provisional creation, for cap/expiry sweeps
	mu                sync.Mutex
}

// PTYRoutes tracks active PTY routing entries.
type PTYRoutes struct {
	mu     sync.RWMutex
	routes map[string]*PTYRoute // session_id → route
}

type tunnelRequestKey struct {
	WingID    string
	RequestID string
}

type pendingTunnelRequest struct {
	Browser   *websocket.Conn
	CreatedAt time.Time
}

const (
	maxPendingTunnelRequests = 4096
	pendingTunnelRequestTTL  = 5 * time.Minute

	// Provisional routes are created by attach for session IDs the wing has
	// not confirmed. An unknown ID gets no wing response, so without a cap and
	// expiry one authenticated browser could grow the route map without bound.
	maxProvisionalRoutes = 1024
	provisionalRouteTTL  = 2 * time.Minute

	// One route's viewer map is also attacker-growable by re-attaching to the
	// same (possibly nonexistent) session ID with fresh viewer IDs.
	maxViewersPerRoute = 64
)

// admitProvisionalLocked sweeps expired provisional routes and reports whether
// a new provisional route may be created. Caller holds r.mu.
func (r *PTYRoutes) admitProvisionalLocked() bool {
	now := time.Now()
	provisional := 0
	for id, route := range r.routes {
		if !route.Provisional {
			continue
		}
		if now.Sub(route.CreatedAt) > provisionalRouteTTL {
			delete(r.routes, id)
			continue
		}
		provisional++
	}
	return provisional < maxProvisionalRoutes
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

// AddViewer adds a spectator connection to a session route, creating the
// minimal route when the relay restarted after the egg was created.
func (r *PTYRoutes) AddViewer(sessionID, viewerID, wingID string, conn *websocket.Conn) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	route := r.routes[sessionID]
	if route == nil {
		if !r.admitProvisionalLocked() {
			return false
		}
		route = &PTYRoute{WingID: wingID, Provisional: true, CreatedAt: time.Now()}
		r.routes[sessionID] = route
	}
	route.mu.Lock()
	if route.WingID != "" && route.WingID != wingID {
		route.mu.Unlock()
		return false
	}
	if route.WingID == "" {
		route.WingID = wingID
	}
	if route.Viewers == nil {
		route.Viewers = make(map[string]*websocket.Conn)
	}
	if _, exists := route.Viewers[viewerID]; !exists && len(route.Viewers) >= maxViewersPerRoute {
		route.mu.Unlock()
		return false
	}
	route.Viewers[viewerID] = conn
	route.mu.Unlock()
	return true
}

func (r *PTYRoutes) SetPendingController(sessionID, wingID, userID string, conn *websocket.Conn) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	route := r.routes[sessionID]
	if route == nil {
		if !r.admitProvisionalLocked() {
			return false
		}
		route = &PTYRoute{WingID: wingID, Provisional: true, CreatedAt: time.Now()}
		r.routes[sessionID] = route
	}
	route.mu.Lock()
	if route.WingID != "" && route.WingID != wingID {
		route.mu.Unlock()
		return false
	}
	// The wing's authorization response carries only the session ID. Keep one
	// controller attach in flight so a second browser cannot replace the pending
	// connection and receive the first browser's successful authorization.
	if route.PendingController != nil {
		route.mu.Unlock()
		return false
	}
	if route.WingID == "" {
		route.WingID = wingID
	}
	route.PendingController = conn
	route.PendingUserID = userID
	route.mu.Unlock()
	return true
}

// RemoveViewer removes a spectator connection from a session route.
func (r *PTYRoutes) RemoveViewer(sessionID, viewerID string) {
	r.mu.RLock()
	route := r.routes[sessionID]
	r.mu.RUnlock()
	if route == nil {
		return
	}
	route.mu.Lock()
	delete(route.Viewers, viewerID)
	route.mu.Unlock()
}

// IsSpectator returns true if conn is a spectator on any session.
func (r *PTYRoutes) IsSpectator(conn *websocket.Conn) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, route := range r.routes {
		route.mu.Lock()
		for _, vc := range route.Viewers {
			if vc == conn {
				route.mu.Unlock()
				return true
			}
		}
		route.mu.Unlock()
	}
	return false
}

// CanAuthenticate reports whether conn owns this route's controller attach or
// the exact spectator viewer ID carried by a passkey response.
func (r *PTYRoutes) CanAuthenticate(sessionID, viewerID string, conn *websocket.Conn) bool {
	_, ok := r.AuthenticationWing(sessionID, viewerID, conn)
	return ok
}

// AuthenticationWing returns the route's wing only when conn owns the attach
// attempt identified by viewerID. Controller attempts use an empty viewer ID.
func (r *PTYRoutes) AuthenticationWing(sessionID, viewerID string, conn *websocket.Conn) (string, bool) {
	r.mu.RLock()
	route := r.routes[sessionID]
	r.mu.RUnlock()
	if route == nil {
		return "", false
	}
	route.mu.Lock()
	defer route.mu.Unlock()
	if viewerID == "" && (route.BrowserConn == conn || route.PendingController == conn) {
		return route.WingID, route.WingID != ""
	}
	if viewerID != "" && route.Viewers[viewerID] == conn {
		return route.WingID, route.WingID != ""
	}
	return "", false
}

// ControllerWing returns the route's wing only when conn is the controller the
// wing has already authorized. Pending reattach attempts and spectators cannot
// drive session input or lifecycle messages.
func (r *PTYRoutes) ControllerWing(sessionID string, conn *websocket.Conn) (string, bool) {
	r.mu.RLock()
	route := r.routes[sessionID]
	r.mu.RUnlock()
	if route == nil {
		return "", false
	}
	route.mu.Lock()
	defer route.mu.Unlock()
	if route.BrowserConn != conn || route.WingID == "" {
		return "", false
	}
	return route.WingID, true
}

// ClearBrowser nils the BrowserConn and removes spectator entries for this connection.
func (r *PTYRoutes) ClearBrowser(conn *websocket.Conn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for sessionID, route := range r.routes {
		route.mu.Lock()
		if route.BrowserConn == conn {
			route.BrowserConn = nil
		}
		if route.PendingController == conn {
			route.PendingController = nil
			route.PendingUserID = ""
		}
		for vid, vc := range route.Viewers {
			if vc == conn {
				delete(route.Viewers, vid)
			}
		}
		remove := route.Provisional && route.BrowserConn == nil && route.PendingController == nil && len(route.Viewers) == 0
		route.mu.Unlock()
		if remove {
			delete(r.routes, sessionID)
		}
	}
}

func (r *PTYRoutes) removeProvisionalIfEmpty(sessionID string, expected *PTYRoute) {
	r.mu.Lock()
	defer r.mu.Unlock()
	route := r.routes[sessionID]
	if route == nil || route != expected {
		return
	}
	route.mu.Lock()
	defer route.mu.Unlock()
	if route.Provisional && route.BrowserConn == nil && route.PendingController == nil && len(route.Viewers) == 0 {
		delete(r.routes, sessionID)
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
		pending := route.PendingController
		wid := route.WingID
		viewers := make([]*websocket.Conn, 0, len(route.Viewers))
		for _, vc := range route.Viewers {
			viewers = append(viewers, vc)
		}
		route.mu.Unlock()
		if wid != wingID {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if bc != nil {
			bc.Write(ctx, websocket.MessageText, msg)
		}
		if pending != nil && pending != bc {
			pending.Write(ctx, websocket.MessageText, msg)
		}
		for _, vc := range viewers {
			vc.Write(ctx, websocket.MessageText, msg)
		}
		cancel()
	}
}

// handlePTYWS handles the browser WebSocket for a PTY session.
func (s *Server) handlePTYWS(w http.ResponseWriter, r *http.Request) {
	// Auth
	var userID string
	var userEmail string
	var userDisplayName string
	var userOrgIDs []string
	if u := s.sessionUser(r); u != nil {
		userID = u.ID
		userOrgIDs = u.OrgIDs
		userDisplayName = u.DisplayName
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
		if s.JWTPubKey() != nil {
			if claims, err := ValidateWingJWT(s.JWTPubKey(), token); err == nil {
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
	defer s.clearTunnelRequests(conn)

	ctx := r.Context()

	// On browser disconnect: clear BrowserConn on all owned routes
	defer s.PTY.ClearBrowser(conn)

	// Wing ID from URL query param — used as the default for messages that do not carry one.
	queryWingID := r.URL.Query().Get("wing_id")

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
			start.Email = userEmail
			start.DisplayName = userDisplayName
			start.OrgRole = ""
			start.Passkeys = nil
			if s.LocalMode || wing.UserID == userID {
				start.OrgRole = "owner"
			} else if wing.OrgID != "" && s.Store != nil {
				start.OrgRole = s.Store.GetOrgMemberRole(wing.OrgID, userID)
			}
			if s.Store != nil {
				if creds, err := s.Store.ListPasskeyCredentials(userID); err == nil && len(creds) > 0 {
					for _, c := range creds {
						start.Passkeys = append(start.Passkeys, base64.StdEncoding.EncodeToString(c.PublicKey))
					}
				}
			}

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

			attach.UserID = userID
			attach.Email = userEmail
			attach.OrgRole = ""
			attach.Passkeys = nil
			if s.LocalMode || wing.UserID == userID {
				attach.OrgRole = "owner"
			} else if wing.OrgID != "" && s.Store != nil {
				attach.OrgRole = s.Store.GetOrgMemberRole(wing.OrgID, userID)
			}

			if attach.Spectate {
				// Spectator mode: add as read-only viewer, don't overwrite controller.
				// The relay records a pending viewer so wing challenges can be routed
				// back to it. A locked wing still performs local passkey authorization
				// before emitting any viewer-tagged terminal content.
				viewerID := uuid.New().String()[:8]
				attach.ViewerID = viewerID
				if !s.PTY.AddViewer(attach.SessionID, viewerID, wing.WingID, conn) {
					errMsg, _ := json.Marshal(ws.ErrorMsg{Type: ws.TypeError, Message: "session not found"})
					conn.Write(ctx, websocket.MessageText, errMsg)
					continue
				}
				log.Printf("pty session %s spectator added (viewer=%s user=%s)", attach.SessionID, viewerID, userID)
			} else {
				// Normal reattach remains pending until the wing returns pty.started.
				// This prevents a caller who fails wing-local passkey policy from
				// displacing the currently authorized controller at the relay.
				if !s.PTY.SetPendingController(attach.SessionID, wing.WingID, userID, conn) {
					errMsg, _ := json.Marshal(ws.ErrorMsg{Type: ws.TypeError, Message: "session not found"})
					conn.Write(ctx, websocket.MessageText, errMsg)
					continue
				}
				log.Printf("pty session %s reattach pending wing authorization (user=%s)", attach.SessionID, userID)
			}

			fwd, _ := json.Marshal(attach)
			wing.Conn.Write(ctx, websocket.MessageText, fwd)

		case ws.TypePTYInput, ws.TypePTYResize, ws.TypePTYAttentionAck, ws.TypePTYMigrate:
			var control struct {
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(data, &control); err != nil {
				continue
			}
			routeWingID, allowed := s.PTY.ControllerWing(control.SessionID, conn)
			if !allowed {
				continue
			}
			wing := s.findAnyWingByWingID(routeWingID)
			if wing == nil {
				continue
			}
			wing.Conn.Write(ctx, websocket.MessageText, data)

		case ws.TypePasskeyResponse:
			var response ws.PasskeyResponse
			if err := json.Unmarshal(data, &response); err != nil {
				continue
			}
			routeWingID, allowed := s.PTY.AuthenticationWing(response.SessionID, response.ViewerID, conn)
			if !allowed {
				continue
			}
			wing := s.findAnyWingByWingID(routeWingID)
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
			// Also remove from spectators
			for vid, vc := range route.Viewers {
				if vc == conn {
					delete(route.Viewers, vid)
				}
			}
			route.mu.Unlock()

		case ws.TypePTYKill:
			var kill ws.PTYKill
			if err := json.Unmarshal(data, &kill); err != nil {
				continue
			}
			routeWingID, allowed := s.PTY.ControllerWing(kill.SessionID, conn)
			if !allowed {
				continue
			}
			wing := s.findAnyWingByWingID(routeWingID)
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
			req.SenderOrgRole = ""
			req.SenderPasskeys = nil
			if wing.UserID == userID {
				req.SenderOrgRole = "owner"
			} else if wing.OrgID != "" && s.Store != nil {
				req.SenderOrgRole = s.Store.GetOrgMemberRole(wing.OrgID, userID)
			}
			if s.Store != nil {
				if creds, err := s.Store.ListPasskeyCredentials(userID); err == nil && len(creds) > 0 {
					for _, c := range creds {
						req.SenderPasskeys = append(req.SenderPasskeys, base64.StdEncoding.EncodeToString(c.PublicKey))
					}
				}
			}
			if !s.registerTunnelRequest(wing.WingID, req.RequestID, conn) {
				errMsg, _ := json.Marshal(ws.ErrorMsg{Type: ws.TypeError, Message: "invalid or duplicate tunnel request"})
				conn.Write(ctx, websocket.MessageText, errMsg)
				continue
			}
			fwdTunnel, _ := json.Marshal(req)
			wing.Conn.Write(ctx, websocket.MessageText, fwdTunnel)
		}
	}
}

// forwardPTYToBrowser routes a PTY message only when it came from the wing recorded on
// the route. Session IDs are routing metadata, not authorization credentials.
func (s *Server) forwardPTYToBrowser(sessionID, sourceWingID string, data []byte) {
	route := s.PTY.Get(sessionID)
	if route == nil {
		return
	}
	route.mu.Lock()
	routeWingID := route.WingID
	route.mu.Unlock()
	if routeWingID == "" || routeWingID != sourceWingID {
		return
	}

	// Extract viewer_id for spectator routing
	var viewerID string
	var env ws.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return
	}

	// Handle pty.exited: clean up route + send ntfy exit notification
	if env.Type == ws.TypePTYExited {
		var exited ws.PTYExited
		if err := json.Unmarshal(data, &exited); err != nil {
			return
		}
		viewerID = exited.ViewerID

		// Spectator exit: route to specific viewer, don't remove the route
		if viewerID != "" {
			route.mu.Lock()
			vc := route.Viewers[viewerID]
			delete(route.Viewers, viewerID)
			route.mu.Unlock()
			s.PTY.removeProvisionalIfEmpty(sessionID, route)
			if vc != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				vc.Write(ctx, websocket.MessageText, data)
				cancel()
			}
			return
		}

		// Controller exit: send to controller + all spectators, clean up route
		route.mu.Lock()
		bc := route.BrowserConn
		pending := route.PendingController
		userID := route.UserID
		agent := route.Agent
		cwd := route.CWD
		viewers := make(map[string]*websocket.Conn, len(route.Viewers))
		for k, v := range route.Viewers {
			viewers[k] = v
		}
		route.mu.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if bc != nil {
			bc.Write(ctx, websocket.MessageText, data)
		}
		if pending != nil && pending != bc {
			pending.Write(ctx, websocket.MessageText, data)
		}
		for _, vc := range viewers {
			vc.Write(ctx, websocket.MessageText, data)
		}

		// Send ntfy exit notification
		clickURL := ntfyClickURL(sessionID)
		s.trySendNtfy("exit:"+sessionID, userID, func(c *ntfy.Client) {
			c.SendExit(sessionID, agent, cwd, exited.ExitCode, clickURL)
		})

		s.PTY.Remove(sessionID)
		return
	}

	// Extract viewer_id from output/preview messages for spectator routing
	switch env.Type {
	case ws.TypePTYOutput:
		var out ws.PTYOutput
		if json.Unmarshal(data, &out) == nil {
			viewerID = out.ViewerID
		}
	case ws.TypePTYPreview:
		var prev ws.PTYPreview
		if json.Unmarshal(data, &prev) == nil {
			viewerID = prev.ViewerID
		}
	case ws.TypePTYStarted:
		var started ws.PTYStarted
		if json.Unmarshal(data, &started) == nil {
			viewerID = started.ViewerID
		}
	case ws.TypePasskeyChallenge:
		var challenge ws.PasskeyChallenge
		if json.Unmarshal(data, &challenge) == nil {
			viewerID = challenge.ViewerID
		}
	case ws.TypeError:
		var protocolError ws.ErrorMsg
		if json.Unmarshal(data, &protocolError) == nil {
			viewerID = protocolError.ViewerID
		}
	}

	// A successful controller reattach becomes authoritative only after the
	// wing has completed its local authorization and key setup.
	if env.Type == ws.TypePTYStarted && viewerID == "" {
		route.mu.Lock()
		if route.PendingController != nil {
			route.BrowserConn = route.PendingController
			route.UserID = route.PendingUserID
			route.PendingController = nil
			route.PendingUserID = ""
			route.Provisional = false
		}
		route.mu.Unlock()
	} else if env.Type == ws.TypePTYStarted && viewerID != "" {
		route.mu.Lock()
		route.Provisional = false
		route.mu.Unlock()
	}

	// Route to specific spectator
	if viewerID != "" {
		route.mu.Lock()
		vc := route.Viewers[viewerID]
		if env.Type == ws.TypeError {
			delete(route.Viewers, viewerID)
		}
		route.mu.Unlock()
		if env.Type == ws.TypeError {
			s.PTY.removeProvisionalIfEmpty(sessionID, route)
		}
		if vc == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		vc.Write(ctx, websocket.MessageText, data)
		return
	}

	// Challenges and failures for a controller reattach belong to the pending
	// connection, not the old authorized controller. A failure clears only the
	// pending attempt and leaves the old controller in place.
	if env.Type == ws.TypePasskeyChallenge || env.Type == ws.TypeError {
		route.mu.Lock()
		pending := route.PendingController
		if env.Type == ws.TypeError {
			route.PendingController = nil
			route.PendingUserID = ""
		}
		route.mu.Unlock()
		if env.Type == ws.TypeError {
			s.PTY.removeProvisionalIfEmpty(sessionID, route)
		}
		if pending != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			pending.Write(ctx, websocket.MessageText, data)
			return
		}
	}

	// Route to controller (existing behavior)
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

func (s *Server) registerTunnelRequest(wingID, requestID string, browser *websocket.Conn) bool {
	if wingID == "" || requestID == "" || len(requestID) > 200 || browser == nil {
		return false
	}
	now := time.Now()
	s.tunnelMu.Lock()
	defer s.tunnelMu.Unlock()
	for key, pending := range s.tunnelRequests {
		if now.Sub(pending.CreatedAt) > pendingTunnelRequestTTL {
			delete(s.tunnelRequests, key)
		}
	}
	key := tunnelRequestKey{WingID: wingID, RequestID: requestID}
	if _, exists := s.tunnelRequests[key]; exists || len(s.tunnelRequests) >= maxPendingTunnelRequests {
		return false
	}
	s.tunnelRequests[key] = pendingTunnelRequest{Browser: browser, CreatedAt: now}
	return true
}

func (s *Server) clearTunnelRequests(browser *websocket.Conn) {
	s.tunnelMu.Lock()
	defer s.tunnelMu.Unlock()
	for key, pending := range s.tunnelRequests {
		if pending.Browser == browser {
			delete(s.tunnelRequests, key)
		}
	}
}
