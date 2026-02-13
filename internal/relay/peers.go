package relay

import (
	"context"
	"sync"
	"time"
)

// PeerWing represents a wing connected to a remote node.
type PeerWing struct {
	WingID    string
	MachineID string // Fly machine hosting this wing
	NodeID    string
	WingInfo  *WingInfo
}

// PeerDirectory tracks wings on other nodes, updated via gossip.
type PeerDirectory struct {
	mu        sync.RWMutex
	wings     map[string]*PeerWing // wingID → peer (fresh)
	stale     map[string]*PeerWing // stale layer (kept during login restart)
	staleAt   time.Time            // when stale mode was entered
	lastSeq   uint64               // last gossip seq seen from login
	updateCh  chan struct{}         // notified on every delta application
}

func NewPeerDirectory() *PeerDirectory {
	return &PeerDirectory{
		wings:    make(map[string]*PeerWing),
		updateCh: make(chan struct{}, 1),
	}
}

// ApplyDelta applies gossip events from the login node.
func (p *PeerDirectory) ApplyDelta(events []GossipEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, ev := range events {
		switch ev.Event {
		case "online":
			// Remove stale entry for same wing machine (reconnect with new UUID)
			if ev.WingInfo != nil && ev.WingInfo.MachineID != "" {
				for id, pw := range p.wings {
					if pw.WingInfo != nil && pw.WingInfo.MachineID == ev.WingInfo.MachineID {
						delete(p.wings, id)
					}
				}
			}
			p.wings[ev.WingID] = &PeerWing{
				WingID:    ev.WingID,
				MachineID: ev.MachineID,
				NodeID:    ev.NodeID,
				WingInfo:  ev.WingInfo,
			}
		case "offline":
			delete(p.wings, ev.WingID)
		}
	}

	// Notify waiters
	select {
	case p.updateCh <- struct{}{}:
	default:
	}
}

// RemoveWing removes a wing from both fresh and stale layers.
func (p *PeerDirectory) RemoveWing(wingID string) {
	p.mu.Lock()
	delete(p.wings, wingID)
	if p.stale != nil {
		delete(p.stale, wingID)
	}
	p.mu.Unlock()
}

// FindWing looks up a wing, checking fresh layer first then stale.
func (p *PeerDirectory) FindWing(wingID string) *PeerWing {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if w, ok := p.wings[wingID]; ok {
		return w
	}
	if p.stale != nil {
		return p.stale[wingID]
	}
	return nil
}

// AllWings returns all known remote wings for dashboard aggregation.
func (p *PeerDirectory) AllWings() []*PeerWing {
	p.mu.RLock()
	defer p.mu.RUnlock()

	seen := make(map[string]bool)
	var result []*PeerWing
	for _, w := range p.wings {
		result = append(result, w)
		seen[w.WingID] = true
	}
	if p.stale != nil {
		for _, w := range p.stale {
			if !seen[w.WingID] {
				result = append(result, w)
			}
		}
	}
	return result
}

// CountForUser returns the number of peer wings for a given user.
func (p *PeerDirectory) CountForUser(userID string) int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	n := 0
	for _, w := range p.wings {
		if w.WingInfo != nil && w.WingInfo.UserID == userID {
			n++
		}
	}
	return n
}

// LastSeq returns the last seen gossip sequence number.
func (p *PeerDirectory) LastSeq() uint64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.lastSeq
}

// SetLastSeq updates the last seen gossip sequence number.
func (p *PeerDirectory) SetLastSeq(seq uint64) {
	p.mu.Lock()
	p.lastSeq = seq
	p.mu.Unlock()
}

// EnterStaleMode copies current wings to the stale layer (for login restart recovery).
func (p *PeerDirectory) EnterStaleMode() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.stale != nil {
		return // already in stale mode
	}

	p.stale = make(map[string]*PeerWing, len(p.wings))
	for k, v := range p.wings {
		p.stale[k] = v
	}
	p.staleAt = time.Now()
	p.wings = make(map[string]*PeerWing)
}

// ExpireStale drops the stale layer. Called after 30s timeout.
func (p *PeerDirectory) ExpireStale() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stale = nil
}

// StartStaleExpiry runs a background goroutine that expires the stale layer after 30s.
func (p *PeerDirectory) StartStaleExpiry(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.mu.RLock()
				staleAt := p.staleAt
				hasStale := p.stale != nil
				p.mu.RUnlock()

				if hasStale && !staleAt.IsZero() && time.Since(staleAt) > 30*time.Second {
					p.ExpireStale()
				}
			}
		}
	}()
}

// UpdateCh returns a channel that receives a signal when the directory is updated.
func (p *PeerDirectory) UpdateCh() <-chan struct{} {
	return p.updateCh
}

// WaitForWing waits for a wing to appear either locally or via gossip.
// Returns the machine ID if found on a peer, empty string if found locally, or error on timeout.
func WaitForWing(ctx context.Context, local *WingRegistry, peers *PeerDirectory, wingID string, timeout time.Duration) (machineID string, found bool) {
	// Check local first
	if w := local.FindByID(wingID); w != nil {
		return "", true
	}
	// Check peers
	if peers != nil {
		if pw := peers.FindWing(wingID); pw != nil {
			return pw.MachineID, true
		}
	}

	// Wait with timeout
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	// Subscribe to local wing registry events
	localCh := make(chan WingEvent, 4)
	// We use a synthetic userID since we don't know the user
	// This won't get notifications via the normal user-based subscription,
	// so we poll instead
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	var peerCh <-chan struct{}
	if peers != nil {
		peerCh = peers.UpdateCh()
	}

	_ = localCh // suppress unused warning

	for {
		select {
		case <-timer.C:
			return "", false
		case <-ctx.Done():
			return "", false
		case <-ticker.C:
			if w := local.FindByID(wingID); w != nil {
				return "", true
			}
		case <-peerCh:
			if pw := peers.FindWing(wingID); pw != nil {
				return pw.MachineID, true
			}
		}
	}
}
