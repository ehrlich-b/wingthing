package relay

import (
	"encoding/json"
	"net/http"
	"strings"
)

// registerInternalRoutes adds internal API endpoints used for node-to-node communication.
// These should only be accessible on Fly's private network (6PN).
func (s *Server) registerInternalRoutes() {
	s.mux.HandleFunc("GET /internal/status", s.withInternalAuth(s.handleInternalStatus))
	s.mux.HandleFunc("GET /internal/entitlements", s.withInternalAuth(s.handleInternalEntitlements))
	s.mux.HandleFunc("GET /internal/sessions/{token}", s.withInternalAuth(s.handleInternalSession))
	s.mux.HandleFunc("POST /internal/gossip/sync", s.withInternalAuth(s.handleGossipSync))
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

func isPrivateIP(ip string) bool {
	return strings.HasPrefix(ip, "10.") ||
		strings.HasPrefix(ip, "172.16.") || strings.HasPrefix(ip, "172.17.") ||
		strings.HasPrefix(ip, "172.18.") || strings.HasPrefix(ip, "172.19.") ||
		strings.HasPrefix(ip, "172.2") || strings.HasPrefix(ip, "172.3") ||
		strings.HasPrefix(ip, "192.168.") ||
		strings.HasPrefix(ip, "fdaa:") ||
		ip == "127.0.0.1" || ip == "::1"
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

// handleGossipSync handles the gossip sync protocol between login and edge nodes.
// Edge pulls: sends its local events, gets back login's GossipLog delta.
// Login pulls: applies edge events to PeerDirectory, returns GossipLog since edge's lastSeq.
func (s *Server) handleGossipSync(w http.ResponseWriter, r *http.Request) {
	var req GossipSyncRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Apply incoming events (from edge) to our PeerDirectory + notify browser subs
	s.applyGossipAndNotify(req.Events)

	// Login node: return events from GossipLog since edge's last seq
	if s.Gossip != nil {
		events, latestSeq := s.Gossip.Since(req.LatestSeq)
		if events == nil {
			events = []GossipEvent{}
		}
		writeJSON(w, http.StatusOK, GossipSyncResponse{Events: events, LatestSeq: latestSeq})
		return
	}

	// Edge node (shouldn't normally receive sync requests, but handle gracefully)
	var localEvents []GossipEvent
	s.gossipOutMu.Lock()
	localEvents = s.gossipOutbuf
	s.gossipOutbuf = nil
	s.gossipOutMu.Unlock()
	if localEvents == nil {
		localEvents = []GossipEvent{}
	}
	writeJSON(w, http.StatusOK, GossipSyncResponse{Events: localEvents})
}
