package relay

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"time"
)

// registerInternalRoutes adds internal API endpoints used for node-to-node communication.
// These should only be accessible on Fly's private network (6PN).
func (s *Server) registerInternalRoutes() {
	s.mux.HandleFunc("GET /internal/status", s.withInternalAuth(s.handleInternalStatus))
	s.mux.HandleFunc("GET /internal/entitlements", s.withInternalAuth(s.handleInternalEntitlements))
	s.mux.HandleFunc("GET /internal/sessions/{token}", s.withInternalAuth(s.handleInternalSession))
	s.mux.HandleFunc("POST /internal/wing-register", s.withInternalAuth(s.handleWingRegister))
	s.mux.HandleFunc("POST /internal/wing-deregister", s.withInternalAuth(s.handleWingDeregister))
	s.mux.HandleFunc("GET /internal/wing-locate/{wingID}", s.withInternalAuth(s.handleWingLocate))
	s.mux.HandleFunc("POST /internal/wing-sync", s.withInternalAuth(s.handleWingSync))
	s.mux.HandleFunc("GET /internal/org-check/{slug}/{userID}", s.withInternalAuth(s.handleInternalOrgCheck))
	s.mux.HandleFunc("POST /internal/wing-event", s.withInternalAuth(s.handleInternalWingEvent))
	s.mux.HandleFunc("GET /internal/user-orgs/{userID}", s.withInternalAuth(s.handleInternalUserOrgs))
}

// withInternalAuth wraps a handler to only allow requests from Fly's internal network.
// Checks for the Fly-Forwarded-Port header (only set on 6PN) or a shared secret.
func (s *Server) withInternalAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// On Fly, internal 6PN requests have Fly-Client-IP in private range
		// For local dev, check shared secret header
		if s.Config.NodeRole != "" {
			flyPort := r.Header.Get("Fly-Forwarded-Port")
			secret := r.Header.Get("X-Internal-Secret")
			if flyPort == "" && (s.Config.JWTSecret == "" || secret != s.Config.JWTSecret) {
				// Also allow from private IPs (10.x, fdaa:, 172.16-31, 192.168)
				ip := clientIP(r)
				if !isPrivateIP(ip) {
					http.Error(w, "forbidden", http.StatusForbidden)
					return
				}
			}
		}
		next(w, r)
	}
}

// privateCIDRs are the RFC 1918 / RFC 4193 private address ranges
// plus loopback and Fly.io's 6PN (fdaa::/16).
var privateCIDRs []*net.IPNet

func init() {
	for _, cidr := range []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"127.0.0.0/8",
		"::1/128",
		"fc00::/7",  // IPv6 ULA
		"fdaa::/16", // Fly.io 6PN
	} {
		_, network, _ := net.ParseCIDR(cidr)
		privateCIDRs = append(privateCIDRs, network)
	}
}

