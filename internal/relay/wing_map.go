package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// WingLocation tracks where a wing is connected across the cluster.
type WingLocation struct {
	MachineID      string
	ConnectionID   string
	UserID         string
	OrgID          string
	PublicKey      string
	Locked         bool
	AllowedCount   int
	PurposeBinding bool
	DirectMCP      bool
	HostedRelay    string
	GenerationAt   time.Time
	Revision       uint64
	RegisteredAt   time.Time
}

// WingMap is the global registry of all wings, stored on the login node.
type WingMap struct {
	mu    sync.RWMutex
	wings map[string]WingLocation // wing_id → location
	edges map[string]time.Time    // machine_id → last seen
}

func NewWingMap() *WingMap {
	return &WingMap{
		wings: make(map[string]WingLocation),
		edges: make(map[string]time.Time),
	}
}

// Register publishes one real-time wing generation. Current nodes include a
// connection ID, edge-local generation time, and per-connection revision. Use
// those fields to keep a delayed request from an older socket or config update
// from rolling the login-node directory backward. N-1 requests omit the
// connection ID and retain their last-arrival behavior during rolling upgrades.
func (m *WingMap) Register(wingID string, loc WingLocation) bool {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	if current, exists := m.wings[wingID]; exists {
		// A wing ID is chosen by the wing, so a second account that learns one
		// must not be able to evict the live owner merely by connecting through a
		// different edge. Empty owners retain the N-1 compatibility behavior, and
		// an expired edge may be healed by a new authoritative registration.
		if current.MachineID != loc.MachineID && current.UserID != "" && loc.UserID != "" &&
			current.UserID != loc.UserID {
			if seen, alive := m.edges[current.MachineID]; alive && now.Sub(seen) < 30*time.Second {
				return false
			}
		}
		if current.MachineID == loc.MachineID && current.ConnectionID != "" && loc.ConnectionID != "" {
			if current.ConnectionID == loc.ConnectionID {
				if current.Revision > 0 && (loc.Revision == 0 || loc.Revision < current.Revision) {
					return false
				}
			} else if !current.GenerationAt.IsZero() {
				if loc.GenerationAt.IsZero() || !loc.GenerationAt.After(current.GenerationAt) {
					return false
				}
			}
		}
	}
	loc.RegisteredAt = now
	m.wings[wingID] = loc
	m.edges[loc.MachineID] = now
	return true
}

func (m *WingMap) Deregister(wingID string) {
	m.mu.Lock()
	delete(m.wings, wingID)
	m.mu.Unlock()
}

// DeregisterConnection removes a wing only when the event belongs to the
// currently published socket generation. Empty connection IDs preserve the
// legacy unconditional behavior during rolling upgrades.
func (m *WingMap) DeregisterConnection(wingID, connectionID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	loc, ok := m.wings[wingID]
	if !ok || (connectionID != "" && loc.ConnectionID != connectionID) {
		return false
	}
	delete(m.wings, wingID)
	return true
}

func (m *WingMap) Locate(wingID string) (WingLocation, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	loc, ok := m.wings[wingID]
	return loc, ok
}

