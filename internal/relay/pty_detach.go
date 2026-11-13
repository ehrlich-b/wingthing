package relay

import (
	"context"
	"time"

	"github.com/coder/websocket"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

type browserDetachRoute struct {
	wingID  string
	message ws.PTYDetach
}

// Capture connection-specific IDs before clearing routing. A disconnect queued
// before a takeover can only release its own attachment, even if it reaches
// the wing after the replacement has claimed input.
func (r *PTYRoutes) browserDetachRoutes(conn *websocket.Conn, onlySession string) []browserDetachRoute {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []browserDetachRoute
	for sessionID, route := range r.routes {
		if onlySession != "" && onlySession != sessionID {
			continue
		}
		route.mu.Lock()
		ids := []string{}
		if route.BrowserConn == conn && route.ControllerID != "" {
			ids = append(ids, route.ControllerID)
		}
		if route.PendingController == conn && route.PendingControllerID != "" && route.PendingControllerID != route.ControllerID {
			ids = append(ids, route.PendingControllerID)
		}
		for _, id := range ids {
			result = append(result, browserDetachRoute{route.WingID, ws.PTYDetach{Type: ws.TypePTYDetach, SessionID: sessionID, ControllerID: id}})
		}
		for viewerID, viewer := range route.Viewers {
			if viewer == conn {
				result = append(result, browserDetachRoute{route.WingID, ws.PTYDetach{Type: ws.TypePTYDetach, SessionID: sessionID, ViewerID: viewerID}})
			}
		}
		route.mu.Unlock()
	}
	return result
}

func (s *Server) forwardBrowserDetach(conn *websocket.Conn, onlySession string) {
	s.browserMu.Lock()
	tracked := s.browserConns[conn]
	preview := tracked != nil && tracked.preview
	s.browserMu.Unlock()
	if !preview {
		return
	}
	s.forwardDetachedRoutes(s.PTY.browserDetachRoutes(conn, onlySession))
}

func (s *Server) forwardDetachedRoutes(routes []browserDetachRoute) {
	for _, detached := range routes {
		wing := s.Wings.FindByID(detached.wingID)
		if wing == nil {
			wing = s.findAnyWingByWingID(detached.wingID)
		}
		if wing == nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = writeWingJSON(ctx, wing.Conn, detached.message)
		cancel()
	}
}