func isPrivateIP(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	for _, network := range privateCIDRs {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// handleInternalStatus returns node info and connected wing IDs.
func (s *Server) handleInternalStatus(w http.ResponseWriter, r *http.Request) {
	wings := s.Wings.All()
	wingIDs := make([]string, len(wings))
	for i, w := range wings {
		wingIDs[i] = w.ID
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"machine_id": s.Config.FlyMachineID,
		"region":     s.Config.FlyRegion,
		"role":       s.Config.NodeRole,
		"wings":      wingIDs,
	})
}

// EntitlementEntry is a user's entitlement info for edge node caching.
type EntitlementEntry struct {
	UserID string `json:"user_id"`
	Tier   string `json:"tier"`
}

// handleInternalEntitlements returns all active entitlements (login node only).
func (s *Server) handleInternalEntitlements(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "no store")
		return
	}

	rows, err := s.Store.DB().Query(`
		SELECT DISTINCT e.user_id,
			CASE WHEN e.id IS NOT NULL THEN 'pro' ELSE 'free' END as tier
		FROM users u
		LEFT JOIN entitlements e ON e.user_id = u.id
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	var entries []EntitlementEntry
	for rows.Next() {
		var e EntitlementEntry
		if err := rows.Scan(&e.UserID, &e.Tier); err != nil {
			continue
		}
		entries = append(entries, e)
	}
	if entries == nil {
		entries = []EntitlementEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

// SessionValidation is the response from the session validation endpoint.
type SessionValidation struct {
	UserID      string   `json:"user_id"`
	DisplayName string   `json:"display_name"`
	Tier        string   `json:"tier"`
	OrgIDs      []string `json:"org_ids"`
}

// handleInternalSession validates a session token and returns user info (login node only).
func (s *Server) handleInternalSession(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "no store")
		return
	}

	token := r.PathValue("token")
	user, err := s.Store.GetSession(token)
	if err != nil || user == nil {
		writeError(w, http.StatusUnauthorized, "invalid session")
		return
	}

	tier := "free"
	if s.Store.IsUserPro(user.ID) {
		tier = "pro"
	}

	var orgIDs []string
	orgs, _ := s.Store.ListOrgsForUser(user.ID)
	for _, org := range orgs {
		orgIDs = append(orgIDs, org.ID)
	}
	if orgIDs == nil {
		orgIDs = []string{}
	}

	writeJSON(w, http.StatusOK, SessionValidation{
		UserID:      user.ID,
		DisplayName: user.DisplayName,
		Tier:        tier,
		OrgIDs:      orgIDs,
	})
}

// handleWingRegister adds a wing to the global wingMap (login only).
func (s *Server) handleWingRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WingID       string `json:"wing_id"`
		MachineID    string `json:"machine_id"`
		UserID       string `json:"user_id"`
		OrgID        string `json:"org_id"`
		PublicKey    string `json:"public_key"`
		Locked       bool   `json:"locked"`
		AllowedCount int    `json:"allowed_count"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.WingMap != nil {
		s.WingMap.Register(req.WingID, WingLocation{
			MachineID:    req.MachineID,
			UserID:       req.UserID,
			OrgID:        req.OrgID,
			PublicKey:    req.PublicKey,
			Locked:       req.Locked,
			AllowedCount: req.AllowedCount,
		})
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

// handleWingDeregister removes a wing from the global wingMap (login only).
func (s *Server) handleWingDeregister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WingID string `json:"wing_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.WingMap != nil {
		s.WingMap.Deregister(req.WingID)
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

// handleWingLocate returns the machine hosting a given wing.
func (s *Server) handleWingLocate(w http.ResponseWriter, r *http.Request) {
	wingID := r.PathValue("wingID")
	// Check local wings
	if s.findAnyWingByWingID(wingID) != nil {
		writeJSON(w, http.StatusOK, map[string]any{"machine_id": s.Config.FlyMachineID, "found": true})
		return
	}
	// Check wingMap
	if s.WingMap != nil {
		loc, found := s.WingMap.Locate(wingID)
		if found {
			writeJSON(w, http.StatusOK, map[string]any{"machine_id": loc.MachineID, "found": true})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"found": false})
}

// handleWingSync receives the full wing list from an edge for reconciliation.
func (s *Server) handleWingSync(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MachineID string `json:"machine_id"`
		Wings     []struct {
			WingID       string `json:"wing_id"`
			UserID       string `json:"user_id"`
			OrgID        string `json:"org_id"`
			PublicKey    string `json:"public_key"`
			Locked       bool   `json:"locked"`
			AllowedCount int    `json:"allowed_count"`
		} `json:"wings"`
		Bandwidth map[string]int64 `json:"bandwidth,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Absorb edge bandwidth usage
	if s.Bandwidth != nil && len(req.Bandwidth) > 0 {
		for userID, bytes := range req.Bandwidth {
			s.Bandwidth.AddUsage(userID, bytes)
		}
	}

	if s.WingMap != nil {
		for _, rw := range req.Wings {
			s.WingMap.Register(rw.WingID, WingLocation{
				MachineID:    req.MachineID,
				UserID:       rw.UserID,
				OrgID:        rw.OrgID,
				PublicKey:    rw.PublicKey,
				Locked:       rw.Locked,
				AllowedCount: rw.AllowedCount,
			})
		}
		s.WingMap.Reconcile(req.MachineID)
	}

	var banned []string
	if s.Bandwidth != nil {
		banned = s.Bandwidth.ExceededUsers()
	}
	if banned == nil {
		banned = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"banned_users": banned})
}