// IsCurrentEvent checks that a current-node lifecycle event still belongs to
// the directory generation installed by its preceding registration. Legacy
// events omit connectionID and remain accepted during rolling upgrades.
func (m *WingMap) IsCurrentEvent(wingID, machineID, connectionID string, revision uint64, exactRevision bool) bool {
	if connectionID == "" {
		return true
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	current, ok := m.wings[wingID]
	if !ok || current.ConnectionID != connectionID {
		return false
	}
	if machineID != "" && current.MachineID != machineID {
		return false
	}
	if exactRevision && revision > 0 && current.Revision > 0 && current.Revision != revision {
		return false
	}
	return true
}

// ReconcileFull replaces wing state for a machine using the edge's authoritative snapshot.
// Wings registered AFTER snapshotAt are preserved (arrived via real-time event after snapshot).
func (m *WingMap) ReconcileFull(machineID string, activeWings map[string]bool, snapshotAt time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.edges[machineID] = time.Now()
	for wid, loc := range m.wings {
		if loc.MachineID == machineID && !activeWings[wid] && !loc.RegisteredAt.After(snapshotAt) {
			delete(m.wings, wid)
		}
	}
}

// ReconcileSnapshot applies one edge's authoritative snapshot without letting
// it steal a wing that has since registered on another machine or overwrite a
// same-machine connection/config generation established after the snapshot
// began. Current edges provide GenerationAt and Revision from their own clock
// and registry; RegisteredAt remains only as an N-1 compatibility fallback.
func (m *WingMap) ReconcileSnapshot(machineID string, activeWings map[string]WingLocation, snapshotAt time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	m.edges[machineID] = now
	for wingID, incoming := range activeWings {
		current, exists := m.wings[wingID]
		if exists && current.MachineID != machineID {
			// A live edge owns its published generation. If it disappeared without
			// sending an offline event, let another edge's repeated snapshots heal
			// the map once the old edge lease has expired.
			if seen, alive := m.edges[current.MachineID]; alive && now.Sub(seen) < 30*time.Second {
				continue
			}
		}
		if exists && current.MachineID == machineID {
			if current.ConnectionID != "" && incoming.ConnectionID != "" {
				if current.ConnectionID == incoming.ConnectionID {
					// Revisions are comparable only within one connection generation.
					// Once current metadata exists, omission is not a legacy signal:
					// deployed N-1 snapshots omit the connection ID as well.
					if current.Revision > 0 && (incoming.Revision == 0 || current.Revision > incoming.Revision) {
						continue
					}
				} else if !current.GenerationAt.IsZero() {
					// Both timestamps came from this edge, so unlike login receive times
					// they remain comparable even when edge and login clocks differ.
					if incoming.GenerationAt.IsZero() || !incoming.GenerationAt.After(current.GenerationAt) {
						continue
					}
				}
			} else if current.RegisteredAt.After(snapshotAt) {
				// N-1 edges omit generation/revision. Preserve their deployed
				// best-effort timestamp fence during a rolling upgrade.
				continue
			}
		}
		incoming.MachineID = machineID
		if exists && current.ConnectionID == incoming.ConnectionID && current.Revision == incoming.Revision {
			incoming.RegisteredAt = current.RegisteredAt
		} else {
			incoming.RegisteredAt = now
		}
		m.wings[wingID] = incoming
	}
	for wingID, current := range m.wings {
		if current.MachineID == machineID {
			newerGeneration := !current.GenerationAt.IsZero() && current.GenerationAt.After(snapshotAt)
			if current.GenerationAt.IsZero() {
				newerGeneration = current.RegisteredAt.After(snapshotAt)
			}
			if _, active := activeWings[wingID]; !active && !newerGeneration {
				delete(m.wings, wingID)
			}
		}
	}
}

// EdgeIDs returns all known edge machine IDs, expiring dead edges (30s timeout).
func (m *WingMap) EdgeIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	var expired []string
	ids := make([]string, 0, len(m.edges))
	for id, t := range m.edges {
		if now.Sub(t) < 30*time.Second {
			ids = append(ids, id)
		} else {
			expired = append(expired, id)
		}
	}
	for _, eid := range expired {
		delete(m.edges, eid)
		for wid, loc := range m.wings {
			if loc.MachineID == eid {
				delete(m.wings, wid)
			}
		}
	}
	return ids
}

// All returns a snapshot of all wings.
func (m *WingMap) All() map[string]WingLocation {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make(map[string]WingLocation, len(m.wings))
	for id, loc := range m.wings {
		result[id] = loc
	}
	return result
}

// locateWing finds which machine a wing is connected to.
// Login: checks WingMap directly. Edge: asks login via HTTP.
func (s *Server) locateWing(wingID string) (string, bool) {
	if s.WingMap != nil {
		loc, found := s.WingMap.Locate(wingID)
		if found {
			return loc.MachineID, true
		}
		return "", false
	}
	if s.Config.LoginNodeAddr != "" {
		return s.locateWingViaLogin(wingID)
	}
	return "", false
}

// locateWingViaLogin asks the login node where a wing is connected.
func (s *Server) locateWingViaLogin(wingID string) (string, bool) {
	client := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(s.Config.LoginNodeAddr, "/")+"/internal/wing-locate/"+url.PathEscape(wingID), nil)
	if err != nil {
		return "", false
	}
	s.authorizeInternalRequest(req)
	resp, err := client.Do(req)
	if err != nil {
		return "", false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	var result struct {
		MachineID string `json:"machine_id"`
		Found     bool   `json:"found"`
	}
	if err := decodeInternalJSONResponse(resp.Body, &result); err != nil {
		return "", false
	}
	return result.MachineID, result.Found
}

