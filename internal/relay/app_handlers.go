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

	"github.com/ehrlich-b/wingthing/internal/ntfy"
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
		"roost_mode":       s.RoostMode,
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
		out = append(out, entry)
	}

	// Include wings from other nodes via wingMap (login only, edges proxy this)
	if s.WingMap != nil {
		for wingID, loc := range s.WingMap.All() {
			if seenWings[wingID] {
				continue
			}
			// Check access: owner OR org member
			if loc.UserID != user.ID {
				if loc.OrgID == "" || s.Store == nil || !s.Store.IsOrgMember(loc.OrgID, user.ID) {
					continue
				}
			}
			seenWings[wingID] = true
			entry := map[string]any{
				"wing_id":        wingID,
				"public_key":     loc.PublicKey,
				"latest_version": latestVer,
				"org_id":         loc.OrgID,
			}
			if loc.MachineID != s.Config.FlyMachineID {
				entry["remote_node"] = loc.MachineID
			}
			out = append(out, entry)
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

	// Resolve org memberships at subscribe time for pub/sub delivery
	var orgIDs []string
	if s.Store != nil {
		orgs, _ := s.Store.ListOrgsForUser(user.ID)
		for _, org := range orgs {
			orgIDs = append(orgIDs, org.ID)
		}
	} else if len(user.OrgIDs) > 0 {
		orgIDs = user.OrgIDs
	}

	ch := make(chan WingEvent, 16)
	s.Wings.Subscribe(user.ID, orgIDs, ch)
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
	// Check wings on other nodes via wingMap
	if s.WingMap != nil {
		if loc, found := s.WingMap.Locate(wingID); found {
			if loc.UserID == userID {
				return loc.OrgID, true
			}
			if loc.OrgID != "" && s.Store != nil {
				role := s.Store.GetOrgMemberRole(loc.OrgID, userID)
				if role == "owner" || role == "admin" {
					return loc.OrgID, true
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

// --- ntfy Push Notifications ---

// GET /api/app/ntfy — returns current ntfy config (topic masked).
func (s *Server) handleNtfyGet(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	cfg, err := s.Store.GetNtfyConfig(user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load config")
		return
	}
	// Mask topic: show first 5 chars + last word, mask middle
	maskedTopic := cfg.Topic
	if len(cfg.Topic) > 10 {
		parts := strings.Split(cfg.Topic, "-")
		if len(parts) >= 3 {
			masked := make([]string, len(parts))
			masked[0] = parts[0]
			for i := 1; i < len(parts)-1; i++ {
				masked[i] = "****"
			}
			masked[len(parts)-1] = parts[len(parts)-1]
			maskedTopic = strings.Join(masked, "-")
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"topic":     maskedTopic,
		"has_token": cfg.Token != "",
		"events":    cfg.Events,
		"enabled":   cfg.Topic != "",
	})
}

// POST /api/app/ntfy — sets ntfy config.
func (s *Server) handleNtfySet(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	var req struct {
		Topic  string `json:"topic"`
		Token  string `json:"token"`
		Events string `json:"events"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Events == "" {
		req.Events = "attention,exit"
	}
	if err := s.Store.SetNtfyConfig(user.ID, NtfyConfig{
		Topic:  req.Topic,
		Token:  req.Token,
		Events: req.Events,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save config")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// POST /api/app/ntfy/test — sends a test notification.
func (s *Server) handleNtfyTest(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	cfg, err := s.Store.GetNtfyConfig(user.ID)
	if err != nil || cfg.Topic == "" {
		writeError(w, http.StatusBadRequest, "ntfy not configured")
		return
	}
	c := ntfy.New(cfg.Topic, cfg.Token, cfg.Events)
	if err := c.SendTest(); err != nil {
		writeError(w, http.StatusBadGateway, "ntfy send failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// POST /api/app/ntfy/generate — generates a BIP39 topic.
func (s *Server) handleNtfyGenerate(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	topic := ntfy.GenerateTopic()
	writeJSON(w, http.StatusOK, map[string]any{"topic": topic})
}
