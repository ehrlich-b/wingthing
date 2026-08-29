package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/ehrlich-b/wingthing/internal/ntfy"
	"github.com/ehrlich-b/wingthing/internal/ws"
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
	if err != nil || !s.roostUserAllowed(user) {
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
	if err != nil || target == nil || !s.roostUserAllowed(target) {
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
		// The SPA may live on a hostname unrelated to the OAuth/login hostname.
		// Advertising the configured base avoids teaching current browsers to
		// guess topology, while the additive field remains safe for old clients.
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error":          "not logged in",
			"login_base_url": strings.TrimRight(s.Config.BaseURL, "/"),
		})
		return
	}
	tier := "free"
	isPro := s.Store.IsUserPro(user.ID)
	if isPro {
		tier = "pro"
	}
	hasPersonalSub := s.Store.HasPersonalSubscription(user.ID)
	creds, _ := s.Store.ListPasskeyCredentials(user.ID)
	relayAccess := s.relayAccess(user.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"id":                 user.ID,
		"display_name":       user.DisplayName,
		"provider":           user.Provider,
		"avatar_url":         user.AvatarURL,
		"is_pro":             tier == "pro",
		"tier":               tier,
		"email":              user.Email,
		"personal_pro":       hasPersonalSub,
		"roost_mode":         s.RoostMode,
		"self_service_plans": s.selfServicePlansEnabled(),
		"has_passkeys":       len(creds) > 0,
		"relay_allowed":      relayAccess.Allowed,
		"relay_reason":       relayAccess.Reason,
		"default_transport":  "direct",
	})
}

// handleAppWings returns the user's connected wings (local + peer).
func (s *Server) handleAppWings(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		// Native wing discovery (wt wings, wt session sync) authenticates with
		// the device token from wt login rather than a web session cookie.
		user = s.tokenUser(r)
	}
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	writeJSON(w, http.StatusOK, s.appWingEntries(user.ID))
}

// appWingEntries is the access-filtered portal wing inventory shared by the
// browser API and the HTTP MCP surface. Callers may decorate a copied entry,
// but must not bypass this method with the unfiltered registries.
func (s *Server) appWingEntries(userID string) []map[string]any {
	wings := s.listAccessibleWings(userID)
	latestVer := s.getLatestVersion()

	// Collect unique owner IDs and resolve display names
	ownerIDs := make(map[string]bool)
	out := make([]map[string]any, 0, len(wings))
	seenWings := make(map[string]bool) // dedup by wing_id
	for _, wing := range wings {
		seenWings[wing.WingID] = true
		ownerIDs[wing.UserID] = true
		entry := map[string]any{
			"wing_id":         wing.WingID,
			"public_key":      wing.PublicKey,
			"latest_version":  latestVer,
			"org_id":          wing.OrgID,
			"user_id":         wing.UserID,
			"locked":          wing.Locked,
			"allowed_count":   wing.AllowedCount,
			"purpose_binding": wing.PurposeBinding,
			"direct_mcp":      wing.DirectMCP,
			"hosted_relay":    effectiveHostedRelay(wing.HostedRelay),
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
			if loc.UserID != userID {
				if loc.OrgID == "" || s.Store == nil || !s.Store.IsOrgMember(loc.OrgID, userID) {
					continue
				}
			}
			seenWings[wingID] = true
			ownerIDs[loc.UserID] = true
			entry := map[string]any{
				"wing_id":         wingID,
				"public_key":      loc.PublicKey,
				"latest_version":  latestVer,
				"org_id":          loc.OrgID,
				"user_id":         loc.UserID,
				"locked":          loc.Locked,
				"allowed_count":   loc.AllowedCount,
				"purpose_binding": loc.PurposeBinding,
				"direct_mcp":      loc.DirectMCP,
				"hosted_relay":    effectiveHostedRelay(loc.HostedRelay),
			}
			if loc.MachineID != s.Config.FlyMachineID {
				entry["remote_node"] = loc.MachineID
			}
			out = append(out, entry)
		}
	}

	// Resolve owner display names
	ownerNames := make(map[string]string)
	if s.Store != nil {
		for uid := range ownerIDs {
			if uid == userID {
				continue
			}
			if u, err := s.Store.GetUserByID(uid); err == nil && u != nil {
				name := u.DisplayName
				if name == "" && u.Email != nil {
					name = *u.Email
				}
				ownerNames[uid] = name
			}
		}
	}
	for _, entry := range out {
		uid, _ := entry["user_id"].(string)
		if name, ok := ownerNames[uid]; ok {
			entry["owner"] = name
		}
	}

	sort.Slice(out, func(i, j int) bool {
		left, _ := out[i]["wing_id"].(string)
		right, _ := out[j]["wing_id"].(string)
		return left < right
	})
	return out
}