// registerWingWithLogin tells login to add a wing to the global map.
func (s *Server) registerWingWithLogin(wing *ConnectedWing) {
	payload, _ := json.Marshal(map[string]any{
		"wing_id":                wing.WingID,
		"connection_id":          wing.ID,
		"machine_id":             s.Config.FlyMachineID,
		"user_id":                wing.UserID,
		"org_id":                 wing.OrgID,
		"public_key":             wing.PublicKey,
		"locked":                 wing.Locked,
		"allowed_count":          wing.AllowedCount,
		"purpose_binding":        wing.PurposeBinding,
		"direct_mcp":             wing.DirectMCP,
		"hosted_relay":           wing.HostedRelay,
		"connected_at_unix_nano": unixNanoOrZero(wing.ConnectedAt),
		"revision":               wing.Revision,
	})
	client := &http.Client{Timeout: 3 * time.Second}
	req, _ := http.NewRequest("POST", s.Config.LoginNodeAddr+"/internal/wing-register", bytes.NewReader(payload))
	if req == nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	s.authorizeInternalRequest(req)
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("registerWingWithLogin %s: %v", wing.WingID, err)
		return
	}
	if resp.StatusCode != http.StatusOK {
		log.Printf("registerWingWithLogin %s: status %d", wing.WingID, resp.StatusCode)
	}
	if err := resp.Body.Close(); err != nil {
		log.Printf("registerWingWithLogin %s: close response: %v", wing.WingID, err)
	}
}

// StartEdgeSync runs the edge-to-login reconcile loop.
func (s *Server) StartEdgeSync(ctx context.Context, loginAddr string, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.edgeSync(ctx, loginAddr)
			}
		}
	}()
}

func (s *Server) edgeSync(ctx context.Context, loginAddr string) {
	snapshotAt := time.Now()
	local := s.Wings.All()
	type syncWing struct {
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
	}
	wings := make([]syncWing, len(local))
	for i, w := range local {
		wings[i] = syncWing{
			WingID:         w.WingID,
			ConnectionID:   w.ID,
			UserID:         w.UserID,
			OrgID:          w.OrgID,
			PublicKey:      w.PublicKey,
			Locked:         w.Locked,
			AllowedCount:   w.AllowedCount,
			PurposeBinding: w.PurposeBinding,
			DirectMCP:      w.DirectMCP,
			HostedRelay:    w.HostedRelay,
			ConnectedAtNS:  unixNanoOrZero(w.ConnectedAt),
			Revision:       w.Revision,
		}
	}

	var bw map[string]int64
	if s.Bandwidth != nil {
		bw = s.Bandwidth.DrainCounters()
	}
	requeue := func() {
		if s.Bandwidth != nil {
			for userID, n := range bw {
				s.Bandwidth.AddUsage(userID, n)
			}
		}
	}

	body, err := json.Marshal(map[string]any{
		"machine_id":            s.Config.FlyMachineID,
		"snapshot_at":           snapshotAt.Unix(),
		"snapshot_at_unix_nano": snapshotAt.UnixNano(),
		"wings":                 wings,
		"bandwidth":             bw,
	})
	if err != nil {
		requeue()
		return
	}

	client := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequestWithContext(ctx, "POST", loginAddr+"/internal/wing-sync", bytes.NewReader(body))
	if err != nil {
		requeue()
		return
	}
	req.Header.Set("Content-Type", "application/json")
	s.authorizeInternalRequest(req)

	resp, err := client.Do(req)
	if err != nil {
		requeue()
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		requeue()
		return
	}

	var syncResp struct {
		BannedUsers []string `json:"banned_users"`
	}
	if err := decodeInternalJSONResponse(resp.Body, &syncResp); err != nil {
		requeue()
		return
	}

	if s.Bandwidth != nil {
		s.Bandwidth.SetExceeded(syncResp.BannedUsers)
	}
}

func unixNanoOrZero(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UnixNano()
}
