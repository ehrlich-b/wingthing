package relay

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const entitlementDecisionVersionHeader = "X-Wingthing-Entitlement-Version"
const internalSecretHeader = "X-Internal-Secret"

func authorizeInternalRequest(request *http.Request, secret string) {
	if request != nil && secret != "" {
		request.Header.Set(internalSecretHeader, secret)
	}
}

func (s *Server) authorizeInternalRequest(request *http.Request) {
	authorizeInternalRequest(request, s.Config.InternalSecret)
}

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
	s.mux.HandleFunc("GET /internal/wings-debug", s.withInternalAuth(s.handleWingsDebug))
	s.mux.HandleFunc("POST /internal/user-orgs-bulk", s.withInternalAuth(s.handleInternalUserOrgsBulk))
}

// withInternalAuth permits a shared-secret caller everywhere and otherwise only
// direct cluster-private traffic on split Fly nodes. Standalone/self-hosted
// roosts expose the same mux through reverse proxies, so loopback or RFC1918
// source addresses are not an authentication boundary there.
func (s *Server) withInternalAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		provided := r.Header.Get(internalSecretHeader)
		secretOK := s.Config.InternalSecret != "" &&
			subtle.ConstantTimeCompare([]byte(provided), []byte(s.Config.InternalSecret)) == 1
		if secretOK {
			next(w, r)
			return
		}
		flyCluster := s.Config.NodeRole != "" && s.Config.FlyAppName != "" && s.Config.FlyMachineID != ""
		if !flyCluster || !clusterPrivateRequest(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func clusterPrivateRequest(r *http.Request) bool {
	remote := r.RemoteAddr
	if host, _, err := net.SplitHostPort(remote); err == nil {
		remote = host
	}
	if !isPrivateIP(remote) {
		return false
	}
	// Fly overwrites Fly-Client-IP. When it is present, a public value means a
	// public client reached the app through a private proxy hop; do not mistake
	// the proxy's RemoteAddr for an internal caller. X-Forwarded-For is never
	// consulted here because ordinary reverse proxies accept it from clients.
	if forwarded := strings.TrimSpace(r.Header.Get("Fly-Client-IP")); forwarded != "" {
		return isPrivateIP(forwarded)
	}
	return true
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
		wingIDs[i] = w.WingID
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"machine_id": s.Config.FlyMachineID,
		"region":     s.Config.FlyRegion,
		"role":       s.Config.NodeRole,
		"wings":      wingIDs,
	})
}

// handleWingsDebug returns full diagnostic info about connected wings and WingMap state.
func (s *Server) handleWingsDebug(w http.ResponseWriter, r *http.Request) {
	local := s.Wings.All()
	type wingInfo struct {
		ConnID   string `json:"conn_id"`
		WingID   string `json:"wing_id"`
		UserID   string `json:"user_id"`
		OrgID    string `json:"org_id,omitempty"`
		LastSeen string `json:"last_seen"`
	}
	localWings := make([]wingInfo, len(local))
	for i, lw := range local {
		localWings[i] = wingInfo{
			ConnID:   lw.ID,
			WingID:   lw.WingID,
			UserID:   lw.UserID,
			OrgID:    lw.OrgID,
			LastSeen: lw.LastSeen.Format(time.RFC3339),
		}
	}

	result := map[string]any{
		"machine_id":  s.Config.FlyMachineID,
		"region":      s.Config.FlyRegion,
		"role":        s.Config.NodeRole,
		"local_wings": localWings,
	}

	if s.WingMap != nil {
		type mapEntry struct {
			WingID    string `json:"wing_id"`
			MachineID string `json:"machine_id"`
			UserID    string `json:"user_id"`
			OrgID     string `json:"org_id,omitempty"`
		}
		allMap := s.WingMap.All()
		mapEntries := make([]mapEntry, 0, len(allMap))
		for wid, loc := range allMap {
			mapEntries = append(mapEntries, mapEntry{
				WingID:    wid,
				MachineID: loc.MachineID,
				UserID:    loc.UserID,
				OrgID:     loc.OrgID,
			})
		}
		result["wing_map"] = mapEntries
		result["edge_ids"] = s.WingMap.EdgeIDs()
	}

	writeJSON(w, http.StatusOK, result)
}

// EntitlementEntry is a user's entitlement info for edge node caching.
type EntitlementEntry struct {
	UserID       string `json:"user_id"`
	Tier         string `json:"tier"`
	RelayAllowed bool   `json:"relay_allowed"`
	RelayReason  string `json:"relay_reason"`
	Enrolled     bool   `json:"enrolled"`
}

type entitlementRowIterator interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}

