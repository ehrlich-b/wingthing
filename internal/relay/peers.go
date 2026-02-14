package relay

import (
	"context"
	"sync"
	"time"
)

// PeerWing represents a wing connected to a remote node.
type PeerWing struct {
	WingID    string
	MachineID string // Fly machine hosting this wing (for fly-replay)
	NodeID    string
	WingInfo  *WingInfo
}

// PeerDirectory tracks wings on other nodes, updated via full-state sync.
type PeerDirectory struct {
	mu       sync.RWMutex
	wings    map[string]*PeerWing
	updateCh chan struct{}
}

func NewPeerDirectory() *PeerDirectory {
	return &PeerDirectory{
		wings:    make(map[string]*PeerWing),
		updateCh: make(chan struct{}, 1),
	}
}

// Replace swaps the entire wing set and returns the diff.
func (p *PeerDirectory) Replace(wings []*PeerWing) (added, removed []*PeerWing) {
	p.mu.Lock()
	defer p.mu.Unlock()

	newMap := make(map[string]*PeerWing, len(wings))
	for _, w := range wings {
		newMap[w.WingID] = w
	}

	for id, old := range p.wings {
		if _, ok := newMap[id]; !ok {
			removed = append(removed, old)
		}
	}
	for id, w := range newMap {
		if _, ok := p.wings[id]; !ok {
			added = append(added, w)
		}
	}

	p.wings = newMap

	select {
	case p.updateCh <- struct{}{}:
	default:
	}
	return
}

// RemoveWing removes a single wing.
func (p *PeerDirectory) RemoveWing(wingID string) {
	p.mu.Lock()
	delete(p.wings, wingID)
	p.mu.Unlock()
}

// FindWing looks up a wing by ID.
func (p *PeerDirectory) FindWing(wingID string) *PeerWing {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.wings[wingID]
}

// AllWings returns all known remote wings.
func (p *PeerDirectory) AllWings() []*PeerWing {
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := make([]*PeerWing, 0, len(p.wings))
	for _, w := range p.wings {
		result = append(result, w)
	}
	return result
}

// FindByMachineID looks up a peer wing by its hostname (wing machine_id).
func (p *PeerDirectory) FindByMachineID(machineID string) *PeerWing {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, w := range p.wings {
		if w.WingInfo != nil && w.WingInfo.MachineID == machineID {
			return w
		}
	}
	return nil
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

// UpdateCh returns a channel signaled on updates.
func (p *PeerDirectory) UpdateCh() <-chan struct{} {
	return p.updateCh
}

// WaitForWing waits for a wing to appear locally or via sync.
func WaitForWing(ctx context.Context, local *WingRegistry, peers *PeerDirectory, wingID string, timeout time.Duration) (machineID string, found bool) {
	if w := local.FindByID(wingID); w != nil {
		return "", true
	}
	if peers != nil {
		if pw := peers.FindWing(wingID); pw != nil {
			return pw.MachineID, true
		}
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	var peerCh <-chan struct{}
	if peers != nil {
		peerCh = peers.UpdateCh()
	}

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
