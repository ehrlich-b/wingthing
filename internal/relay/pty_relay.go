package relay

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
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

type userOrgContext struct {
	Email    string            `json:"email"`
	OrgIDs   []string          `json:"org_ids"`
	OrgRoles map[string]string `json:"org_roles"`
}

func newRelayRouteID() string {
	return strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
}

func (s *Server) remoteUserOrgContext(ctx context.Context, userID string) (userOrgContext, bool) {
	if s.Config.LoginNodeAddr == "" || userID == "" {
		return userOrgContext{}, false
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(s.Config.LoginNodeAddr, "/")+"/internal/user-orgs/"+url.PathEscape(userID), nil)
	if err != nil {
		return userOrgContext{}, false
	}
	s.authorizeInternalRequest(request)
	response, err := (&http.Client{Timeout: 3 * time.Second}).Do(request)
	if err != nil {
		return userOrgContext{}, false
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return userOrgContext{}, false
	}
	const maxUserOrgContextBytes = 64 << 10
	body, err := io.ReadAll(io.LimitReader(response.Body, maxUserOrgContextBytes+1))
	if err != nil || len(body) > maxUserOrgContextBytes {
		return userOrgContext{}, false
	}
	var result userOrgContext
	if json.Unmarshal(body, &result) != nil {
		return userOrgContext{}, false
	}
	return result, true
}

func roleForWingUser(s *Server, wing *ConnectedWing, userID string, orgIDs []string, orgRoles map[string]string) string {
	if wing == nil || userID == "" {
		return ""
	}
	if s.LocalMode || wing.UserID == userID {
		return "owner"
	}
	if wing.OrgID == "" {
		return ""
	}
	if role := orgRoles[wing.OrgID]; role == "owner" || role == "admin" || role == "member" {
		return role
	}
	if !s.IsEdge() && s.Store != nil {
		if role := s.Store.GetOrgMemberRole(wing.OrgID, userID); role == "owner" || role == "admin" || role == "member" {
			return role
		}
	}
	// N-1 login nodes return membership IDs but not the additive exact-role map.
	// Preserve ordinary member access while refusing to infer elevated authority.
	for _, orgID := range orgIDs {
		if orgID == wing.OrgID {
			return "member"
		}
	}
	return ""
}

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
	Browser          *websocket.Conn
	CreatedAt        time.Time
	Coordination     bool
	ResponseBytes    int
	ResponseMessages int
}

const (
	maxPendingTunnelRequests = 4096
	pendingTunnelRequestTTL  = 5 * time.Minute
	// Coordination is the only free-tier payload allowed through the hosted
	// service. The wing verifies the encrypted operation class, while the relay
	// independently caps the opaque response so a modified wing cannot turn one
	// signaling request into an unbounded ciphertext stream.
	maxCoordinationResponseBytes    = 2 * ws.MaxCoordinationTunnelPayload
	maxCoordinationResponseMessages = 16

	// Provisional routes are created by attach for session IDs the wing has
	// not confirmed. An unknown ID gets no wing response, so without a cap and
	// expiry one authenticated browser could grow the route map without bound.
	maxProvisionalRoutes = 1024
	provisionalRouteTTL  = 2 * time.Minute
	// Keep one authenticated browser from consuming the shared provisional
	// budget and denying session setup to every other signed-in user.
	maxProvisionalRoutesPerBrowser = 128

	// One route's viewer map is also attacker-growable by re-attaching to the
	// same (possibly nonexistent) session ID with fresh viewer IDs.
	maxViewersPerRoute = 64

	// Tunnel requests share one relay-wide table. A per-browser slice prevents a
	// single authenticated connection from pinning all entries until the TTL.
	maxPendingTunnelRequestsPerBrowser = 128
)

// admitProvisionalLocked sweeps expired provisional routes and reports whether
// a new provisional route may be created. Caller holds r.mu.
func (r *PTYRoutes) admitProvisionalLocked(browser *websocket.Conn) bool {
	now := time.Now()
	provisional := 0
	forBrowser := 0
	for id, route := range r.routes {
		route.mu.Lock()
		isProvisional := route.Provisional
		createdAt := route.CreatedAt
		referencesBrowser := routeReferencesBrowserLocked(route, browser)
		route.mu.Unlock()
		if !isProvisional {
			continue
		}
		if now.Sub(createdAt) > provisionalRouteTTL {
			delete(r.routes, id)
			continue
		}
		provisional++
		if referencesBrowser {
			forBrowser++
		}
	}
	return provisional < maxProvisionalRoutes && (browser == nil || forBrowser < maxProvisionalRoutesPerBrowser)
}

