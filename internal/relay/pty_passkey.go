package relay

import (
	"encoding/json"
	"time"

	"github.com/coder/websocket"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

type ptyPasskeyTarget struct {
	controllerID string
	viewerID     string
}

type ptyPasskeyChallenge struct {
	conn    *websocket.Conn
	expires time.Time
}

// One entry per live attachment, bounded by the route's two controllers and
// viewer cap. Expired, detached and replaced attachments cannot answer later.
func (route *PTYRoute) prunePasskeyChallengesLocked(now time.Time) {
	for target, challenge := range route.passkeyChallenges {
		associated := false
		if target.viewerID != "" {
			associated = route.Viewers[target.viewerID] == challenge.conn
		} else {
			associated = (route.ControllerID == target.controllerID && route.BrowserConn == challenge.conn) ||
				(route.PendingControllerID == target.controllerID && route.PendingController == challenge.conn)
		}
		if !associated || !now.Before(challenge.expires) {
			delete(route.passkeyChallenges, target)
		}
	}
}

func (route *PTYRoute) forgetPasskeySocketLocked(conn *websocket.Conn) {
	for target, challenge := range route.passkeyChallenges {
		if challenge.conn == conn {
			delete(route.passkeyChallenges, target)
		}
	}
}

// A challenge binds the response to the socket selected by the wing's routing
// identity. Browser-supplied controller IDs are never authentication evidence.
func (s *Server) forwardPasskeyChallenge(route *PTYRoute, sessionID string, data []byte) {
	var msg ws.PasskeyChallenge
	if json.Unmarshal(data, &msg) != nil {
		return
	}
	route.mu.Lock()
	now := s.PTY.clockNow()
	route.expirePendingLocked(now)
	route.prunePasskeyChallengesLocked(now)
	target := ptyPasskeyTarget{controllerID: msg.ControllerID, viewerID: msg.ViewerID}
	var conn *websocket.Conn
	if msg.ViewerID != "" {
		target.controllerID = ""
		conn = route.Viewers[msg.ViewerID]
	} else if msg.ControllerID == "" {
		// Older starters omitted their identity. A current starter keeps its
		// challenge even if a new attach arrived before it was forwarded.
		conn, target.controllerID = route.BrowserConn, route.ControllerID
		if conn == nil {
			conn, target.controllerID = route.PendingController, route.PendingControllerID
		}
	} else if msg.ControllerID == route.ControllerID {
		conn = route.BrowserConn
	} else if msg.ControllerID == route.PendingControllerID {
		conn = route.PendingController
	}
	if conn != nil {
		if route.passkeyChallenges == nil {
			route.passkeyChallenges = make(map[ptyPasskeyTarget]ptyPasskeyChallenge)
		}
		route.passkeyChallenges[target] = ptyPasskeyChallenge{conn: conn, expires: now.Add(time.Minute)}
	}
	route.mu.Unlock()
	if conn != nil {
		s.queueRelayPayload(conn, data, sessionID, "")
	}
}

func (r *PTYRoutes) takePasskeyResponse(response *ws.PasskeyResponse, conn *websocket.Conn) (string, bool) {
	route := r.Get(response.SessionID)
	if route == nil {
		return "", false
	}
	route.mu.Lock()
	defer route.mu.Unlock()
	now := r.clockNow()
	route.expirePendingLocked(now)
	route.prunePasskeyChallengesLocked(now)
	for target, challenge := range route.passkeyChallenges {
		if challenge.conn == conn && target.viewerID == response.ViewerID {
			delete(route.passkeyChallenges, target)
			response.ControllerID = target.controllerID
			return route.WingID, route.WingID != ""
		}
	}
	return "", false
}
