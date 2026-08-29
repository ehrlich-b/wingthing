package webrtc

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
)

const (
	defaultMaxPeerConnections          = 128
	defaultMaxPeerConnectionsPerSender = 8
	defaultMaxConcurrentOffers         = 16
	maxSDPOfferBytes                   = 256 << 10
	defaultOfferGatherTimeout          = 15 * time.Second
)

// PeerIdentity holds the relay-injected identity for a sender.
type PeerIdentity struct {
	UserID   string
	Email    string
	OrgRole  string
	Passkeys []string
}

// DCHandler is called as soon as a remote DataChannel is announced, before it
// opens. Identity is the coordinator-authenticated identity captured by this
// exact offer; it must not be looked up later by a reusable sender key because
// several simultaneous offers can share that key. Handlers must install
// OnMessage callbacks synchronously so the first message cannot race setup.
type DCHandler func(senderPub, sessionID string, identity PeerIdentity, dc *webrtc.DataChannel)

// PeerManager manages per-sender WebRTC peer connections.
type PeerManager struct {
	mu         sync.Mutex
	peers      map[string]*webrtc.PeerConnection // senderPub + offer digest → PC
	peerOrder  []string                          // oldest first, for deterministic bounds
	identities map[string]PeerIdentity           // senderPub → identity
	iceServers []webrtc.ICEServer
	dcHandler  DCHandler
	maxPeers   int
	maxPerPeer int
	offerSlots chan struct{}
	offerWait  time.Duration
	closed     bool
}

// NewPeerManager creates a PeerManager with the given ICE servers.
// Pass nil for host-only ICE (same-LAN only).
func NewPeerManager(iceServers []webrtc.ICEServer) *PeerManager {
	return &PeerManager{
		peers:      make(map[string]*webrtc.PeerConnection),
		identities: make(map[string]PeerIdentity),
		iceServers: iceServers,
		maxPeers:   defaultMaxPeerConnections,
		maxPerPeer: defaultMaxPeerConnectionsPerSender,
		offerSlots: make(chan struct{}, defaultMaxConcurrentOffers),
		offerWait:  defaultOfferGatherTimeout,
	}
}

// OnDC registers a callback for new DataChannels.
func (pm *PeerManager) OnDC(handler DCHandler) {
	pm.mu.Lock()
	pm.dcHandler = handler
	pm.mu.Unlock()
}

// HandleOffer processes a WebRTC offer from a browser, creating a PeerConnection
// and returning the answer SDP. Identity is cached from the relay-injected signaling.
func (pm *PeerManager) HandleOffer(senderPub, userID, email, orgRole string, passkeys []string, sdpOffer string) (string, error) {
	if len(sdpOffer) > maxSDPOfferBytes {
		return "", fmt.Errorf("SDP offer exceeds %d bytes", maxSDPOfferBytes)
	}
	pm.mu.Lock()
	closed := pm.closed
	pm.mu.Unlock()
	if closed {
		return "", fmt.Errorf("peer manager is closed")
	}
	select {
	case pm.offerSlots <- struct{}{}:
		defer func() { <-pm.offerSlots }()
	default:
		return "", fmt.Errorf("too many concurrent WebRTC offers")
	}
	identity := PeerIdentity{
		UserID:   userID,
		Email:    email,
		OrgRole:  orgRole,
		Passkeys: append([]string(nil), passkeys...),
	}
	config := webrtc.Configuration{
		ICEServers: pm.iceServers,
	}

	pc, err := webrtc.NewPeerConnection(config)
	if err != nil {
		return "", fmt.Errorf("new peer connection: %w", err)
	}

	// A native installation has one persistent identity key but may run several
	// MCP clients at once. Key connections by offer while retaining identity by
	// sender, so Codex and Claude do not evict one another. Bounds keep repeated
	// authenticated offers from accumulating idle PeerConnections forever.
	offerDigest := sha256.Sum256([]byte(sdpOffer))
	peerKey := fmt.Sprintf("%s\x00%x", senderPub, offerDigest[:12])

	// Handle incoming data channels
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		label := dc.Label()
		// Expect label format "pty:<session_id>"
		sessionID := ""
		if len(label) > 4 && label[:4] == "pty:" {
			sessionID = label[4:]
		}

		pm.mu.Lock()
		handler := pm.dcHandler
		pm.mu.Unlock()
		if handler != nil {
			handler(senderPub, sessionID, identity, dc)
		}

		dc.OnOpen(func() {
			log.Printf("[P2P] data channel %q opened for sender %s", label, logPrefix(senderPub))
		})
	})

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("[P2P] peer %s connection state: %s", logPrefix(senderPub), state.String())
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			pm.mu.Lock()
			if pm.peers[peerKey] == pc {
				pm.removePeerLocked(peerKey)
				if !pm.hasSenderPeerLocked(senderPub) {
					delete(pm.identities, senderPub)
				}
			}
			pm.mu.Unlock()
		}
	})

	// Set remote description
	offer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  sdpOffer,
	}
	if err := pc.SetRemoteDescription(offer); err != nil {
		pm.discardPeer(peerKey, senderPub, pc)
		return "", fmt.Errorf("set remote description: %w", err)
	}

	// Create answer
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		pm.discardPeer(peerKey, senderPub, pc)
		return "", fmt.Errorf("create answer: %w", err)
	}

	// Set local description and wait for ICE gathering to complete
	gatherComplete := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		pm.discardPeer(peerKey, senderPub, pc)
		return "", fmt.Errorf("set local description: %w", err)
	}
	select {
	case <-gatherComplete:
	case <-time.After(pm.offerWait):
		pm.discardPeer(peerKey, senderPub, pc)
		return "", fmt.Errorf("ICE gathering timed out after %s", pm.offerWait)
	}

	// Return the answer SDP with embedded ICE candidates
	localDesc := pc.LocalDescription()
	if localDesc == nil {
		pm.discardPeer(peerKey, senderPub, pc)
		return "", fmt.Errorf("no local description after ICE gathering")
	}
	// Publish only a fully validated offer/answer pair. Installing before SDP
	// validation would let repeated malformed offers consume the bounds and
	// evict healthy peers even though no replacement could ever connect.
	if !pm.installPeer(peerKey, senderPub, identity, pc) {
		return "", fmt.Errorf("peer connection closed before it could be installed")
	}
	return localDesc.SDP, nil
}

