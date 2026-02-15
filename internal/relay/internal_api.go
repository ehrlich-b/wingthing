package relay

import (
	"encoding/json"
	"net"
	"net/http"
)

// registerInternalRoutes adds internal API endpoints used for node-to-node communication.
// These should only be accessible on Fly's private network (6PN).
func (s *Server) registerInternalRoutes() {
	s.mux.HandleFunc("GET /internal/status", s.withInternalAuth(s.handleInternalStatus))
	s.mux.HandleFunc("GET /internal/entitlements", s.withInternalAuth(s.handleInternalEntitlements))
	s.mux.HandleFunc("GET /internal/sessions/{token}", s.withInternalAuth(s.handleInternalSession))
	s.mux.HandleFunc("POST /internal/sync", s.withInternalAuth(s.handleSync))
	s.mux.HandleFunc("GET /internal/org-check/{slug}/{userID}", s.withInternalAuth(s.handleInternalOrgCheck))
	s.mux.HandleFunc("POST /internal/wing-event", s.withInternalAuth(s.handleInternalWingEvent))
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
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	Tier        string `json:"tier"`
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

	writeJSON(w, http.StatusOK, SessionValidation{
		UserID:      user.ID,
		DisplayName: user.DisplayName,
		Tier:        tier,
	})
}

// handleSync handles the full-state cluster sync protocol.
// Edges POST their full wing list, login responds with all other wings.
func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	var req SyncRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if s.Cluster == nil {
		writeJSON(w, http.StatusOK, SyncResponse{Wings: []SyncWing{}})
		return
	}

	// Absorb edge bandwidth usage into login's meter for DB persistence
	if s.Bandwidth != nil && len(req.Bandwidth) > 0 {
		for userID, bytes := range req.Bandwidth {
			s.Bandwidth.AddUsage(userID, bytes)
		}
	}

	others, all := s.Cluster.Sync(req.NodeID, req.Wings)

	// Update login's PeerDirectory from full cluster state
	if s.Peers != nil {
		peers := make([]*PeerWing, len(all))
		for i, sw := range all {
			peers[i] = syncToPeer(sw)
		}
		added, removed, changed := s.Peers.Replace(peers)
		s.notifyPeerDiff(added, removed, changed)
	}

	// Add login's own local wings to response
	for _, w := range s.Wings.All() {
		others = append(others, connectedToSync(s.Config.FlyMachineID, w))
	}

	if others == nil {
		others = []SyncWing{}
	}
	var banned []string
	if s.Bandwidth != nil {
		banned = s.Bandwidth.ExceededUsers()
	}
	writeJSON(w, http.StatusOK, SyncResponse{Wings: others, BannedUsers: banned})
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

// handleInternalWingEvent receives a wing config event from an edge node and
// broadcasts it to the owner and org members connected to this (login) node.
func (s *Server) handleInternalWingEvent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Type         string `json:"type"`
		WingID       string `json:"wing_id"`
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

	locked := req.Locked
	allowedCount := req.AllowedCount
	ev := WingEvent{
		Type:         req.Type,
		WingID:       req.WingID,
		PublicKey:    req.PublicKey,
		Locked:       &locked,
		AllowedCount: &allowedCount,
	}

	// Notify owner + org members via pub/sub
	s.Wings.notifyWing(req.UserID, req.OrgID, ev)

	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}
