package webrtc

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"

	"github.com/pion/webrtc/v4"
)

// PeerIdentity holds the relay-injected identity for a sender.
type PeerIdentity struct {
	UserID   string
	Email    string
	OrgRole  string
	Passkeys []string
}

// DCHandler is called when a new DataChannel opens on a peer connection.
type DCHandler func(senderPub, sessionID string, dc *webrtc.DataChannel)

// PeerManager manages per-sender WebRTC peer connections.
type PeerManager struct {
	mu         sync.Mutex
	peers      map[string]*webrtc.PeerConnection // senderPub → PC
	identities map[string]PeerIdentity           // senderPub → identity
	iceServers []webrtc.ICEServer
	dcHandler  DCHandler
}

// NewPeerManager creates a PeerManager with the given ICE servers.
// Pass nil for host-only ICE (same-LAN only).
func NewPeerManager(iceServers []webrtc.ICEServer) *PeerManager {
	return &PeerManager{
		peers:      make(map[string]*webrtc.PeerConnection),
		identities: make(map[string]PeerIdentity),
		iceServers: iceServers,
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
	config := webrtc.Configuration{
		ICEServers: pm.iceServers,
	}

	pc, err := webrtc.NewPeerConnection(config)
	if err != nil {
		return "", fmt.Errorf("new peer connection: %w", err)
	}

	pm.mu.Lock()
	// Close any existing peer connection for this sender
	if old, ok := pm.peers[senderPub]; ok {
		old.Close()
	}
	pm.peers[senderPub] = pc
	pm.identities[senderPub] = PeerIdentity{
		UserID:   userID,
		Email:    email,
		OrgRole:  orgRole,
		Passkeys: passkeys,
	}
	pm.mu.Unlock()

	// Handle incoming data channels
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		label := dc.Label()
		// Expect label format "pty:<session_id>"
		sessionID := ""
		if len(label) > 4 && label[:4] == "pty:" {
			sessionID = label[4:]
		}

		dc.OnOpen(func() {
			log.Printf("[P2P] data channel %q opened for sender %s", label, senderPub[:8])
			pm.mu.Lock()
			handler := pm.dcHandler
			pm.mu.Unlock()
			if handler != nil {
				handler(senderPub, sessionID, dc)
			}
		})
	})

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("[P2P] peer %s connection state: %s", senderPub[:8], state.String())
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			pm.mu.Lock()
			if pm.peers[senderPub] == pc {
				delete(pm.peers, senderPub)
				delete(pm.identities, senderPub)
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
		pc.Close()
		return "", fmt.Errorf("set remote description: %w", err)
	}

	// Create answer
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		pc.Close()
		return "", fmt.Errorf("create answer: %w", err)
	}

	// Set local description and wait for ICE gathering to complete
	gatherComplete := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		pc.Close()
		return "", fmt.Errorf("set local description: %w", err)
	}
	<-gatherComplete

	// Return the answer SDP with embedded ICE candidates
	localDesc := pc.LocalDescription()
	if localDesc == nil {
		pc.Close()
		return "", fmt.Errorf("no local description after ICE gathering")
	}
	return localDesc.SDP, nil
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
	pc, ok := pm.peers[senderPub]
	pm.mu.Unlock()
	if !ok || pc == nil {
		return nil
	}
	// DataChannels are browser-created — we can't enumerate them from the Go side.
	// The caller should track DCs via the OnDC callback instead.
	return nil
}

// Close shuts down all peer connections.
func (pm *PeerManager) Close() {
	pm.mu.Lock()
	peers := make(map[string]*webrtc.PeerConnection, len(pm.peers))
	for k, v := range pm.peers {
		peers[k] = v
	}
	pm.peers = make(map[string]*webrtc.PeerConnection)
	pm.identities = make(map[string]PeerIdentity)
	pm.mu.Unlock()

	for _, pc := range peers {
		pc.Close()
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