func logPrefix(value string) string {
	if len(value) <= 8 {
		return value
	}
	return value[:8]
}

func (pm *PeerManager) discardPeer(peerKey, senderPub string, pc *webrtc.PeerConnection) {
	pm.mu.Lock()
	if pm.peers[peerKey] == pc {
		pm.removePeerLocked(peerKey)
		if !pm.hasSenderPeerLocked(senderPub) {
			delete(pm.identities, senderPub)
		}
	}
	pm.mu.Unlock()
	_ = pc.Close()
}

func (pm *PeerManager) installPeer(peerKey, senderPub string, identity PeerIdentity, pc *webrtc.PeerConnection) bool {
	pm.mu.Lock()
	state := pc.ConnectionState()
	if pm.closed || state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
		pm.mu.Unlock()
		_ = pc.Close()
		return false
	}
	var evicted []*webrtc.PeerConnection
	if old := pm.removePeerLocked(peerKey); old != nil {
		evicted = append(evicted, old)
	}
	for pm.senderPeerCountLocked(senderPub) >= pm.maxPerPeer {
		key := pm.oldestPeerLocked(senderPub)
		if key == "" {
			break
		}
		if old := pm.removePeerLocked(key); old != nil {
			evicted = append(evicted, old)
		}
	}
	for len(pm.peers) >= pm.maxPeers {
		key := pm.oldestPeerLocked("")
		if key == "" {
			break
		}
		oldSender := peerSender(key)
		if old := pm.removePeerLocked(key); old != nil {
			evicted = append(evicted, old)
		}
		if oldSender != senderPub && !pm.hasSenderPeerLocked(oldSender) {
			delete(pm.identities, oldSender)
		}
	}
	pm.peers[peerKey] = pc
	pm.peerOrder = append(pm.peerOrder, peerKey)
	pm.identities[senderPub] = identity
	pm.mu.Unlock()

	// Closing can invoke Pion callbacks; never do it while holding pm.mu.
	for _, old := range evicted {
		_ = old.Close()
	}
	return true
}

func (pm *PeerManager) removePeerLocked(peerKey string) *webrtc.PeerConnection {
	pc := pm.peers[peerKey]
	if pc == nil {
		return nil
	}
	delete(pm.peers, peerKey)
	for index, key := range pm.peerOrder {
		if key == peerKey {
			pm.peerOrder = append(pm.peerOrder[:index], pm.peerOrder[index+1:]...)
			break
		}
	}
	return pc
}

func (pm *PeerManager) senderPeerCountLocked(senderPub string) int {
	count := 0
	for key := range pm.peers {
		if peerSender(key) == senderPub {
			count++
		}
	}
	return count
}

func (pm *PeerManager) oldestPeerLocked(senderPub string) string {
	for _, key := range pm.peerOrder {
		if senderPub == "" || peerSender(key) == senderPub {
			return key
		}
	}
	return ""
}

func peerSender(peerKey string) string {
	if separator := strings.IndexByte(peerKey, 0); separator >= 0 {
		return peerKey[:separator]
	}
	return peerKey
}

// GetPeerIdentity returns the cached identity for a sender.
func (pm *PeerManager) GetPeerIdentity(senderPub string) (PeerIdentity, bool) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	id, ok := pm.identities[senderPub]
	return id, ok
}

// GetDC returns the DataChannel for a given sender and session.
// Returns nil if no matching DC exists.
func (pm *PeerManager) GetDC(senderPub, sessionID string) *webrtc.DataChannel {
	pm.mu.Lock()
	var pc *webrtc.PeerConnection
	for key, candidate := range pm.peers {
		if strings.HasPrefix(key, senderPub+"\x00") {
			pc = candidate
			break
		}
	}
	pm.mu.Unlock()
	if pc == nil {
		return nil
	}
	// DataChannels are browser-created — we can't enumerate them from the Go side.
	// The caller should track DCs via the OnDC callback instead.
	return nil
}

func (pm *PeerManager) hasSenderPeerLocked(senderPub string) bool {
	for key := range pm.peers {
		if strings.HasPrefix(key, senderPub+"\x00") {
			return true
		}
	}
	return false
}

// Close shuts down all peer connections.
func (pm *PeerManager) Close() {
	pm.mu.Lock()
	pm.closed = true
	peers := make(map[string]*webrtc.PeerConnection, len(pm.peers))
	for k, v := range pm.peers {
		peers[k] = v
	}
	pm.peers = make(map[string]*webrtc.PeerConnection)
	pm.peerOrder = nil
	pm.identities = make(map[string]PeerIdentity)
	pm.mu.Unlock()

	for _, pc := range peers {
		_ = pc.Close()
	}
}

// SDPPayload is the JSON structure for webrtc.offer/answer tunnel payloads.
type SDPPayload struct {
	SDP string `json:"sdp"`
}

// MarshalSDP encodes an SDP payload to JSON bytes.
func MarshalSDP(sdp string) []byte {
	data, _ := json.Marshal(SDPPayload{SDP: sdp})
	return data
}
