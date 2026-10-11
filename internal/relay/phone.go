package relay

import (
	"context"
	"encoding/json"
	"github.com/coder/websocket"
	"github.com/ehrlich-b/wingthing/internal/ws"
	"net/http"
	"time"
)

// Phone authorization terminates at the wing. This route only forwards bounded
// opaque messages to one routing ID, and accepts no account/device credential.
func (s *Server) handlePhoneWS(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("wing_id")
	if id == "" || len(id) > 100 || r.Header.Get("Authorization") != "" {
		http.Error(w, "invalid route", 400)
		return
	}
	wing := s.findAnyWingByWingID(id)
	if wing == nil && s.Config.FlyMachineID != "" {
		if machine, ok := s.locateWing(id); ok && machine != s.Config.FlyMachineID {
			w.Header().Set("fly-replay", "instance="+machine)
			return
		}
	}
	if wing == nil {
		http.Error(w, "unreachable", 404)
		return
	}
	if !ws.HostedRelayAllowed(wing.HostedRelay) || !s.relayAccess(wing.UserID).Allowed {
		http.Error(w, "relay unavailable", 403)
		return
	}
	// No cross-site browser clients. Native URLSession sends no Origin.
	if r.Header.Get("Origin") != "" {
		http.Error(w, "native phone route", 403)
		return
	}
	conn, e := websocket.Accept(w, r, nil)
	if e != nil {
		return
	}
	defer conn.CloseNow()
	defer s.clearTunnelRequests(conn)
	// Meter transport against the already registered wing owner; this grants no phone authority.
	s.trackBrowser(conn, wing.UserID)
	defer s.untrackBrowser(conn)
	conn.SetReadLimit(260 << 10)
	ctx := r.Context()
	last := time.Now()
	requests := 0
	for {
		readCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		_, data, e := conn.Read(readCtx)
		cancel()
		if e != nil {
			return
		}
		if time.Since(last) > time.Minute {
			last = time.Now()
			requests = 0
		}
		requests++
		if requests > 240 {
			_ = conn.Close(websocket.StatusPolicyViolation, "rate limit")
			return
		}
		var req ws.TunnelRequest
		if json.Unmarshal(data, &req) != nil || req.Type != ws.TypeTunnelRequest || req.Purpose != "phone" || req.WingID != id || len(req.Payload) > 256<<10 || req.SenderPub != "" {
			_ = conn.Close(websocket.StatusPolicyViolation, "invalid phone envelope")
			return
		}
		// Drop ALL sender identity hints, including forged roles and passkeys.
		forwarded := ws.TunnelRequest{Type: ws.TypeTunnelRequest, WingID: id, RequestID: req.RequestID, Purpose: "phone", Payload: req.Payload}
		current := s.findAnyWingByWingID(id)
		if current == nil || current.Conn != wing.Conn || !ws.HostedRelayAllowed(current.HostedRelay) || !s.relayAccess(wing.UserID).Allowed {
			return
		}
		if !s.registerTunnelRequest(id, req.RequestID, conn, false) {
			return
		}
		writeCtx, writeCancel := context.WithTimeout(ctx, 5*time.Second)
		e = writeWebSocketJSON(writeCtx, wing.Conn, forwarded)
		writeCancel()
		if e != nil {
			return
		}
	}
}
