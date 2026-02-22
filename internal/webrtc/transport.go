package webrtc

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"

	"github.com/pion/webrtc/v4"
)

// WriteFn sends a message over a transport (relay WS or DataChannel).
type WriteFn func(v any) error

// SwappableWriter manages atomic switching between relay WS and DataChannel writes.
// It wraps a relay write function and can be migrated to/from a DataChannel.
type SwappableWriter struct {
	mu         sync.Mutex
	relayWrite WriteFn
	dcWrite    WriteFn
	mode       string // "relay" or "p2p"
}

// NewSwappableWriter creates a SwappableWriter backed by the relay write function.
func NewSwappableWriter(relayWrite WriteFn) *SwappableWriter {
	return &SwappableWriter{
		relayWrite: relayWrite,
		mode:       "relay",
	}
}

// Write sends a message via the current active transport.
// Lock is held through the write call to prevent migration mid-write.
func (sw *SwappableWriter) Write(v any) error {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	w := sw.dcWrite
	if w == nil {
		w = sw.relayWrite
	}
	return w(v)
}

// MigrateToDC atomically switches output to a DataChannel. It sends a pty.migrated
// message via the relay (last WS message for this session) and swaps the write function.
func (sw *SwappableWriter) MigrateToDC(sessionID string, dc *webrtc.DataChannel) error {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	if sw.mode == "p2p" {
		return fmt.Errorf("already migrated to p2p")
	}

	// Send pty.migrated via relay (last relay message for this session)
	migrated := map[string]string{
		"type":       "pty.migrated",
		"session_id": sessionID,
	}
	if err := sw.relayWrite(migrated); err != nil {
		return fmt.Errorf("send pty.migrated: %w", err)
	}

	// Swap to DC write
	sw.dcWrite = func(v any) error {
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		return dc.SendText(string(data))
	}
	sw.mode = "p2p"

	log.Printf("[P2P] session %s MIGRATED — output now on DataChannel", sessionID)
	return nil
}

// FallbackToRelay atomically switches output back to the relay WS.
// Sends a pty.fallback message via relay so the browser knows.
func (sw *SwappableWriter) FallbackToRelay(sessionID string) error {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	if sw.mode == "relay" {
		return nil // already on relay
	}

	sw.dcWrite = nil
	sw.mode = "relay"

	// Send pty.fallback via relay
	fallback := map[string]string{
		"type":       "pty.fallback",
		"session_id": sessionID,
	}
	if err := sw.relayWrite(fallback); err != nil {
		return fmt.Errorf("send pty.fallback: %w", err)
	}

	log.Printf("[P2P] session %s FALLBACK — output back on relay WS", sessionID)
	return nil
}

// Mode returns the current transport mode ("relay" or "p2p").
func (sw *SwappableWriter) Mode() string {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	return sw.mode
}
