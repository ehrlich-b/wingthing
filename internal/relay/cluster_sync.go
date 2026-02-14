package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/ehrlich-b/wingthing/internal/ws"
)

// WingInfo contains summary info about a wing for cluster sync.
type WingInfo struct {
	UserID      string           `json:"user_id"`
	WingID      string           `json:"wing_id,omitempty"`
	Hostname    string           `json:"hostname,omitempty"`
	Platform    string           `json:"platform,omitempty"`
	Version     string           `json:"version,omitempty"`
	Agents      []string         `json:"agents,omitempty"`
	Labels      []string         `json:"labels,omitempty"`
	Projects    []ws.WingProject `json:"projects,omitempty"`
	PublicKey   string           `json:"public_key,omitempty"`
	OrgID       string           `json:"org_id,omitempty"`
	Pinned      bool             `json:"pinned,omitempty"`
	PinnedCount int              `json:"pinned_count,omitempty"`
}

// SyncWing is a wing entry in the cluster sync protocol.
type SyncWing struct {
	WingID string   `json:"wing_id"`
	NodeID string   `json:"node_id"`
	Info   WingInfo `json:"info"`
}

// SyncRequest is sent from edge to login every sync interval.
type SyncRequest struct {
	NodeID    string            `json:"node_id"`
	Wings     []SyncWing        `json:"wings"`
	Bandwidth map[string]int64  `json:"bandwidth,omitempty"` // userID → bytes since last sync
}

// SyncResponse is the full cluster state returned from login.
type SyncResponse struct {
	Wings       []SyncWing `json:"wings"`
	BannedUsers []string   `json:"banned_users,omitempty"`
}

type nodeEntry struct {
	wings    []SyncWing
	lastSync time.Time
}

// ClusterState tracks wings across all nodes (login node only).
// Edges send their full wing list every second; login merges and responds
// with the full cluster state. Dead nodes expire after 10s of silence.
type ClusterState struct {
	mu    sync.Mutex
	nodes map[string]*nodeEntry
}

func NewClusterState() *ClusterState {
	return &ClusterState{nodes: make(map[string]*nodeEntry)}
}

// Sync replaces an edge's wing list, expires dead nodes, and returns:
//   - others: all wings except the requesting edge's (for edge response)
//   - all: all edge wings across all nodes (for login's PeerDirectory)
func (c *ClusterState) Sync(nodeID string, wings []SyncWing) (others, all []SyncWing) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	c.nodes[nodeID] = &nodeEntry{wings: wings, lastSync: now}
	c.expireLocked(now)

	for nid, e := range c.nodes {
		all = append(all, e.wings...)
		if nid != nodeID {
			others = append(others, e.wings...)
		}
	}
	return
}

// All returns all edge wings, expiring dead nodes first.
func (c *ClusterState) All() []SyncWing {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expireLocked(time.Now())
	var all []SyncWing
	for _, e := range c.nodes {
		all = append(all, e.wings...)
	}
	return all
}

func (c *ClusterState) expireLocked(now time.Time) {
	for nid, e := range c.nodes {
		if now.Sub(e.lastSync) > 10*time.Second {
			delete(c.nodes, nid)
		}
	}
}

// connectedToSync converts a ConnectedWing to a SyncWing.
func connectedToSync(nodeID string, w *ConnectedWing) SyncWing {
	return SyncWing{
		WingID: w.ID,
		NodeID: nodeID,
		Info: WingInfo{
			UserID:      w.UserID,
			WingID:      w.WingID,
			Hostname:    w.Hostname,
			Platform:    w.Platform,
			Version:     w.Version,
			Agents:      w.Agents,
			Labels:      w.Labels,
			Projects:    nil, // projects flow through E2E tunnel only
			PublicKey:    w.PublicKey,
			OrgID:       w.OrgID,
			Pinned:      w.Pinned,
			PinnedCount: w.PinnedCount,
		},
	}
}

// syncToPeer converts a SyncWing to a PeerWing.
func syncToPeer(w SyncWing) *PeerWing {
	info := w.Info
	return &PeerWing{
		WingID:    w.WingID,
		MachineID: w.NodeID, // relay machine ID for fly-replay
		NodeID:    w.NodeID,
		WingInfo:  &info,
	}
}

// notifyPeerDiff sends browser events for peer wing changes.
func (s *Server) notifyPeerDiff(added, removed []*PeerWing) {
	for _, w := range added {
		if w.WingInfo != nil {
			s.Wings.notify(w.WingInfo.UserID, WingEvent{
				Type:        "wing.online",
				ConnID:      w.WingID,
				WingID:      w.WingInfo.WingID,
				Hostname:    w.WingInfo.Hostname,
				Platform:    w.WingInfo.Platform,
				Version:     w.WingInfo.Version,
				Agents:      w.WingInfo.Agents,
				Labels:      w.WingInfo.Labels,
				PublicKey:    w.WingInfo.PublicKey,
				Pinned:      w.WingInfo.Pinned,
				PinnedCount: w.WingInfo.PinnedCount,
			})
		}
	}
	for _, w := range removed {
		if w.WingInfo != nil {
			s.Wings.notify(w.WingInfo.UserID, WingEvent{
				Type:      "wing.offline",
				ConnID:    w.WingID,
				WingID:    w.WingInfo.WingID,
			})
		}
	}
}

// rebuildPeersFromCluster updates login's PeerDirectory from cluster state.
func (s *Server) rebuildPeersFromCluster() {
	if s.Peers == nil || s.Cluster == nil {
		return
	}
	all := s.Cluster.All()
	peers := make([]*PeerWing, len(all))
	for i, w := range all {
		peers[i] = syncToPeer(w)
	}
	added, removed := s.Peers.Replace(peers)
	s.notifyPeerDiff(added, removed)
}

// StartClusterExpiry periodically expires dead edge nodes on the login node.
func (s *Server) StartClusterExpiry(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.rebuildPeersFromCluster()
			}
		}
	}()
}

// StartEdgeSync runs the edge-to-login sync loop.
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
	local := s.Wings.All()
	wings := make([]SyncWing, len(local))
	for i, w := range local {
		wings[i] = connectedToSync(s.Config.FlyMachineID, w)
	}

	var bw map[string]int64
	if s.Bandwidth != nil {
		bw = s.Bandwidth.DrainCounters()
	}

	// Re-add drained bandwidth on any failure so bytes aren't lost
	requeue := func() {
		if s.Bandwidth != nil {
			for userID, n := range bw {
				s.Bandwidth.AddUsage(userID, n)
			}
		}
	}

	body, err := json.Marshal(SyncRequest{
		NodeID:    s.Config.FlyMachineID,
		Wings:     wings,
		Bandwidth: bw,
	})
	if err != nil {
		requeue()
		return
	}

	client := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequestWithContext(ctx, "POST",
		loginAddr+"/internal/sync", bytes.NewReader(body))
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

	var syncResp SyncResponse
	if err := json.NewDecoder(resp.Body).Decode(&syncResp); err != nil {
		requeue()
		return
	}

	// Success — apply response
	if s.Peers != nil {
		peers := make([]*PeerWing, len(syncResp.Wings))
		for i, w := range syncResp.Wings {
			peers[i] = syncToPeer(w)
		}
		added, removed := s.Peers.Replace(peers)
		s.notifyPeerDiff(added, removed)
	}

	// Apply bandwidth exceeded list from login
	if s.Bandwidth != nil {
		s.Bandwidth.SetExceeded(syncResp.BannedUsers)
	}
}
