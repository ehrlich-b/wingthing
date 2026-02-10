package relay

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

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
		projects := wing.Projects
		if projects == nil {
			projects = []ws.WingProject{}
		}
		out[i] = map[string]any{
			"id":         wing.ID,
			"machine_id": wing.MachineID,
			"agents":     wing.Agents,
			"labels":     wing.Labels,
			"public_key": wing.PublicKey,
			"last_seen":  wing.LastSeen,
			"projects":   projects,
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAppSessions returns the user's active PTY and chat sessions.
func (s *Server) handleAppSessions(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}

	var out []map[string]any

	ptySessions := s.PTY.ListForUser(user.ID)
	for _, sess := range ptySessions {
		status := sess.Status
		if status == "" {
			status = "active"
		}
		entry := map[string]any{
			"id":      sess.ID,
			"wing_id": sess.WingID,
			"agent":   sess.Agent,
			"status":  status,
			"kind":    "terminal",
		}
		if sess.CWD != "" {
			entry["cwd"] = sess.CWD
		}
		out = append(out, entry)
	}

	chatSessions := s.Chat.ListForUser(user.ID)
	for _, sess := range chatSessions {
		out = append(out, map[string]any{
			"id":      sess.ID,
			"wing_id": sess.WingID,
			"agent":   sess.Agent,
			"status":  sess.Status,
			"kind":    "chat",
		})
	}

	if out == nil {
		out = make([]map[string]any, 0)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDeleteSession kills or removes a PTY or chat session.
func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}

	sessionID := r.PathValue("id")

	// Try PTY first
	ptySession := s.PTY.Get(sessionID)
	if ptySession != nil && ptySession.UserID == user.ID {
		wing := s.Wings.FindByID(ptySession.WingID)
		if wing != nil {
			kill := ws.PTYKill{Type: ws.TypePTYKill, SessionID: sessionID}
			data, _ := json.Marshal(kill)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			wing.Conn.Write(ctx, websocket.MessageText, data)
			cancel()
		}
		s.PTY.Remove(sessionID)
		log.Printf("pty session %s: deleted via API (user=%s)", sessionID, user.ID)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}

	// Try chat
	chatSession := s.Chat.Get(sessionID)
	if chatSession != nil && chatSession.UserID == user.ID {
		wing := s.Wings.FindByID(chatSession.WingID)
		if wing != nil {
			del := ws.ChatDelete{Type: ws.TypeChatDelete, SessionID: sessionID}
			data, _ := json.Marshal(del)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			wing.Conn.Write(ctx, websocket.MessageText, data)
			cancel()
		}
		s.Chat.Remove(sessionID)
		log.Printf("chat session %s: deleted via API (user=%s)", sessionID, user.ID)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}

	writeError(w, http.StatusNotFound, "session not found")
}

// handleWingUpdate sends a wing.update command to a connected wing.
func (s *Server) handleWingUpdate(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}

	wingID := r.PathValue("wingID")
	wing := s.Wings.FindByID(wingID)
	if wing == nil || wing.UserID != user.ID {
		writeError(w, http.StatusNotFound, "wing not found")
		return
	}

	msg := ws.WingUpdate{Type: ws.TypeWingUpdate}
	data, _ := json.Marshal(msg)
	writeCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := wing.Conn.Write(writeCtx, websocket.MessageText, data); err != nil {
		writeError(w, http.StatusBadGateway, "wing unreachable")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleWingLS proxies a directory listing request to a connected wing.
func (s *Server) handleWingLS(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}

	wingID := r.PathValue("wingID")
	path := r.URL.Query().Get("path")

	wing := s.Wings.FindByID(wingID)
	if wing == nil || wing.UserID != user.ID {
		writeError(w, http.StatusNotFound, "wing not found")
		return
	}

	reqID := uuid.New().String()[:8]
	ch := make(chan *ws.DirResults, 1)
	s.Wings.RegisterDirRequest(reqID, ch)
	defer s.Wings.UnregisterDirRequest(reqID)

	msg := ws.DirList{Type: ws.TypeDirList, RequestID: reqID, Path: path}
	data, _ := json.Marshal(msg)
	writeCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := wing.Conn.Write(writeCtx, websocket.MessageText, data); err != nil {
		writeError(w, http.StatusBadGateway, "wing unreachable")
		return
	}

	select {
	case result := <-ch:
		writeJSON(w, http.StatusOK, result.Entries)
	case <-time.After(3 * time.Second):
		writeError(w, http.StatusGatewayTimeout, "timeout")
	case <-r.Context().Done():
	}
}
