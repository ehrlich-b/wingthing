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
)

// tokenUser authenticates a request via Bearer token (CLI device auth).
func (s *Server) tokenUser(r *http.Request) *User {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return nil
	}
	token := strings.TrimPrefix(auth, "Bearer ")
	if s.Store == nil {
		return nil
	}
	userID, _, err := s.Store.ValidateToken(token)
	if err != nil {
		return nil
	}
	user, err := s.Store.GetUserByID(userID)
	if err != nil {
		return nil
	}
	return user
}

// handleResolveEmail resolves an email to a user ID.
// GET /api/app/resolve-email?email=...
func (s *Server) handleResolveEmail(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		user = s.tokenUser(r)
	}
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	email := r.URL.Query().Get("email")
	if email == "" {
		writeError(w, http.StatusBadRequest, "email required")
		return
	}
	if s.Store == nil {
		writeError(w, http.StatusInternalServerError, "no store")
		return
	}
	target, err := s.Store.GetUserByEmail(email)
	if err != nil || target == nil {
		writeError(w, http.StatusNotFound, "no user found with email: "+email)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"user_id":      target.ID,
		"display_name": target.DisplayName,
	})
}

// handleAppMe returns the current user's info or 401.
func (s *Server) handleAppMe(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	tier := "free"
	isPro := s.Store.IsUserPro(user.ID)
	if isPro {
		tier = "pro"
	}
	hasPersonalSub := s.Store.HasPersonalSubscription(user.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"id":               user.ID,
		"display_name":     user.DisplayName,
		"provider":         user.Provider,
		"avatar_url":       user.AvatarURL,
		"is_pro":           tier == "pro",
		"tier":             tier,
		"email":            user.Email,
		"personal_pro":     hasPersonalSub,
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
		resolvedOrgID = wings[0].OrgID
	}
	wingLabels := s.Store.ResolveLabels(wingIDs, user.ID, resolvedOrgID)

	out := make([]map[string]any, 0, len(wings))
	seenWings := make(map[string]bool) // dedup by wing_id
	for _, wing := range wings {
		seenWings[wing.WingID] = true
		entry := map[string]any{
			"wing_id":        wing.WingID,
			"public_key":     wing.PublicKey,
			"latest_version": latestVer,
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
			if pw.WingInfo == nil {
				continue
			}
			// Check access: owner OR org member
			if pw.WingInfo.UserID != user.ID {
				if pw.WingInfo.OrgID == "" || s.Store == nil || !s.Store.IsOrgMember(pw.WingInfo.OrgID, user.ID) {
					continue
				}
			}
			peerWingID := pw.WingInfo.WingID
			if peerWingID == "" {
				continue // skip peers without a wing_id
			}
			if seenWings[peerWingID] {
				continue
			}
			seenWings[peerWingID] = true
			out = append(out, map[string]any{
				"wing_id":        peerWingID,
				"public_key":     pw.WingInfo.PublicKey,
				"latest_version": latestVer,
				"org_id":         pw.WingInfo.OrgID,
				"remote_node":    pw.MachineID,
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
				role := s.Store.GetOrgMemberRole(pw.WingInfo.OrgID, userID)
				if role == "owner" || role == "admin" {
					return pw.WingInfo.OrgID, true
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
		scopeType = "org"
		scopeID = orgID
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
		scopeType = "org"
		scopeID = orgID
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

