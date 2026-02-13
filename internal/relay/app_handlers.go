package relay

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
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
	tier := user.Tier
	if tier == "" {
		tier = "free"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":           user.ID,
		"display_name": user.DisplayName,
		"provider":     user.Provider,
		"avatar_url":   user.AvatarURL,
		"is_pro":       user.IsPro,
		"tier":         tier,
		"email":        user.Email,
	})
}

// handleAppWings returns the user's connected wings.
func (s *Server) handleAppWings(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}

	wings := s.listAccessibleWings(user.ID)
	latestVer := s.getLatestVersion()
	out := make([]map[string]any, len(wings))
	for i, wing := range wings {
		projects := wing.Projects
		if projects == nil {
			projects = []ws.WingProject{}
		}
		out[i] = map[string]any{
			"id":             wing.ID,
			"machine_id":     wing.MachineID,
			"platform":       wing.Platform,
			"version":        wing.Version,
			"agents":         wing.Agents,
			"labels":         wing.Labels,
			"public_key":     wing.PublicKey,
			"last_seen":      wing.LastSeen,
			"projects":       projects,
			"latest_version": latestVer,
			"egg_config":     wing.EggConfig,
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// getLatestVersion returns the latest release version from cache, fetching from GitHub if stale.
func (s *Server) getLatestVersion() string {
	s.latestVersionMu.RLock()
	ver := s.latestVersion
	at := s.latestVersionAt
	s.latestVersionMu.RUnlock()

	if ver != "" && time.Since(at) < time.Hour {
		return ver
	}

	// Fetch in background, return cached (possibly empty) for now
	go s.fetchLatestVersion()
	return ver
}

func (s *Server) fetchLatestVersion() {
	s.latestVersionMu.Lock()
	if s.latestVersion != "" && time.Since(s.latestVersionAt) < time.Hour {
		s.latestVersionMu.Unlock()
		return
	}
	s.latestVersionMu.Unlock()

	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest("GET", "https://api.github.com/repos/ehrlich-b/wingthing/releases/latest", nil)
	if err != nil {
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if err != nil {
		return
	}
	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &release); err != nil || release.TagName == "" {
		return
	}

	ver := release.TagName
	if !strings.HasPrefix(ver, "v") {
		ver = "v" + ver
	}

	s.latestVersionMu.Lock()
	s.latestVersion = ver
	s.latestVersionAt = time.Now()
	s.latestVersionMu.Unlock()
	log.Printf("latest release version: %s", ver)
}

// handleAppSessions returns the user's active PTY and chat sessions.
// On each call, requests a fresh session list from connected wings to ensure accuracy.
func (s *Server) handleAppSessions(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}

	// Request fresh session list from all accessible wings
	s.requestSessionSync(r.Context(), user.ID)

	var out []map[string]any

	ptySessions := s.listAccessiblePTYSessions(user.ID)
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
		if sess.EggConfig != "" {
			entry["egg_config"] = sess.EggConfig
		}
		if sess.NeedsAttention {
			entry["needs_attention"] = true
		}
		out = append(out, entry)
	}

	chatSessions := s.listAccessibleChatSessions(user.ID)
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

// requestSessionSync asks all connected wings for the user to send their session list,
// waits up to 2s for responses, and updates the PTYRegistry.
func (s *Server) requestSessionSync(ctx context.Context, userID string) {
	wings := s.listAccessibleWings(userID)
	if len(wings) == 0 {
		return
	}

	type pending struct {
		reqID string
		ch    chan *ws.SessionsSync
	}
	var reqs []pending

	for _, wing := range wings {
		reqID := uuid.New().String()[:8]
		ch := make(chan *ws.SessionsSync, 1)
		s.Wings.RegisterSessionRequest(reqID, ch)

		msg := ws.SessionsList{Type: ws.TypeSessionsList, RequestID: reqID}
		data, _ := json.Marshal(msg)
		writeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		err := wing.Conn.Write(writeCtx, websocket.MessageText, data)
		cancel()
		if err != nil {
			s.Wings.UnregisterSessionRequest(reqID)
			continue
		}
		reqs = append(reqs, pending{reqID: reqID, ch: ch})
	}

	// Wait for all responses (up to 500ms)
	deadline := time.After(500 * time.Millisecond)
	for _, req := range reqs {
		select {
		case <-req.ch:
			// SyncFromWing already called in handleWingWS when sessions.sync arrived
		case <-deadline:
		case <-ctx.Done():
		}
		s.Wings.UnregisterSessionRequest(req.reqID)
	}
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
	if ptySession != nil && s.canAccessSession(user.ID, ptySession) {
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
	if chatSession != nil {
		canAccess := chatSession.UserID == user.ID
		if !canAccess {
			cWing := s.Wings.FindByID(chatSession.WingID)
			canAccess = cWing != nil && s.canAccessWing(user.ID, cWing)
		}
		if canAccess {
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
	if wing == nil || !s.canAccessWing(user.ID, wing) {
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

// handleWingEggConfig pushes a new egg config to a connected wing.
func (s *Server) handleWingEggConfig(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}

	wingID := r.PathValue("wingID")
	wing := s.Wings.FindByID(wingID)
	if wing == nil || !s.canAccessWing(user.ID, wing) {
		writeError(w, http.StatusNotFound, "wing not found")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	yamlStr := string(body)

	// Push to wing via WebSocket
	msg := ws.EggConfigUpdate{Type: ws.TypeEggConfigUpdate, YAML: yamlStr}
	data, _ := json.Marshal(msg)
	writeCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := wing.Conn.Write(writeCtx, websocket.MessageText, data); err != nil {
		writeError(w, http.StatusBadGateway, "wing unreachable")
		return
	}

	// Update cached config on the ConnectedWing
	wing.EggConfig = yamlStr

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAppWS is a dashboard WebSocket that pushes wing.online/wing.offline events.
func (s *Server) handleAppWS(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	defer conn.CloseNow()

	ch := make(chan WingEvent, 16)
	s.Wings.Subscribe(user.ID, ch)
	defer s.Wings.Unsubscribe(user.ID, ch)

	ctx := conn.CloseRead(r.Context())
	for {
		select {
		case ev := <-ch:
			data, _ := json.Marshal(ev)
			writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := conn.Write(writeCtx, websocket.MessageText, data)
			cancel()
			if err != nil {
				return
			}
		case <-ctx.Done():
			return
		}
	}
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
	if wing == nil || !s.canAccessWing(user.ID, wing) {
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

// handleAppUsage returns the user's current bandwidth usage and tier info.
func (s *Server) handleAppUsage(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}

	tier := user.Tier
	if tier == "" {
		tier = "free"
	}

	var usageBytes int64
	if s.Bandwidth != nil {
		usageBytes = s.Bandwidth.MonthlyUsage(user.ID)
	}

	out := map[string]any{
		"tier":        tier,
		"usage_bytes": usageBytes,
	}
	if tier == "free" {
		out["cap_bytes"] = freeMonthlyCap
		out["exceeded"] = usageBytes >= freeMonthlyCap
	} else {
		// Pro/team: show usage but no cap
		out["cap_bytes"] = nil
		out["exceeded"] = false
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAppUpgrade sets the user's tier to "pro".
// TODO: integrate Stripe — this is a placeholder that grants pro immediately.
func (s *Server) handleAppUpgrade(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	if err := s.Store.UpdateUserTier(user.ID, "pro"); err != nil {
		writeError(w, http.StatusInternalServerError, "update tier: "+err.Error())
		return
	}
	log.Printf("user %s (%s) upgraded to pro (no billing)", user.ID, user.DisplayName)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tier": "pro"})
}