func routeReferencesBrowserLocked(route *PTYRoute, browser *websocket.Conn) bool {
	if route == nil || browser == nil {
		return false
	}
	if route.BrowserConn == browser || route.PendingController == browser {
		return true
	}
	for _, viewer := range route.Viewers {
		if viewer == browser {
			return true
		}
	}
	return false
}

func (r *PTYRoutes) provisionalCountForBrowserLocked(browser *websocket.Conn) int {
	if browser == nil {
		return 0
	}
	count := 0
	for _, route := range r.routes {
		route.mu.Lock()
		if route.Provisional && routeReferencesBrowserLocked(route, browser) {
			count++
		}
		route.mu.Unlock()
	}
	return count
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

// AddControllerStart reserves a bounded provisional route for a new session.
// It becomes durable only after the wing confirms pty.started. This prevents a
// silent or disconnected wing from letting authenticated start attempts pin
// unbounded relay state.
func (r *PTYRoutes) AddControllerStart(sessionID string, route *PTYRoute) bool {
	if sessionID == "" || route == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.routes[sessionID] != nil || !r.admitProvisionalLocked(route.BrowserConn) {
		return false
	}
	route.Provisional = true
	route.CreatedAt = time.Now()
	r.routes[sessionID] = route
	return true
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
		if !r.admitProvisionalLocked(conn) {
			return false
		}
		route = &PTYRoute{WingID: wingID, Provisional: true, CreatedAt: time.Now()}
		r.routes[sessionID] = route
	}
	provisionalForBrowser := r.provisionalCountForBrowserLocked(conn)
	route.mu.Lock()
	if route.WingID != "" && route.WingID != wingID {
		route.mu.Unlock()
		return false
	}
	if route.WingID == "" {
		route.WingID = wingID
	}
	if route.Provisional && !routeReferencesBrowserLocked(route, conn) && provisionalForBrowser >= maxProvisionalRoutesPerBrowser {
		route.mu.Unlock()
		return false
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
		if !r.admitProvisionalLocked(conn) {
			return false
		}
		route = &PTYRoute{WingID: wingID, Provisional: true, CreatedAt: time.Now()}
		r.routes[sessionID] = route
	}
	provisionalForBrowser := r.provisionalCountForBrowserLocked(conn)
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
	if route.Provisional && !routeReferencesBrowserLocked(route, conn) && provisionalForBrowser >= maxProvisionalRoutesPerBrowser {
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

func (r *PTYRoutes) removeProvisional(sessionID string, expected *PTYRoute) {
	r.mu.Lock()
	defer r.mu.Unlock()
	route := r.routes[sessionID]
	if route == nil || route != expected {
		return
	}
	route.mu.Lock()
	defer route.mu.Unlock()
	if route.Provisional {
		delete(r.routes, sessionID)
	}
}

func (r *PTYRoutes) offlineRecipients(wingID string) []*websocket.Conn {
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := make(map[*websocket.Conn]bool)
	var recipients []*websocket.Conn
	for _, route := range r.routes {
		route.mu.Lock()
		if route.WingID == wingID {
			for _, connection := range append([]*websocket.Conn{route.BrowserConn, route.PendingController}, mapValues(route.Viewers)...) {
				if connection != nil && !seen[connection] {
					seen[connection] = true
					recipients = append(recipients, connection)
				}
			}
		}
		route.mu.Unlock()
	}
	return recipients
}

func mapValues(values map[string]*websocket.Conn) []*websocket.Conn {
	result := make([]*websocket.Conn, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}

// NotifyWingOffline sends a wing.offline message to all PTY browsers connected
// to the given wing. Snapshot route ownership before I/O so one slow browser
// cannot hold the global routing lock and stall every session on the relay.
func (r *PTYRoutes) NotifyWingOffline(wingID string) {
	msg := []byte(`{"type":"wing.offline"}`)
	for _, connection := range r.offlineRecipients(wingID) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := connection.Write(ctx, websocket.MessageText, msg); err != nil {
			log.Printf("notify browser that wing %s is offline: %v", wingID, err)
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
	var userOrgRoles map[string]string
	if u := s.sessionUser(r); u != nil {
		userID = u.ID
		userOrgIDs = u.OrgIDs
		userOrgRoles = u.OrgRoles
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
	if !s.roostUserIDAllowed(userID) {
		http.Error(w, "this account is not enrolled in this roost", http.StatusForbidden)
		return
	}
	if !s.IsEdge() && s.Store != nil {
		// Bearer-authenticated native connectors do not have a web-session User
		// object. Hydrate the same provider identity that cookie callers receive;
		// member path ACLs and wing-local admin overrides are email based.
		if storedUser, err := s.Store.GetUserByID(userID); err == nil && storedUser != nil {
			userDisplayName = storedUser.DisplayName
			if storedUser.Email != nil {
				userEmail = *storedUser.Email
			}
		}
		orgs, _ := s.Store.ListOrgsForUser(userID)
		userOrgIDs = make([]string, 0, len(orgs))
		userOrgRoles = make(map[string]string, len(orgs))
		for _, org := range orgs {
			userOrgIDs = append(userOrgIDs, org.ID)
			if role := s.Store.GetOrgMemberRole(org.ID, userID); role != "" {
				userOrgRoles[org.ID] = role
			}
		}
	} else if s.IsEdge() && s.Config.LoginNodeAddr != "" {
		if remote, ok := s.remoteUserOrgContext(r.Context(), userID); ok {
			userOrgIDs = remote.OrgIDs
			userOrgRoles = remote.OrgRoles
			// A successful current lookup is authoritative, including an empty
			// address after the login provider cleared a stale email.
			userEmail = remote.Email
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

	conn, err := websocket.Accept(w, r, s.browserWebSocketAcceptOptions())
	if err != nil {
		log.Printf("pty websocket accept: %v", err)
		return
	}
	conn.SetReadLimit(512 * 1024) // match wing/client envelope cap; policy applies tighter purpose bounds
	defer func() { _ = conn.CloseNow() }()

	s.trackBrowser(conn, userID)
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
			if !s.relayAccess(userID).Allowed {
				if err := writeWebSocketJSON(ctx, conn, ws.ErrorMsg{Type: ws.TypeError, Message: "hosted relay is not included on the free direct tier; connect directly, use a self-hosted roost, or upgrade to Pro"}); err != nil {
					log.Printf("deny PTY start: %v", err)
					return
				}
				continue
			}
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
				if err := writeWebSocketJSON(ctx, conn, ws.ErrorMsg{Type: ws.TypeError, Message: "no wing connected"}); err != nil {
					log.Printf("report missing wing for PTY start: %v", err)
					return
				}
				continue
			}
			if !ws.HostedRelayAllowed(wing.HostedRelay) {
				s.denyHostedRelay(ctx, conn, userID, wing.WingID, ws.TypePTYStart, "", "")
				continue
			}

			sessionID := newRelayRouteID()
			start.SessionID = sessionID
			start.UserID = userID
			start.Email = userEmail
			start.DisplayName = userDisplayName
			start.OrgRole = roleForWingUser(s, wing, userID, userOrgIDs, userOrgRoles)
			start.Passkeys = nil
			if s.Store != nil {
				if creds, err := s.Store.ListPasskeyCredentials(userID); err == nil && len(creds) > 0 {
					for _, c := range creds {
						start.Passkeys = append(start.Passkeys, base64.StdEncoding.EncodeToString(c.PublicKey))
					}
				}
			}

			route := &PTYRoute{BrowserConn: conn, UserID: userID, WingID: wing.WingID, Agent: start.Agent, CWD: start.CWD}
			if !s.PTY.AddControllerStart(sessionID, route) {
				if err := writeWebSocketJSON(ctx, conn, ws.ErrorMsg{Type: ws.TypeError, Message: "relay has too many pending session starts; retry shortly"}); err != nil {
					log.Printf("report pending PTY start limit: %v", err)
					return
				}
				continue
			}

			if err := writeWebSocketJSON(ctx, wing.Conn, start); err != nil {
				s.PTY.removeProvisional(sessionID, route)
				log.Printf("forward PTY start to wing %s: %v", wing.WingID, err)
				return
			}

			log.Printf("pty session %s started (user=%s wing=%s agent=%s)", sessionID, userID, wing.WingID, start.Agent)

		case ws.TypePTYAttach:
			if !s.relayAccess(userID).Allowed {
				if err := writeWebSocketJSON(ctx, conn, ws.ErrorMsg{Type: ws.TypeError, Message: "hosted relay is not included on the free direct tier; connect directly, use a self-hosted roost, or upgrade to Pro"}); err != nil {
					log.Printf("deny PTY attach: %v", err)
					return
				}
				continue
			}
			var attach ws.PTYAttach
			if err := json.Unmarshal(data, &attach); err != nil {
				continue
			}
			if !ws.ValidSessionID(attach.SessionID) {
				if err := writeWebSocketJSON(ctx, conn, ws.ErrorMsg{Type: ws.TypeError, Message: "invalid session ID"}); err != nil {
					log.Printf("report invalid PTY attach session ID: %v", err)
					return
				}
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
				if err := writeWebSocketJSON(ctx, conn, ws.ErrorMsg{Type: ws.TypeError, Message: "wing not found"}); err != nil {
					log.Printf("report missing wing for PTY attach: %v", err)
					return
				}
				continue
			}
			if !ws.HostedRelayAllowed(wing.HostedRelay) {
				s.denyHostedRelay(ctx, conn, userID, wing.WingID, ws.TypePTYAttach, "", attach.SessionID)
				continue
			}

			attach.UserID = userID
			attach.Email = userEmail
			attach.OrgRole = roleForWingUser(s, wing, userID, userOrgIDs, userOrgRoles)
			attach.Passkeys = nil

			if attach.Spectate {
				// Spectator mode: add as read-only viewer, don't overwrite controller.
				// The relay records a pending viewer so wing challenges can be routed
				// back to it. A locked wing still performs local passkey authorization
				// before emitting any viewer-tagged terminal content.
				viewerID := newRelayRouteID()
				attach.ViewerID = viewerID
				if !s.PTY.AddViewer(attach.SessionID, viewerID, wing.WingID, conn) {
					if err := writeWebSocketJSON(ctx, conn, ws.ErrorMsg{Type: ws.TypeError, Message: "session not found"}); err != nil {
						log.Printf("report missing spectator session: %v", err)
						return
					}
					continue
				}
				log.Printf("pty session %s spectator added (viewer=%s user=%s)", attach.SessionID, viewerID, userID)
			} else {
				// Normal reattach remains pending until the wing returns pty.started.
				// This prevents a caller who fails wing-local passkey policy from
				// displacing the currently authorized controller at the relay.
				if !s.PTY.SetPendingController(attach.SessionID, wing.WingID, userID, conn) {
					if err := writeWebSocketJSON(ctx, conn, ws.ErrorMsg{Type: ws.TypeError, Message: "session not found"}); err != nil {
						log.Printf("report missing controller session: %v", err)
						return
					}
					continue
				}
				log.Printf("pty session %s reattach pending wing authorization (user=%s)", attach.SessionID, userID)
			}

			if err := writeWebSocketJSON(ctx, wing.Conn, attach); err != nil {
				log.Printf("forward PTY attach to wing %s: %v", wing.WingID, err)
				return
			}

		case ws.TypePTYInput, ws.TypePTYResize, ws.TypePTYAttentionAck, ws.TypePTYMigrate:
			var control struct {
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(data, &control); err != nil {
				continue
			}
			if !ws.ValidSessionID(control.SessionID) {
				continue
			}
			if !s.relayAccess(userID).Allowed {
				s.denyRelayEntitlement(ctx, conn, control.SessionID)
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
			if !ws.HostedRelayAllowed(wing.HostedRelay) {
				s.denyHostedRelay(ctx, conn, userID, wing.WingID, env.Type, "", control.SessionID)
				continue
			}
			if err := wing.Conn.Write(ctx, websocket.MessageText, data); err != nil {
				log.Printf("forward PTY control to wing %s: %v", wing.WingID, err)
				return
			}

		case ws.TypePasskeyResponse:
			var response ws.PasskeyResponse
			if err := json.Unmarshal(data, &response); err != nil {
				continue
			}
			if !ws.ValidSessionID(response.SessionID) {
				continue
			}
			if !s.relayAccess(userID).Allowed {
				s.denyRelayEntitlement(ctx, conn, response.SessionID)
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
			if !ws.HostedRelayAllowed(wing.HostedRelay) {
				s.denyHostedRelay(ctx, conn, userID, wing.WingID, ws.TypePasskeyResponse, "", response.SessionID)
				continue
			}
			if err := wing.Conn.Write(ctx, websocket.MessageText, data); err != nil {
				log.Printf("forward passkey response to wing %s: %v", wing.WingID, err)
				return
			}

		case ws.TypePTYDetach:
			var det ws.PTYDetach
			if err := json.Unmarshal(data, &det); err != nil {
				continue
			}
			if !ws.ValidSessionID(det.SessionID) {
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
			if !ws.ValidSessionID(kill.SessionID) {
				continue
			}
			if !s.relayAccess(userID).Allowed {
				s.denyRelayEntitlement(ctx, conn, kill.SessionID)
				continue
			}
			routeWingID, allowed := s.PTY.ControllerWing(kill.SessionID, conn)
			if !allowed {
				continue
			}
			wing := s.findAnyWingByWingID(routeWingID)
			if wing == nil {
				continue
			}
			if !ws.HostedRelayAllowed(wing.HostedRelay) {
				s.denyHostedRelay(ctx, conn, userID, wing.WingID, ws.TypePTYKill, "", kill.SessionID)
				continue
			}
			if err := writeWebSocketJSON(ctx, wing.Conn, kill); err != nil {
				log.Printf("forward PTY kill to wing %s: %v", wing.WingID, err)
				return
			}

		case ws.TypeTunnelRequest:
			var req ws.TunnelRequest
			if err := json.Unmarshal(data, &req); err != nil {
				continue
			}
			relayAllowed := s.relayAccess(userID).Allowed
			if !relayAllowed && (!ws.IsCoordinationTunnelPurpose(req.Purpose) || len(req.Payload) > ws.MaxCoordinationTunnelPayload) {
				errMsg := ws.ErrorMsg{
					Type: ws.TypeError, RequestID: req.RequestID,
					Message: "hosted payload tunnels are not included on the free direct tier",
				}
				if err := writeWebSocketJSON(ctx, conn, errMsg); err != nil {
					log.Printf("deny hosted payload tunnel: %v", err)
					return
				}
				continue
			}
			wing := s.findAnyWingByWingID(req.WingID)
			if wing == nil || !s.canAccessWing(userID, wing, userOrgIDs) {
				if err := writeWebSocketJSON(ctx, conn, ws.ErrorMsg{Type: ws.TypeError, RequestID: req.RequestID, Message: "wing not found"}); err != nil {
					log.Printf("report missing tunnel wing: %v", err)
					return
				}
				continue
			}
			if !ws.HostedRelayAllowed(wing.HostedRelay) && (!ws.IsCoordinationTunnelPurpose(req.Purpose) || len(req.Payload) > ws.MaxCoordinationTunnelPayload) {
				s.denyHostedRelay(ctx, conn, userID, wing.WingID, ws.TypeTunnelRequest, req.RequestID, "")
				continue
			}
			if (!relayAllowed || !ws.HostedRelayAllowed(wing.HostedRelay)) && !wing.PurposeBinding {
				errMsg := ws.ErrorMsg{
					Type: ws.TypeError, RequestID: req.RequestID,
					Message: "wing must be upgraded before direct-tier coordination can be used safely",
				}
				if err := writeWebSocketJSON(ctx, conn, errMsg); err != nil {
					log.Printf("deny unsafe direct-tier coordination: %v", err)
					return
				}
				continue
			}
			// Inject user identity into tunnel request envelope
			req.SenderUserID = userID
			req.SenderEmail = userEmail
			req.SenderOrgRole = roleForWingUser(s, wing, userID, userOrgIDs, userOrgRoles)
			req.SenderPasskeys = nil
			if s.Store != nil {
				if creds, err := s.Store.ListPasskeyCredentials(userID); err == nil && len(creds) > 0 {
					for _, c := range creds {
						req.SenderPasskeys = append(req.SenderPasskeys, base64.StdEncoding.EncodeToString(c.PublicKey))
					}
				}
			}
			// A response may bypass live relay-entitlement checks only when the wing
			// verifies that the encrypted inner operation matches this public purpose.
			// An N-1 wing does not, so treat even a coordination-labeled request as a
			// normal relay payload for revocation purposes.
			coordination := trustedCoordinationRequest(wing, req)
			if !s.registerTunnelRequest(wing.WingID, req.RequestID, conn, coordination) {
				if err := writeWebSocketJSON(ctx, conn, ws.ErrorMsg{Type: ws.TypeError, RequestID: req.RequestID, Message: "invalid or duplicate tunnel request"}); err != nil {
					log.Printf("report rejected tunnel request: %v", err)
					return
				}
				continue
			}
			if err := writeWebSocketJSON(ctx, wing.Conn, req); err != nil {
				log.Printf("forward tunnel request to wing %s: %v", wing.WingID, err)
				return
			}
		}
	}
}

func trustedCoordinationRequest(wing *ConnectedWing, req ws.TunnelRequest) bool {
	return wing != nil && wing.PurposeBinding && ws.IsCoordinationTunnelPurpose(req.Purpose) && len(req.Payload) <= ws.MaxCoordinationTunnelPayload
}

func (s *Server) denyRelayEntitlement(ctx context.Context, conn *websocket.Conn, sessionID string) {
	if err := writeWebSocketJSON(ctx, conn, ws.ErrorMsg{
		Type: ws.TypeError, SessionID: sessionID,
		Message: "hosted relay is not included on the free direct tier; connect directly, use a self-hosted roost, or upgrade to Pro",
	}); err != nil {
		log.Printf("report relay entitlement denial: %v", err)
	}
}

func (s *Server) denyHostedRelay(ctx context.Context, conn *websocket.Conn, userID, wingID, operation, requestID, sessionID string) {
	if err := writeWebSocketJSON(ctx, conn, ws.ErrorMsg{
		Type: ws.TypeError, RequestID: requestID, SessionID: sessionID,
		Message: "hosted relay payload transport is disabled by this wing",
	}); err != nil {
		log.Printf("report hosted relay denial: %v", err)
	}
	detail := "wing=" + wingID + " operation=" + operation + " policy=deny"
	log.Printf("[audit] hosted_relay_denied user=%s %s", userID, detail)
	if s.Store != nil {
		if err := s.Store.AppendAudit(userID, "hosted_relay_denied", &detail); err != nil {
			log.Printf("hosted relay denial audit: %v", err)
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
				s.writeRelayPayload(ctx, vc, data, sessionID, "")
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
			s.writeRelayPayload(ctx, bc, data, sessionID, "")
		}
		if pending != nil && pending != bc {
			s.writeRelayPayload(ctx, pending, data, sessionID, "")
		}
		for _, vc := range viewers {
			s.writeRelayPayload(ctx, vc, data, sessionID, "")
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
		}
		route.Provisional = false
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
		s.writeRelayPayload(ctx, vc, data, sessionID, "")
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
			s.writeRelayPayload(ctx, pending, data, sessionID, "")
			return
		}
	}

	// Route to controller (existing behavior)
	route.mu.Lock()
	bc := route.BrowserConn
	userID := route.UserID
	provisionalError := env.Type == ws.TypeError && route.Provisional
	route.mu.Unlock()
	if provisionalError {
		s.PTY.removeProvisional(sessionID, route)
	}

	if bc == nil {
		// Detached — drop the data (wing has ring buffer for replay)
		return
	}
	if allowed, notify := s.browserRelayPayloadAccess(bc, "session:"+sessionID); !allowed {
		if notify {
			denied := ws.ErrorMsg{
				Type: ws.TypeError, SessionID: sessionID,
				Message: "hosted relay is not included on the free direct tier; connect directly, use a self-hosted roost, or upgrade to Pro",
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := writeWebSocketJSON(ctx, bc, denied); err != nil {
				log.Printf("report browser relay denial: %v", err)
			}
			cancel()
		}
		return
	}

	// Meter outbound bandwidth (relay → browser is what costs on Fly)
	if s.Bandwidth != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.Bandwidth.Wait(ctx, userID, len(data)); err != nil {
			msg := ws.BandwidthExceeded{
				Type:    ws.TypeBandwidthExceeded,
				Message: "Monthly bandwidth limit exceeded. Upgrade to pro for higher limits.",
			}
			if writeErr := writeWebSocketJSON(ctx, bc, msg); writeErr != nil {
				log.Printf("report relay bandwidth exhaustion: %v", writeErr)
			}
			// Detach browser so subsequent forwards are dropped (send once)
			route.mu.Lock()
			if route.BrowserConn == bc {
				route.BrowserConn = nil
			}
			route.mu.Unlock()
			if closeErr := bc.Close(websocket.StatusNormalClosure, "bandwidth exceeded"); closeErr != nil {
				log.Printf("close bandwidth-exhausted browser connection: %v", closeErr)
			}
			return
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := bc.Write(ctx, websocket.MessageText, data); err != nil {
		log.Printf("forward PTY payload to browser: %v", err)
		route.mu.Lock()
		if route.BrowserConn == bc {
			route.BrowserConn = nil
		}
		route.mu.Unlock()
	}
}

func (s *Server) writeRelayPayload(ctx context.Context, conn *websocket.Conn, data []byte, sessionID, requestID string) {
	resource := "session:" + sessionID
	if requestID != "" {
		resource = "request:" + requestID
	}
	allowed, notify := s.browserRelayPayloadAccess(conn, resource)
	if allowed {
		s.writeMeteredRelayPayload(ctx, conn, data)
		return
	}
	if notify {
		denied := ws.ErrorMsg{
			Type: ws.TypeError, SessionID: sessionID, RequestID: requestID,
			Message: "hosted relay is not included on the free direct tier; connect directly, use a self-hosted roost, or upgrade to Pro",
		}
		if err := writeWebSocketJSON(ctx, conn, denied); err != nil {
			log.Printf("report relay payload denial: %v", err)
		}
	}
}

func (s *Server) writeMeteredRelayPayload(ctx context.Context, conn *websocket.Conn, data []byte) bool {
	userID := s.browserUserID(conn)
	if s.Bandwidth != nil {
		if userID == "" {
			return false
		}
		if err := s.Bandwidth.Wait(ctx, userID, len(data)); err != nil {
			message := ws.BandwidthExceeded{
				Type:    ws.TypeBandwidthExceeded,
				Message: "Monthly bandwidth limit exceeded. Upgrade to pro for higher limits.",
			}
			if writeErr := writeWebSocketJSON(ctx, conn, message); writeErr != nil {
				log.Printf("report metered relay bandwidth exhaustion: %v", writeErr)
			}
			return false
		}
	}
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		log.Printf("write metered relay payload: %v", err)
		return false
	}
	return true
}

func (s *Server) writeCoordinationPayload(ctx context.Context, conn *websocket.Conn, data []byte) bool {
	userID := s.browserUserID(conn)
	if s.Bandwidth != nil {
		if userID == "" {
			return false
		}
		if err := s.Bandwidth.WaitRate(ctx, userID, len(data)); err != nil {
			return false
		}
	}
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		log.Printf("write coordination payload: %v", err)
		return false
	}
	return true
}

func (s *Server) registerTunnelRequest(wingID, requestID string, browser *websocket.Conn, coordination bool) bool {
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
	forBrowser := 0
	for _, pending := range s.tunnelRequests {
		if pending.Browser == browser {
			forBrowser++
		}
	}
	key := tunnelRequestKey{WingID: wingID, RequestID: requestID}
	if _, exists := s.tunnelRequests[key]; exists || len(s.tunnelRequests) >= maxPendingTunnelRequests ||
		forBrowser >= maxPendingTunnelRequestsPerBrowser {
		return false
	}
	s.tunnelRequests[key] = pendingTunnelRequest{Browser: browser, CreatedAt: now, Coordination: coordination}
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
