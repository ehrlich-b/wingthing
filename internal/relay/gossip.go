package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/ehrlich-b/wingthing/internal/ws"
)

// GossipEvent represents a wing online/offline event in the gossip log.
type GossipEvent struct {
	Seq       uint64    `json:"seq,omitempty"`
	NodeID    string    `json:"node_id"`
	MachineID string    `json:"machine_id"` // Fly machine that owns the wing
	WingID    string    `json:"wing_id"`
	Event     string    `json:"event"` // "online" or "offline"
	WingInfo  *WingInfo `json:"wing_info,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// WingInfo contains summary info about a wing for gossip propagation.
type WingInfo struct {
	UserID    string           `json:"user_id"`
	MachineID string           `json:"machine_id,omitempty"` // wing's machine ID (stable across reconnects)
	Platform  string           `json:"platform,omitempty"`
	Version   string           `json:"version,omitempty"`
	Agents    []string         `json:"agents,omitempty"`
	Labels    []string         `json:"labels,omitempty"`
	Projects  []ws.WingProject `json:"projects,omitempty"`
	PublicKey string           `json:"public_key,omitempty"`
	OrgID     string           `json:"org_id,omitempty"`
}

// GossipSyncRequest is sent from login → edge during round-robin.
type GossipSyncRequest struct {
	Events    []GossipEvent `json:"events"`
	LatestSeq uint64        `json:"latest_seq"`
}

// GossipSyncResponse is returned from edge → login with local wing events.
type GossipSyncResponse struct {
	Events    []GossipEvent `json:"events"`
	LatestSeq uint64        `json:"latest_seq,omitempty"`
}

// GossipLog is the sequenced event log maintained by the login node.
type GossipLog struct {
	mu         sync.Mutex
	events     []GossipEvent
	seq        uint64
	checkpoint []GossipEvent // snapshot of current wing state
	checkSeq   uint64        // seq at last checkpoint
}

func NewGossipLog() *GossipLog {
	return &GossipLog{}
}

// Append adds events to the log, assigning sequence numbers.
func (g *GossipLog) Append(events []GossipEvent) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for i := range events {
		g.seq++
		events[i].Seq = g.seq
		if events[i].Timestamp.IsZero() {
			events[i].Timestamp = time.Now()
		}
		g.events = append(g.events, events[i])
	}
}

// Since returns events after the given sequence number.
// If seq < checkSeq, returns the full checkpoint + events since.
func (g *GossipLog) Since(seq uint64) ([]GossipEvent, uint64) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if seq < g.checkSeq && len(g.checkpoint) > 0 {
		result := make([]GossipEvent, len(g.checkpoint))
		copy(result, g.checkpoint)
		for _, e := range g.events {
			if e.Seq > seq {
				result = append(result, e)
			}
		}
		return result, g.seq
	}

	var result []GossipEvent
	for _, e := range g.events {
		if e.Seq > seq {
			result = append(result, e)
		}
	}
	return result, g.seq
}

// LatestSeq returns the current sequence number.
func (g *GossipLog) LatestSeq() uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.seq
}

// Compact snapshots current wing state and truncates old events.
func (g *GossipLog) Compact() {
	g.mu.Lock()
	defer g.mu.Unlock()

	// Build current state from all events
	state := make(map[string]*GossipEvent)
	for i := range g.events {
		e := &g.events[i]
		if e.Event == "online" {
			state[e.WingID] = e
		} else {
			delete(state, e.WingID)
		}
	}

	g.checkpoint = make([]GossipEvent, 0, len(state))
	for _, e := range state {
		g.checkpoint = append(g.checkpoint, *e)
	}
	g.checkSeq = g.seq
	g.events = nil
}

// RecordWingEvent records a local wing event directly into the log (for login node's own wings).
func (g *GossipLog) RecordWingEvent(machineID string, wing *ConnectedWing, event string) {
	ev := GossipEvent{
		NodeID:    machineID,
		MachineID: machineID,
		WingID:    wing.ID,
		Event:     event,
		Timestamp: time.Now(),
	}
	if event == "online" {
		ev.WingInfo = &WingInfo{
			UserID:    wing.UserID,
			MachineID: wing.MachineID,
			Platform:  wing.Platform,
			Version:   wing.Version,
			Agents:    wing.Agents,
			Labels:    wing.Labels,
			Projects:  wing.Projects,
			PublicKey: wing.PublicKey,
			OrgID:     wing.OrgID,
		}
	}
	g.Append([]GossipEvent{ev})
}

// EdgePeer represents a known edge node for gossip round-robin.
type EdgePeer struct {
	MachineID string
	Addr      string // internal HTTP address
	LastSeq   uint64 // last seq we sent to this edge
}

// StartSweep begins the login-driven gossip loop, cycling through all edges.
func (g *GossipLog) StartSweep(ctx context.Context, peers []*EdgePeer, interval time.Duration) {
	if len(peers) == 0 {
		return
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		idx := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				peer := peers[idx%len(peers)]
				g.syncWithEdge(ctx, peer)
				idx++
			}
		}
	}()
}

func (g *GossipLog) syncWithEdge(ctx context.Context, peer *EdgePeer) {
	events, latestSeq := g.Since(peer.LastSeq)

	reqBody := GossipSyncRequest{
		Events:    events,
		LatestSeq: latestSeq,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return
	}

	client := &http.Client{Timeout: 5 * time.Second}
	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		peer.Addr+"/internal/gossip/sync",
		bytes.NewReader(body))
	if err != nil {
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(httpReq)
	if err != nil {
		log.Printf("gossip sync to %s failed: %v", peer.MachineID, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("gossip sync to %s: status %d", peer.MachineID, resp.StatusCode)
		return
	}

	var syncResp GossipSyncResponse
	if err := json.NewDecoder(resp.Body).Decode(&syncResp); err != nil {
		return
	}

	// Append edge events to our log
	if len(syncResp.Events) > 0 {
		g.Append(syncResp.Events)
	}

	peer.LastSeq = latestSeq
}

// applyGossipAndNotify applies gossip events to PeerDirectory and notifies browser subscribers.
// Must look up offline wings BEFORE applying delta (which removes them).
func (s *Server) applyGossipAndNotify(events []GossipEvent) {
	if s.Peers == nil || len(events) == 0 {
		return
	}

	// For offline events, look up userID and machine_id from PeerDirectory before removal
	type offlineInfo struct {
		userID    string
		machineID string
	}
	offlineLookup := make(map[string]offlineInfo) // wingID → info
	for _, ev := range events {
		if ev.Event == "offline" {
			if pw := s.Peers.FindWing(ev.WingID); pw != nil && pw.WingInfo != nil {
				offlineLookup[ev.WingID] = offlineInfo{
					userID:    pw.WingInfo.UserID,
					machineID: pw.WingInfo.MachineID,
				}
			}
		}
	}

	s.Peers.ApplyDelta(events)

	// Notify browser subscribers
	myNodeID := s.Config.FlyMachineID
	for _, ev := range events {
		// Skip self-echo: don't re-notify for events from our own node
		if myNodeID != "" && ev.NodeID == myNodeID {
			continue
		}
		// Skip events for wings we have locally (already notified by WingRegistry)
		if s.Wings.FindByID(ev.WingID) != nil {
			continue
		}
		var userID string
		var wingMachineID string
		if ev.WingInfo != nil {
			userID = ev.WingInfo.UserID
			wingMachineID = ev.WingInfo.MachineID
		}
		if ev.Event == "offline" {
			if info, ok := offlineLookup[ev.WingID]; ok {
				if userID == "" {
					userID = info.userID
				}
				if wingMachineID == "" {
					wingMachineID = info.machineID
				}
			}
		}
		if userID == "" || wingMachineID == "" {
			continue // skip events we can't properly identify
		}
		evType := "wing.online"
		if ev.Event == "offline" {
			evType = "wing.offline"
		}
		we := WingEvent{
			Type:      evType,
			WingID:    ev.WingID,
			MachineID: wingMachineID,
		}
		if ev.WingInfo != nil {
			we.Platform = ev.WingInfo.Platform
			we.Version = ev.WingInfo.Version
			we.Agents = ev.WingInfo.Agents
			we.Labels = ev.WingInfo.Labels
			we.PublicKey = ev.WingInfo.PublicKey
			we.Projects = ev.WingInfo.Projects
		}
		s.Wings.notify(userID, we)
	}
}

// StartEdgeGossipPull runs a loop on edge nodes, polling login for gossip events.
func (s *Server) StartEdgeGossipPull(ctx context.Context, loginAddr string, interval time.Duration) {
	var lastSeq uint64

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.edgeGossipPull(ctx, loginAddr, &lastSeq)
			}
		}
	}()
}

func (s *Server) edgeGossipPull(ctx context.Context, loginAddr string, lastSeq *uint64) {
	// Drain local events
	s.gossipOutMu.Lock()
	localEvents := s.gossipOutbuf
	s.gossipOutbuf = nil
	s.gossipOutMu.Unlock()
	if localEvents == nil {
		localEvents = []GossipEvent{}
	}

	// Re-queue helper for failure paths
	requeue := func() {
		if len(localEvents) > 0 {
			s.gossipOutMu.Lock()
			s.gossipOutbuf = append(localEvents, s.gossipOutbuf...)
			s.gossipOutMu.Unlock()
		}
	}

	reqBody := GossipSyncRequest{
		Events:    localEvents,
		LatestSeq: *lastSeq,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		requeue()
		return
	}

	client := &http.Client{Timeout: 3 * time.Second}
	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		loginAddr+"/internal/gossip/sync",
		bytes.NewReader(body))
	if err != nil {
		requeue()
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(httpReq)
	if err != nil {
		requeue()
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		requeue()
		return
	}

	var syncResp GossipSyncResponse
	if err := json.NewDecoder(resp.Body).Decode(&syncResp); err != nil {
		requeue()
		return
	}

	// Detect login restart (seq reset)
	if s.Peers != nil && syncResp.LatestSeq < *lastSeq {
		s.Peers.EnterStaleMode()
	}

	// Apply login's events to our PeerDirectory + notify browser subs
	s.applyGossipAndNotify(syncResp.Events)

	if s.Peers != nil {
		s.Peers.SetLastSeq(syncResp.LatestSeq)
	}
	*lastSeq = syncResp.LatestSeq
}

// StartCompaction runs periodic compaction of the gossip log.
func (g *GossipLog) StartCompaction(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				g.Compact()
			}
		}
	}()
}