func effectiveHostedRelay(policy string) string {
	if ws.HostedRelayAllowed(policy) {
		return ws.HostedRelayAllow
	}
	return ws.HostedRelayDeny
}

const (
	latestVersionFreshFor = time.Hour
	latestVersionRetryIn  = 5 * time.Minute
)

// getLatestVersion returns the latest release version from cache, fetching from
// GitHub if stale. Inventory requests may arrive in bursts, so the stale check
// also reserves the fetch before releasing the mutex. Failed fetches retain the
// last known version and are retried after a short backoff instead of spawning
// one request per inventory call.
func (s *Server) getLatestVersion() string {
	s.latestVersionMu.Lock()
	ver := s.latestVersion
	now := time.Now()
	if ver != "" && now.Sub(s.latestVersionAt) < latestVersionFreshFor {
		s.latestVersionMu.Unlock()
		return ver
	}
	if s.latestVersionFetching || now.Before(s.latestVersionNextFetch) {
		s.latestVersionMu.Unlock()
		return ver
	}
	s.latestVersionFetching = true
	// Reserve the retry window immediately. The completion path updates it from
	// the actual completion time, but this value also protects against a fetch
	// function that blocks for longer than expected.
	s.latestVersionNextFetch = now.Add(latestVersionRetryIn)
	s.latestVersionMu.Unlock()

	// Fetch in background, return cached (possibly empty) for now
	go s.fetchLatestVersion()
	return ver
}

func (s *Server) fetchLatestVersion() {
	fetch := s.latestVersionFetch
	if fetch == nil {
		fetch = fetchLatestGitHubVersion
	}
	ver, err := fetch(context.Background())
	ver = strings.TrimSpace(ver)
	if err == nil && ver == "" {
		err = fmt.Errorf("version fetch returned an empty tag")
	}
	now := time.Now()

	s.latestVersionMu.Lock()
	s.latestVersionFetching = false
	if err != nil {
		s.latestVersionNextFetch = now.Add(latestVersionRetryIn)
		s.latestVersionMu.Unlock()
		return
	}
	if !strings.HasPrefix(ver, "v") {
		ver = "v" + ver
	}
	s.latestVersion = ver
	s.latestVersionAt = now
	s.latestVersionNextFetch = now.Add(latestVersionFreshFor)
	s.latestVersionMu.Unlock()
	log.Printf("latest release version: %s", ver)
}

func fetchLatestGitHubVersion(ctx context.Context) (string, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/ehrlich-b/wingthing/releases/latest", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github release response: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if err != nil {
		return "", err
	}
	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &release); err != nil || release.TagName == "" {
		if err != nil {
			return "", err
		}
		return "", fmt.Errorf("github release response has no tag_name")
	}
	return release.TagName, nil
}

// handleAppWS is a dashboard WebSocket that pushes wing.online/wing.offline events.
func (s *Server) handleAppWS(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := websocket.Accept(w, r, s.browserWebSocketAcceptOptions())
	if err != nil {
		return
	}
	defer func() { _ = conn.CloseNow() }()

	s.trackBrowser(conn, user.ID)
	defer s.untrackBrowser(conn)

	// Resolve org memberships at subscribe time for pub/sub delivery
	var orgIDs []string
	if s.Store != nil {
		orgs, err := s.Store.ListOrgsForUser(user.ID)
		if err != nil {
			log.Printf("app websocket: list user orgs: %v", err)
			return
		}
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
	if !s.selfServicePlansEnabled() {
		writeError(w, http.StatusForbidden, "self-service plan changes are unavailable on this deployment")
		return
	}

	subID := uuid.New().String()
	sub := &Subscription{ID: subID, UserID: &user.ID, Plan: "pro_monthly", Status: "active", Seats: 1}
	ent := &Entitlement{ID: uuid.New().String(), UserID: user.ID, SubscriptionID: subID}
	_, created, err := s.Store.EnsurePersonalSubscription(sub, ent)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "activate subscription: "+err.Error())
		return
	}
	if s.Bandwidth != nil {
		s.Bandwidth.InvalidateUser(user.ID)
	}
	if created {
		log.Printf("user %s (%s) upgraded to pro (no billing)", user.ID, user.DisplayName)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tier": "pro"})
}