func scanEntitlementRows(rows entitlementRowIterator) ([]EntitlementEntry, error) {
	var entries []EntitlementEntry
	for rows.Next() {
		var e EntitlementEntry
		if err := rows.Scan(&e.UserID, &e.Tier); err != nil {
			return nil, fmt.Errorf("scan entitlement user: %w", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate entitlement users: %w", err)
	}
	if entries == nil {
		entries = []EntitlementEntry{}
	}
	return entries, nil
}

// handleInternalEntitlements returns all active entitlements (login node only).
func (s *Server) handleInternalEntitlements(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "no store")
		return
	}

	rows, err := s.Store.DB().Query(`
		SELECT u.id,
			CASE WHEN EXISTS (
				SELECT 1
				FROM entitlements e
				JOIN subscriptions s ON s.id = e.subscription_id
				WHERE e.user_id = u.id AND s.status = 'active'
			) THEN 'pro' ELSE 'free' END as tier
		FROM users u
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	entries, err := scanEntitlementRows(rows)
	if err != nil {
		_ = rows.Close()
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Close the user cursor before relayAccess performs more store queries.
	// The :memory: relay store deliberately uses one SQLite connection; keeping
	// this cursor open would deadlock that supported self-hosted/test setup.
	if err := rows.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, "close entitlement users: "+err.Error())
		return
	}
	for index := range entries {
		e := &entries[index]
		access := s.relayAccess(e.UserID)
		e.RelayAllowed = access.Allowed
		e.RelayReason = access.Reason
		e.Enrolled = s.roostUserIDAllowed(e.UserID)
	}
	// A missing header identifies an N-1 login node whose response contained
	// tiers only. New edges preserve that node's legacy allow-all relay behavior
	// during an edge-first rolling upgrade, but cannot infer new enrollment state.
	w.Header().Set(entitlementDecisionVersionHeader, "2")
	writeJSON(w, http.StatusOK, entries)
}

// SessionValidation is the response from the session validation endpoint.
type SessionValidation struct {
	UserID       string            `json:"user_id"`
	DisplayName  string            `json:"display_name"`
	Email        string            `json:"email,omitempty"`
	Tier         string            `json:"tier"`
	RelayAllowed bool              `json:"relay_allowed"`
	RelayReason  string            `json:"relay_reason"`
	OrgIDs       []string          `json:"org_ids"`
	OrgRoles     map[string]string `json:"org_roles,omitempty"`
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
	if !s.roostUserAllowed(user) {
		writeError(w, http.StatusForbidden, "this account is not enrolled in this roost")
		return
	}

	tier := "free"
	if s.Store.IsUserPro(user.ID) {
		tier = "pro"
	}

	var orgIDs []string
	orgRoles := make(map[string]string)
	orgs, _ := s.Store.ListOrgsForUser(user.ID)
	for _, org := range orgs {
		orgIDs = append(orgIDs, org.ID)
		if role := s.Store.GetOrgMemberRole(org.ID, user.ID); role != "" {
			orgRoles[org.ID] = role
		}
	}
	if orgIDs == nil {
		orgIDs = []string{}
	}

	email := ""
	if user.Email != nil {
		email = *user.Email
	}
	access := s.relayAccess(user.ID)
	writeJSON(w, http.StatusOK, SessionValidation{
		UserID:       user.ID,
		DisplayName:  user.DisplayName,
		Email:        email,
		Tier:         tier,
		RelayAllowed: access.Allowed,
		RelayReason:  access.Reason,
		OrgIDs:       orgIDs,
		OrgRoles:     orgRoles,
	})
}

// handleWingRegister adds a wing to the global wingMap (login only).
func (s *Server) handleWingRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WingID         string `json:"wing_id"`
		ConnectionID   string `json:"connection_id"`
		MachineID      string `json:"machine_id"`
		UserID         string `json:"user_id"`
		OrgID          string `json:"org_id"`
		PublicKey      string `json:"public_key"`
		Locked         bool   `json:"locked"`
		AllowedCount   int    `json:"allowed_count"`
		PurposeBinding bool   `json:"purpose_binding"`
		DirectMCP      bool   `json:"direct_mcp"`
		HostedRelay    string `json:"hosted_relay"`
		ConnectedAtNS  int64  `json:"connected_at_unix_nano,omitempty"`
		Revision       uint64 `json:"revision,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	generationAt := time.Time{}
	if req.ConnectedAtNS != 0 {
		generationAt = time.Unix(0, req.ConnectedAtNS)
	}
	if s.WingMap != nil {
		s.WingMap.Register(req.WingID, WingLocation{
			MachineID:      req.MachineID,
			ConnectionID:   req.ConnectionID,
			UserID:         req.UserID,
			OrgID:          req.OrgID,
			PublicKey:      req.PublicKey,
			Locked:         req.Locked,
			AllowedCount:   req.AllowedCount,
			PurposeBinding: req.PurposeBinding,
			DirectMCP:      req.DirectMCP,
			HostedRelay:    req.HostedRelay,
			GenerationAt:   generationAt,
			Revision:       req.Revision,
		})
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

// handleWingDeregister removes a wing from the global wingMap (login only).
func (s *Server) handleWingDeregister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WingID       string `json:"wing_id"`
		ConnectionID string `json:"connection_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.WingMap != nil {
		s.WingMap.DeregisterConnection(req.WingID, req.ConnectionID)
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
		MachineID    string `json:"machine_id"`
		SnapshotAt   int64  `json:"snapshot_at"`
		SnapshotAtNS int64  `json:"snapshot_at_unix_nano,omitempty"`
		Wings        []struct {
			WingID         string `json:"wing_id"`
			ConnectionID   string `json:"connection_id,omitempty"`
			UserID         string `json:"user_id"`
			OrgID          string `json:"org_id"`
			PublicKey      string `json:"public_key"`
			Locked         bool   `json:"locked"`
			AllowedCount   int    `json:"allowed_count"`
			PurposeBinding bool   `json:"purpose_binding"`
			DirectMCP      bool   `json:"direct_mcp"`
			HostedRelay    string `json:"hosted_relay"`
			ConnectedAtNS  int64  `json:"connected_at_unix_nano,omitempty"`
			Revision       uint64 `json:"revision,omitempty"`
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
		wingLocations := make(map[string]WingLocation, len(req.Wings))
		for _, rw := range req.Wings {
			generationAt := time.Time{}
			if rw.ConnectedAtNS != 0 {
				generationAt = time.Unix(0, rw.ConnectedAtNS)
			}
			wingLocations[rw.WingID] = WingLocation{
				MachineID:      req.MachineID,
				ConnectionID:   rw.ConnectionID,
				UserID:         rw.UserID,
				OrgID:          rw.OrgID,
				PublicKey:      rw.PublicKey,
				Locked:         rw.Locked,
				AllowedCount:   rw.AllowedCount,
				PurposeBinding: rw.PurposeBinding,
				DirectMCP:      rw.DirectMCP,
				HostedRelay:    rw.HostedRelay,
				GenerationAt:   generationAt,
				Revision:       rw.Revision,
			}
		}
		snapshotAt := time.Unix(req.SnapshotAt, 0)
		if req.SnapshotAtNS != 0 {
			snapshotAt = time.Unix(0, req.SnapshotAtNS)
		}
		s.WingMap.ReconcileSnapshot(req.MachineID, wingLocations, snapshotAt)
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
	const maxWingEventBytes = 8192
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWingEventBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(body) > maxWingEventBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "wing event too large")
		return
	}
	var req struct {
		Type           string  `json:"type"`
		WingID         string  `json:"wing_id"`
		ConnectionID   string  `json:"connection_id"`
		MachineID      string  `json:"machine_id"`
		UserID         string  `json:"user_id"`
		OrgID          string  `json:"org_id"`
		PublicKey      string  `json:"public_key"`
		Locked         *bool   `json:"locked"`
		AllowedCount   *int    `json:"allowed_count"`
		PurposeBinding *bool   `json:"purpose_binding"`
		DirectMCP      *bool   `json:"direct_mcp"`
		HostedRelay    *string `json:"hosted_relay"`
		ConnectedAtNS  int64   `json:"connected_at_unix_nano,omitempty"`
		Revision       uint64  `json:"revision,omitempty"`
		SessionID      string  `json:"session_id"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// org.changed: update subscriber org memberships + session cache
	if req.Type == "org.changed" {
		if s.IsEdge() && s.Config.LoginNodeAddr != "" {
			go s.refreshRemoteUserOrgs(req.UserID)
		} else {
			s.Wings.notify(req.UserID, WingEvent{Type: "org.changed"})
		}
		writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
		return
	}

	// A reconnect can register a replacement before the old socket's async
	// offline event reaches the login node. Bind current-node lifecycle events
	// to the socket generation so stale completion cannot hide a live wing.
	// Legacy events omit the generation and retain their N-1 behavior.
	if req.Type == "wing.offline" && req.ConnectionID != "" && s.WingMap != nil {
		if !s.WingMap.DeregisterConnection(req.WingID, req.ConnectionID) {
			writeJSON(w, http.StatusOK, map[string]string{"ok": "true", "ignored": "stale"})
			return
		}
	}

	// The edge normally registers synchronously before forwarding online/config,
	// but the two HTTP requests can fail independently. Make the event
	// self-healing so a successful event both repairs the directory and notifies
	// subscribers. Register's generation/revision fence rejects delayed events.
	if (req.Type == "wing.online" || req.Type == "wing.config") && req.ConnectionID != "" && s.WingMap != nil {
		generationAt := time.Time{}
		if req.ConnectedAtNS != 0 {
			generationAt = time.Unix(0, req.ConnectedAtNS)
		}
		location := WingLocation{
			MachineID:    req.MachineID,
			ConnectionID: req.ConnectionID,
			UserID:       req.UserID,
			OrgID:        req.OrgID,
			PublicKey:    req.PublicKey,
			GenerationAt: generationAt,
			Revision:     req.Revision,
		}
		if req.Locked != nil {
			location.Locked = *req.Locked
		}
		if req.AllowedCount != nil {
			location.AllowedCount = *req.AllowedCount
		}
		if req.PurposeBinding != nil {
			location.PurposeBinding = *req.PurposeBinding
		}
		if req.DirectMCP != nil {
			location.DirectMCP = *req.DirectMCP
		}
		if req.HostedRelay != nil {
			location.HostedRelay = *req.HostedRelay
		}
		if !s.WingMap.Register(req.WingID, location) {
			writeJSON(w, http.StatusOK, map[string]string{"ok": "true", "ignored": "stale"})
			return
		}
	}

	// Current edge nodes register synchronously before forwarding the matching
	// event. Recheck that generation here because a newer connection or config
	// revision may have registered while the event request was in flight. An
	// ignored stale event must not roll browser capability state backward.
	if req.Type != "wing.offline" && req.ConnectionID != "" && s.WingMap != nil {
		exactRevision := req.Type == "wing.online" || req.Type == "wing.config"
		if !s.WingMap.IsCurrentEvent(req.WingID, req.MachineID, req.ConnectionID, req.Revision, exactRevision) {
			writeJSON(w, http.StatusOK, map[string]string{"ok": "true", "ignored": "stale"})
			return
		}
	}

	// Wing lifecycle event: deliver to local subscribers
	var ev WingEvent
	switch req.Type {
	case "wing.offline":
		ev = WingEvent{Type: req.Type, WingID: req.WingID}
	case "session.attention":
		ev = WingEvent{Type: req.Type, WingID: req.WingID, SessionID: req.SessionID}
	default:
		ev = WingEvent{
			Type:           req.Type,
			WingID:         req.WingID,
			PublicKey:      req.PublicKey,
			Locked:         req.Locked,
			AllowedCount:   req.AllowedCount,
			PurposeBinding: req.PurposeBinding,
			DirectMCP:      req.DirectMCP,
			HostedRelay:    req.HostedRelay,
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
	orgRoles := make(map[string]string, len(orgs))
	for _, org := range orgs {
		orgIDs = append(orgIDs, org.ID)
		if role := s.Store.GetOrgMemberRole(org.ID, userID); role != "" {
			orgRoles[org.ID] = role
		}
	}
	email := ""
	if user, _ := s.Store.GetUserByID(userID); user != nil && user.Email != nil {
		email = *user.Email
	}
	// email and org_roles are additive. N-1 edges still decode org_ids, while
	// current edges can attribute bearer-authenticated direct control exactly.
	writeJSON(w, http.StatusOK, map[string]any{"org_ids": orgIDs, "org_roles": orgRoles, "email": email})
}

// handleInternalUserOrgsBulk returns org IDs for multiple users (login node only).
func (s *Server) handleInternalUserOrgsBulk(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "no store")
		return
	}
	var req struct {
		UserIDs []string `json:"user_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result := make(map[string][]string, len(req.UserIDs))
	for _, uid := range req.UserIDs {
		orgs, _ := s.Store.ListOrgsForUser(uid)
		orgIDs := make([]string, 0, len(orgs))
		for _, org := range orgs {
			orgIDs = append(orgIDs, org.ID)
		}
		result[uid] = orgIDs
	}
	writeJSON(w, http.StatusOK, result)
}

// refreshRemoteUserOrgs fetches a user's org IDs from the login node and
// updates local subscriber org memberships.
func (s *Server) refreshRemoteUserOrgs(userID string) {
	result, ok := s.remoteUserOrgContext(context.Background(), userID)
	if !ok {
		return
	}
	if s.Wings.UpdateUserOrgs(userID, result.OrgIDs) {
		s.Wings.notify(userID, WingEvent{Type: "org.changed"})
	}
	// Also update session cache so canAccessWing works on edge
	if s.sessionCache != nil {
		s.sessionCache.UpdateUserOrgContext(userID, result.OrgIDs, result.OrgRoles)
	}
}
