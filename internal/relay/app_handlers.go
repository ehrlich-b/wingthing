package relay

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"github.com/ehrlich-b/wingthing/internal/ws"
)

// handleAppMe returns the current user's info or 401.
func (s *Server) handleAppMe(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":           user.ID,
		"display_name": user.DisplayName,
		"provider":     user.Provider,
		"avatar_url":   user.AvatarURL,
		"is_pro":       user.IsPro,
	})
}

// handleAppWings returns the user's connected wings.
func (s *Server) handleAppWings(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}

	wings := s.Wings.ListForUser(user.ID)
	out := make([]map[string]any, len(wings))
	for i, wing := range wings {
		out[i] = map[string]any{
			"id":         wing.ID,
			"machine_id": wing.MachineID,
			"agents":     wing.Agents,
			"labels":     wing.Labels,
			"public_key": wing.PublicKey,
			"last_seen":  wing.LastSeen,
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAppSessions returns the user's active PTY sessions.
func (s *Server) handleAppSessions(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}

	sessions := s.PTY.ListForUser(user.ID)
	out := make([]map[string]any, len(sessions))
	for i, sess := range sessions {
		status := sess.Status
		if status == "" {
			status = "active"
		}
		out[i] = map[string]any{
			"id":      sess.ID,
			"wing_id": sess.WingID,
			"agent":   sess.Agent,
			"status":  status,
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDeleteSession kills or removes a PTY session.
func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}

	sessionID := r.PathValue("id")
	session := s.PTY.Get(sessionID)
	if session == nil || session.UserID != user.ID {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	// Try to tell the wing to kill the PTY
	wing := s.Wings.FindByID(session.WingID)
	if wing != nil {
		kill := ws.PTYKill{Type: ws.TypePTYKill, SessionID: sessionID}
		data, _ := json.Marshal(kill)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		wing.Conn.Write(ctx, websocket.MessageText, data)
		cancel()
	}

	// Clean up relay-side regardless
	s.PTY.Remove(sessionID)
	log.Printf("pty session %s: deleted via API (user=%s)", sessionID, user.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
