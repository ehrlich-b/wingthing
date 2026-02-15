package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"
)

// WingLocation tracks where a wing is connected across the cluster.
type WingLocation struct {
	MachineID    string
	UserID       string
	OrgID        string
	PublicKey    string
	Locked       bool
	AllowedCount int
	RegisteredAt time.Time
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

func (m *WingMap) Register(wingID string, loc WingLocation) {
	loc.RegisteredAt = time.Now()
	m.mu.Lock()
	m.wings[wingID] = loc
	m.edges[loc.MachineID] = time.Now()
	m.mu.Unlock()
}

func (m *WingMap) Deregister(wingID string) {
	m.mu.Lock()
	delete(m.wings, wingID)
	m.mu.Unlock()
}

func (m *WingMap) Locate(wingID string) (WingLocation, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	loc, ok := m.wings[wingID]
	return loc, ok
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
	resp, err := client.Get(s.Config.LoginNodeAddr + "/internal/wing-locate/" + wingID)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	var result struct {
		MachineID string `json:"machine_id"`
		Found     bool   `json:"found"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", false
	}
	return result.MachineID, result.Found
}

// registerWingWithLogin tells login to add a wing to the global map.
func (s *Server) registerWingWithLogin(wing *ConnectedWing) {
	payload, _ := json.Marshal(map[string]any{
		"wing_id":       wing.WingID,
		"machine_id":    s.Config.FlyMachineID,
		"user_id":       wing.UserID,
		"org_id":        wing.OrgID,
		"public_key":    wing.PublicKey,
		"locked":        wing.Locked,
		"allowed_count": wing.AllowedCount,
	})
	client := &http.Client{Timeout: 3 * time.Second}
	req, _ := http.NewRequest("POST", s.Config.LoginNodeAddr+"/internal/wing-register", bytes.NewReader(payload))
	if req == nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("registerWingWithLogin %s: %v", wing.WingID, err)
		return
	}
	if resp.StatusCode != http.StatusOK {
		log.Printf("registerWingWithLogin %s: status %d", wing.WingID, resp.StatusCode)
	}
	resp.Body.Close()
}

// deregisterWingWithLogin tells login to remove a wing from the global map.
func (s *Server) deregisterWingWithLogin(wingID string) {
	payload, _ := json.Marshal(map[string]string{"wing_id": wingID})
	client := &http.Client{Timeout: 3 * time.Second}
	req, _ := http.NewRequest("POST", s.Config.LoginNodeAddr+"/internal/wing-deregister", bytes.NewReader(payload))
	if req == nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("deregisterWingWithLogin %s: %v", wingID, err)
		return
	}
	if resp.StatusCode != http.StatusOK {
		log.Printf("deregisterWingWithLogin %s: status %d", wingID, resp.StatusCode)
	}
	resp.Body.Close()
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
		WingID       string `json:"wing_id"`
		UserID       string `json:"user_id"`
		OrgID        string `json:"org_id"`
		PublicKey    string `json:"public_key"`
		Locked       bool   `json:"locked"`
		AllowedCount int    `json:"allowed_count"`
	}
	wings := make([]syncWing, len(local))
	for i, w := range local {
		wings[i] = syncWing{
			WingID:       w.WingID,
			UserID:       w.UserID,
			OrgID:        w.OrgID,
			PublicKey:    w.PublicKey,
			Locked:       w.Locked,
			AllowedCount: w.AllowedCount,
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
		"machine_id":  s.Config.FlyMachineID,
		"snapshot_at": snapshotAt.Unix(),
		"wings":       wings,
		"bandwidth":   bw,
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

	resp, err := client.Do(req)
	if err != nil {
		requeue()
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		requeue()
		return
	}

	var syncResp struct {
		BannedUsers []string `json:"banned_users"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&syncResp); err != nil {
		requeue()
		return
	}

	if s.Bandwidth != nil {
		s.Bandwidth.SetExceeded(syncResp.BannedUsers)
	}
}