// handleInternalOrgCheck validates that a user is an owner/admin of an org.
// Called by edge nodes during wing registration with --org.
func (s *Server) handleInternalOrgCheck(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "no store")
		return
	}

	ref := r.PathValue("slug")
	userID := r.PathValue("userID")

	org, err := s.Store.ResolveOrg(ref, userID)
	if err != nil || org == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false})
		return
	}
	role := s.Store.GetOrgMemberRole(org.ID, userID)
	ok := role == "owner" || role == "admin"
	writeJSON(w, http.StatusOK, map[string]any{"ok": ok, "org_id": org.ID})
}

// handleInternalWingEvent receives a wing event from another node.
// Edge → login: login delivers locally and re-broadcasts to all edges.
// Login → edge: edge delivers locally to its subscribers.
func (s *Server) handleInternalWingEvent(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 8192))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var req struct {
		Type         string `json:"type"`
		WingID       string `json:"wing_id"`
		UserID       string `json:"user_id"`
		OrgID        string `json:"org_id"`
		PublicKey    string `json:"public_key"`
		Locked       bool   `json:"locked"`
		AllowedCount int    `json:"allowed_count"`
		SessionID    string `json:"session_id"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// org.changed: update subscriber org memberships
	if req.Type == "org.changed" {
		if s.IsEdge() && s.Config.LoginNodeAddr != "" {
			go s.refreshRemoteUserOrgs(req.UserID)
		} else {
			s.Wings.notify(req.UserID, WingEvent{Type: "org.changed"})
		}
		writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
		return
	}

	// Wing lifecycle event: deliver to local subscribers
	var ev WingEvent
	switch req.Type {
	case "wing.offline":
		ev = WingEvent{Type: req.Type, WingID: req.WingID}
	case "session.attention":
		ev = WingEvent{Type: req.Type, WingID: req.WingID, SessionID: req.SessionID}
	default:
		locked := req.Locked
		allowedCount := req.AllowedCount
		ev = WingEvent{
			Type:         req.Type,
			WingID:       req.WingID,
			PublicKey:    req.PublicKey,
			Locked:       &locked,
			AllowedCount: &allowedCount,
		}
	}
	s.Wings.notifyWing(req.UserID, req.OrgID, ev)

	// Login: re-broadcast to all edges
	if s.IsLogin() && s.WingMap != nil {
		go s.broadcastToEdges(body)
	}

	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

// handleInternalUserOrgs returns the org IDs for a user (login node only).
func (s *Server) handleInternalUserOrgs(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "no store")
		return
	}
	userID := r.PathValue("userID")
	orgs, _ := s.Store.ListOrgsForUser(userID)
	orgIDs := make([]string, 0, len(orgs))
	for _, org := range orgs {
		orgIDs = append(orgIDs, org.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"org_ids": orgIDs})
}

// refreshRemoteUserOrgs fetches a user's org IDs from the login node and
// updates local subscriber org memberships.
func (s *Server) refreshRemoteUserOrgs(userID string) {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(s.Config.LoginNodeAddr + "/internal/user-orgs/" + userID)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	var result struct {
		OrgIDs []string `json:"org_ids"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return
	}
	if s.Wings.UpdateUserOrgs(userID, result.OrgIDs) {
		s.Wings.notify(userID, WingEvent{Type: "org.changed"})
	}
}
