package relay

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
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
	tier := "free"
	if s.Store.IsUserPro(user.ID) {
		tier = "pro"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":           user.ID,
		"display_name": user.DisplayName,
		"provider":     user.Provider,
		"avatar_url":   user.AvatarURL,
		"is_pro":       tier == "pro",
		"tier":         tier,
		"email":        user.Email,
	})
}

// handleAppWings returns the user's connected wings (local + peer).
func (s *Server) handleAppWings(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}

	wings := s.listAccessibleWings(user.ID)
	latestVer := s.getLatestVersion()

	// Resolve labels for all wings
	var wingIDs []string
	for _, wing := range wings {
		wingIDs = append(wingIDs, wing.WingID)
	}
	// Determine org ID for label resolution
	resolvedOrgID := ""
	if len(wings) > 0 && wings[0].OrgID != "" {
		org, _ := s.Store.GetOrgBySlug(wings[0].OrgID)
		if org != nil {
			resolvedOrgID = org.ID
		}
	}
	wingLabels := s.Store.ResolveLabels(wingIDs, user.ID, resolvedOrgID)

	out := make([]map[string]any, 0, len(wings))
	seenWings := make(map[string]bool) // dedup by wing_id
	for _, wing := range wings {
		seenWings[wing.WingID] = true
		projects := wing.Projects
		if projects == nil {
			projects = []ws.WingProject{}
		}
		entry := map[string]any{
			"id":             wing.ID,
			"wing_id":     wing.WingID,
			"hostname":       wing.Hostname,
			"platform":       wing.Platform,
			"version":        wing.Version,
			"agents":         wing.Agents,
			"labels":         wing.Labels,
			"public_key":     wing.PublicKey,
			"last_seen":      wing.LastSeen,
			"projects":       projects,
			"latest_version": latestVer,
			"egg_config":     wing.EggConfig,
			"org_id":         wing.OrgID,
		}
		if label, ok := wingLabels[wing.WingID]; ok {
			entry["wing_label"] = label
		}
		out = append(out, entry)
	}

	// Include peer wings from cluster sync (dedup by wing_id)
	if s.Peers != nil {
		for _, pw := range s.Peers.AllWings() {
			if pw.WingInfo == nil || pw.WingInfo.UserID != user.ID {
				continue
			}
			peerWingID := pw.WingInfo.WingID
			if peerWingID == "" {
				continue // skip peers without a wing_id
			}
			if seenWings[peerWingID] {
				continue
			}
			seenWings[peerWingID] = true
			var projects []ws.WingProject
			if pw.WingInfo.Projects != nil {
				projects = pw.WingInfo.Projects
			} else {
				projects = []ws.WingProject{}
			}
			out = append(out, map[string]any{
				"id":             pw.WingID,
				"wing_id":     peerWingID,
				"hostname":       pw.WingInfo.Hostname,
				"platform":       pw.WingInfo.Platform,
				"version":        pw.WingInfo.Version,
				"agents":         pw.WingInfo.Agents,
				"labels":         pw.WingInfo.Labels,
				"public_key":     pw.WingInfo.PublicKey,
				"projects":       projects,
				"latest_version": latestVer,
				"remote_node":    pw.MachineID, // Fly machine hosting this wing
			})
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

// requestSessionSyncForWing asks a single wing for its session list and waits for the response.
func (s *Server) requestSessionSyncForWing(ctx context.Context, wing *ConnectedWing) {
	reqID := uuid.New().String()[:8]
	ch := make(chan *ws.SessionsSync, 1)
	s.Wings.RegisterSessionRequest(reqID, ch)
	defer s.Wings.UnregisterSessionRequest(reqID)

	msg := ws.SessionsList{Type: ws.TypeSessionsList, RequestID: reqID}
	data, _ := json.Marshal(msg)
	writeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	err := wing.Conn.Write(writeCtx, websocket.MessageText, data)
	cancel()
	if err != nil {
		return
	}

	select {
	case <-ch:
	case <-time.After(500 * time.Millisecond):
	case <-ctx.Done():
	}
}

// handleDeleteSession kills or removes a PTY or chat session.
// Route: DELETE /api/app/wings/{wingID}/sessions/{id}
func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}

	wingID := r.PathValue("wingID")
	if s.replayToWingEdgeByWingID(w, wingID) {
		return
	}
	sessionID := r.PathValue("id")

	wing := s.findWingByWingID(user.ID, wingID)
	if wing == nil {
		writeError(w, http.StatusNotFound, "wing not found or offline")
		return
	}

	// Try PTY first
	ptySession := s.PTY.Get(sessionID)
	if ptySession != nil && ptySession.WingID == wing.ID {
		kill := ws.PTYKill{Type: ws.TypePTYKill, SessionID: sessionID}
		data, _ := json.Marshal(kill)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		wing.Conn.Write(ctx, websocket.MessageText, data)
		cancel()
		s.PTY.Remove(sessionID)
		log.Printf("pty session %s: deleted via API (user=%s)", sessionID, user.ID)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}

	// Try chat
	chatSession := s.Chat.Get(sessionID)
	if chatSession != nil && chatSession.WingID == wing.ID {
		del := ws.ChatDelete{Type: ws.TypeChatDelete, SessionID: sessionID}
		data, _ := json.Marshal(del)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		wing.Conn.Write(ctx, websocket.MessageText, data)
		cancel()
		s.Chat.Remove(sessionID)
		log.Printf("chat session %s: deleted via API (user=%s)", sessionID, user.ID)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}

	// Session not in local registry — send kill directly to the wing anyway
	// (the wing owns the egg process, it can kill it even if the relay lost track)
	kill := ws.PTYKill{Type: ws.TypePTYKill, SessionID: sessionID}
	data, _ := json.Marshal(kill)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	wing.Conn.Write(ctx, websocket.MessageText, data)
	cancel()
	s.PTY.Remove(sessionID)
	log.Printf("session %s: force-deleted via wing %s (user=%s)", sessionID, wing.ID, user.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleWingUpdate sends a wing.update command to a connected wing.
func (s *Server) handleWingUpdate(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}

	wingID := r.PathValue("wingID")
	if s.replayToWingEdgeByWingID(w, wingID) {
		return
	}
	wing := s.findWingByWingID(user.ID, wingID)
	if wing == nil {
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
	if s.replayToWingEdgeByWingID(w, wingID) {
		return
	}
	wing := s.findWingByWingID(user.ID, wingID)
	if wing == nil {
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

	s.trackBrowser(conn)
	defer s.untrackBrowser(conn)

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
	if s.replayToWingEdgeByWingID(w, wingID) {
		return
	}
	path := r.URL.Query().Get("path")

	wing := s.findWingByWingID(user.ID, wingID)
	if wing == nil {
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

	tier := "free"
	if s.Store.IsUserPro(user.ID) {
		tier = "pro"
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
		out["cap_bytes"] = nil
		out["exceeded"] = false
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAppUpgrade creates a personal subscription + entitlement.
func (s *Server) handleAppUpgrade(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}

	existing, _ := s.Store.GetActivePersonalSubscription(user.ID)
	if existing != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tier": "pro"})
		return
	}

	subID := uuid.New().String()
	sub := &Subscription{ID: subID, UserID: &user.ID, Plan: "pro_monthly", Status: "active", Seats: 1}
	if err := s.Store.CreateSubscription(sub); err != nil {
		writeError(w, http.StatusInternalServerError, "create subscription: "+err.Error())
		return
	}
	if err := s.Store.CreateEntitlement(&Entitlement{ID: uuid.New().String(), UserID: user.ID, SubscriptionID: subID}); err != nil {
		writeError(w, http.StatusInternalServerError, "create entitlement: "+err.Error())
		return
	}

	s.Store.UpdateUserTier(user.ID, "pro")
	if s.Bandwidth != nil {
		s.Bandwidth.InvalidateUser(user.ID)
	}
	log.Printf("user %s (%s) upgraded to pro (no billing)", user.ID, user.DisplayName)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tier": "pro"})
}

// handleAppDowngrade cancels the user's personal subscription.
func (s *Server) handleAppDowngrade(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}

	sub, _ := s.Store.GetActivePersonalSubscription(user.ID)
	if sub == nil {
		writeError(w, http.StatusBadRequest, "no active personal subscription")
		return
	}

	if err := s.Store.UpdateSubscriptionStatus(sub.ID, "canceled"); err != nil {
		writeError(w, http.StatusInternalServerError, "cancel subscription: "+err.Error())
		return
	}
	s.Store.DeleteEntitlementByUserAndSub(user.ID, sub.ID)

	tier := "free"
	if s.Store.IsUserPro(user.ID) {
		tier = "pro"
	}
	s.Store.UpdateUserTier(user.ID, tier)
	if s.Bandwidth != nil {
		s.Bandwidth.InvalidateUser(user.ID)
	}
	log.Printf("user %s (%s) downgraded (personal sub canceled)", user.ID, user.DisplayName)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tier": tier})
}

// handleWingSessions proxies a past-sessions request to the wing via WebSocket.
func (s *Server) handleWingSessions(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}

	wingID := r.PathValue("wingID")
	if s.replayToWingEdgeByWingID(w, wingID) {
		return
	}
	wing := s.findWingByWingID(user.ID, wingID)
	if wing == nil {
		writeError(w, http.StatusNotFound, "wing not found or offline")
		return
	}

	// ?active=true returns live sessions from the wing's PTY/chat registries
	if r.URL.Query().Get("active") == "true" {
		s.requestSessionSyncForWing(r.Context(), wing)

		var out []map[string]any
		for _, sess := range s.listAccessiblePTYSessions(user.ID) {
			if sess.WingID != wing.ID {
				continue
			}
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
		for _, sess := range s.listAccessibleChatSessions(user.ID) {
			if sess.WingID != wing.ID {
				continue
			}
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
		return
	}

	// Default: past session history from wing's disk
	offset := 0
	limit := 20
	if v := r.URL.Query().Get("offset"); v != "" {
		fmt.Sscanf(v, "%d", &offset)
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		fmt.Sscanf(v, "%d", &limit)
	}

	reqID := uuid.New().String()[:8]
	ch := make(chan *ws.SessionsHistoryResults, 1)
	s.Wings.RegisterHistoryRequest(reqID, ch)
	defer s.Wings.UnregisterHistoryRequest(reqID)

	msg := ws.SessionsHistory{
		Type:      ws.TypeSessionsHistory,
		RequestID: reqID,
		Offset:    offset,
		Limit:     limit,
	}
	data, _ := json.Marshal(msg)
	writeCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := wing.Conn.Write(writeCtx, websocket.MessageText, data); err != nil {
		writeError(w, http.StatusBadGateway, "wing unreachable")
		return
	}

	select {
	case result := <-ch:
		writeJSON(w, http.StatusOK, result)
	case <-time.After(5 * time.Second):
		writeError(w, http.StatusGatewayTimeout, "timeout")
	case <-r.Context().Done():
	}
}

// wingLabelScope resolves the owner and scope for a wing label operation.
// Checks both local wings and peer wings (for cross-node labeling on login).
// Returns (orgID, isOwner). Empty orgID means personal scope.
func (s *Server) wingLabelScope(userID, wingID string) (orgID string, isOwner bool) {
	// Check local wings first
	if wing := s.findWingByWingID(userID, wingID); wing != nil {
		if !s.isWingOwner(userID, wing) {
			return "", false
		}
		return wing.OrgID, true
	}
	// Check peer wings (wing connected to a different node)
	if s.Peers != nil {
		if pw := s.Peers.FindByWingID(wingID); pw != nil && pw.WingInfo != nil {
			if pw.WingInfo.UserID == userID {
				return pw.WingInfo.OrgID, true
			}
			// Check org ownership for peer wings
			if pw.WingInfo.OrgID != "" && s.Store != nil {
				org, _ := s.Store.GetOrgBySlug(pw.WingInfo.OrgID)
				if org != nil {
					role := s.Store.GetOrgMemberRole(org.ID, userID)
					if role == "owner" || role == "admin" {
						return pw.WingInfo.OrgID, true
					}
				}
			}
		}
	}
	return "", false
}

// handleWingLabel sets or updates a label for a wing.
func (s *Server) handleWingLabel(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}

	wingID := r.PathValue("wingID")
	orgID, isOwner := s.wingLabelScope(user.ID, wingID)
	if !isOwner {
		writeError(w, http.StatusNotFound, "wing not found")
		return
	}

	var body struct {
		Label string `json:"label"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad body")
		return
	}

	// Determine scope
	scopeType := "user"
	scopeID := user.ID
	if orgID != "" {
		org, _ := s.Store.GetOrgBySlug(orgID)
		if org != nil {
			scopeType = "org"
			scopeID = org.ID
		}
	}

	if err := s.Store.SetLabel(wingID, scopeType, scopeID, body.Label); err != nil {
		writeError(w, http.StatusInternalServerError, "save label: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleDeleteWingLabel removes the label for a wing at current scope.
func (s *Server) handleDeleteWingLabel(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}

	wingID := r.PathValue("wingID")
	orgID, isOwner := s.wingLabelScope(user.ID, wingID)
	if !isOwner {
		writeError(w, http.StatusNotFound, "wing not found")
		return
	}

	scopeType := "user"
	scopeID := user.ID
	if orgID != "" {
		org, _ := s.Store.GetOrgBySlug(orgID)
		if org != nil {
			scopeType = "org"
			scopeID = org.ID
		}
	}

	s.Store.DeleteLabel(wingID, scopeType, scopeID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleSessionLabel sets a label for a session.
func (s *Server) handleSessionLabel(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}

	sessionID := r.PathValue("id")
	var body struct {
		Label string `json:"label"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad body")
		return
	}

	if err := s.Store.SetLabel(sessionID, "user", user.ID, body.Label); err != nil {
		writeError(w, http.StatusInternalServerError, "save label: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAuditSSE streams audit data from wing to browser via SSE.
func (s *Server) handleAuditSSE(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}

	wingID := r.PathValue("wingID")
	if s.replayToWingEdgeByWingID(w, wingID) {
		return
	}
	sessionID := r.PathValue("sessionID")
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = "pty"
	}

	wing := s.findWingByWingID(user.ID, wingID)
	if wing == nil {
		writeError(w, http.StatusNotFound, "wing not found or offline")
		return
	}

	if !s.isWingOwner(user.ID, wing) {
		writeError(w, http.StatusForbidden, "not wing owner")
		return
	}

	reqID := uuid.New().String()[:8]
	ch := make(chan any, 64)
	s.Wings.RegisterAuditRequest(reqID, ch)
	defer s.Wings.UnregisterAuditRequest(reqID)

	msg := ws.AuditRequest{
		Type:      ws.TypeAuditRequest,
		RequestID: reqID,
		SessionID: sessionID,
		Kind:      kind,
	}
	data, _ := json.Marshal(msg)
	writeCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := wing.Conn.Write(writeCtx, websocket.MessageText, data); err != nil {
		writeError(w, http.StatusBadGateway, "wing unreachable")
		return
	}

	// SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Gzip if client supports it
	var out io.Writer = w
	useGzip := strings.Contains(r.Header.Get("Accept-Encoding"), "gzip")
	if useGzip {
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		defer gz.Close()
		out = gz
	}

	var totalBytes int64
	timeout := time.After(60 * time.Second)
	for {
		select {
		case msg := <-ch:
			switch v := msg.(type) {
			case *ws.AuditChunk:
				lines := strings.Split(strings.TrimRight(v.Data, "\n"), "\n")
				for _, line := range lines {
					if line == "" {
						continue
					}
					n, _ := fmt.Fprintf(out, "event: chunk\ndata: %s\n\n", line)
					totalBytes += int64(n)
				}
				if gz, ok := out.(*gzip.Writer); ok {
					gz.Flush()
				}
				flusher.Flush()
			case *ws.AuditDone:
				fmt.Fprintf(out, "event: done\ndata: {}\n\n")
				if gz, ok := out.(*gzip.Writer); ok {
					gz.Flush()
				}
				flusher.Flush()
				if s.Bandwidth != nil && totalBytes > 0 {
					s.Bandwidth.Wait(r.Context(), user.ID, int(totalBytes))
				}
				return
			case *ws.AuditError:
				fmt.Fprintf(out, "event: error\ndata: %s\n\n", v.Error)
				if gz, ok := out.(*gzip.Writer); ok {
					gz.Flush()
				}
				flusher.Flush()
				return
			}
		case <-timeout:
			fmt.Fprintf(out, "event: error\ndata: timeout\n\n")
			if gz, ok := out.(*gzip.Writer); ok {
				gz.Flush()
			}
			flusher.Flush()
			return
		case <-r.Context().Done():
			if s.Bandwidth != nil && totalBytes > 0 {
				s.Bandwidth.Wait(r.Context(), user.ID, int(totalBytes))
			}
			return
		}
	}
}