// handleAppDowngrade cancels the user's personal subscription.
func (s *Server) handleAppDowngrade(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}

	sub, err := s.Store.GetActivePersonalSubscription(user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load subscription: "+err.Error())
		return
	}
	if sub == nil {
		writeError(w, http.StatusBadRequest, "no active personal subscription")
		return
	}

	tier, err := s.Store.CancelPersonalSubscription(sub.ID, user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cancel subscription: "+err.Error())
		return
	}
	if s.Bandwidth != nil {
		s.Bandwidth.InvalidateUser(user.ID)
	}
	log.Printf("user %s (%s) downgraded (personal sub canceled)", user.ID, user.DisplayName)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tier": tier})
}

// wingLabelScope resolves the owner and scope for a wing label operation.
// Checks both local wings and peer wings (for cross-node labeling on login).
// Returns the organization scope (empty for personal), whether the user owns
// the wing itself, and whether the preflight lookup authorizes the operation.
func (s *Server) wingLabelScope(userID, wingID string) (orgID string, wingOwner, authorized bool) {
	// Check local wings first
	if wing := s.findWingByWingID(userID, wingID); wing != nil {
		if !s.isWingOwner(userID, wing) {
			return "", false, false
		}
		return wing.OrgID, true, true
	}
	// Check wings on other nodes via wingMap
	if s.WingMap != nil {
		if loc, found := s.WingMap.Locate(wingID); found {
			if loc.UserID == userID {
				return loc.OrgID, true, true
			}
			if loc.OrgID != "" && s.Store != nil {
				role := s.Store.GetOrgMemberRole(loc.OrgID, userID)
				if role == "owner" || role == "admin" {
					return loc.OrgID, false, true
				}
			}
		}
	}
	return "", false, false
}

// handleWingLabel sets or updates a label for a wing.
func (s *Server) handleWingLabel(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}

	wingID := r.PathValue("wingID")
	orgID, wingOwner, authorized := s.wingLabelScope(user.ID, wingID)
	if !authorized {
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

	if err := s.Store.SetLabelAuthorized(wingID, scopeType, scopeID, user.ID, wingOwner, body.Label); err != nil {
		if errors.Is(err, ErrOrgMutationUnauthorized) {
			writeError(w, http.StatusNotFound, "wing not found")
			return
		}
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
	orgID, wingOwner, authorized := s.wingLabelScope(user.ID, wingID)
	if !authorized {
		writeError(w, http.StatusNotFound, "wing not found")
		return
	}

	scopeType := "user"
	scopeID := user.ID
	if orgID != "" {
		scopeType = "org"
		scopeID = orgID
	}

	if err := s.Store.DeleteLabelAuthorized(wingID, scopeType, scopeID, user.ID, wingOwner); err != nil {
		if errors.Is(err, ErrOrgMutationUnauthorized) {
			writeError(w, http.StatusNotFound, "wing not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "delete label: "+err.Error())
		return
	}
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
	if req.Topic != "" {
		if _, err := s.newNtfyClient(req.Topic, req.Token, req.Events); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
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
	c, err := s.newNtfyClient(cfg.Topic, cfg.Token, cfg.Events)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := c.SendTest(); err != nil {
		writeError(w, http.StatusBadGateway, "ntfy send failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) newNtfyClient(topic, token, events string) (*ntfy.Client, error) {
	if s.LocalMode || s.RoostMode {
		return ntfy.New(topic, token, events), nil
	}
	return ntfy.NewHosted(topic, token, events)
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
