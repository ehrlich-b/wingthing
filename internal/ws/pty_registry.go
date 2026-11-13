package ws

import (
	"context"
	"sync"
	"time"
)

// PTYRegistry belongs to the wing runtime, before any relay connects. The relay
// and direct adapters subscribe to this local input routing; disconnecting
// either transport does not remove an accepted session.
type PTYRegistry struct {
	mu       sync.Mutex
	sessions map[string]*ptyRoute
}

func (r *PTYRegistry) HasPTYSession(id string) bool {
	return r.lookup(id) != nil
}

type ptyRoute struct {
	input   chan []byte
	retired chan struct{}
}

func (r *PTYRegistry) lookup(id string) *ptyRoute {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessions[id]
}

func (r *PTYRegistry) register(id string, input chan []byte) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !ValidSessionID(id) || r.sessions[id] != nil {
		return false
	}
	if r.sessions == nil {
		r.sessions = make(map[string]*ptyRoute)
	}
	r.sessions[id] = &ptyRoute{input: input, retired: make(chan struct{})}
	return true
}

func (r *PTYRegistry) unregister(id string, input chan []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if route := r.sessions[id]; route != nil && route.input == input {
		close(route.retired)
		delete(r.sessions, id)
	}
}

// Register installs local input routing synchronously, without relay access.
func (r *PTYRegistry) Register(id string) (<-chan []byte, func(), bool) {
	input := make(chan []byte, 64)
	if !r.register(id, input) {
		return nil, func() {}, false
	}
	return input, func() { r.unregister(id, input) }, true
}

// deliver serializes admission with retirement. A blocking channel send would
// outlive unregister and could succeed after the consumer has exited. Retry only
// nonblocking sends under the registry lock, against the original registration;
// a replacement with the same session ID must never receive an old input.
func (r *PTYRegistry) deliver(ctx context.Context, id string, data []byte) (string, error) {
	route := r.lookup(id)
	if route == nil {
		return "session_not_found", nil
	}
	deadline := time.NewTimer(ptyInputWait)
	defer deadline.Stop()
	// Receivers use ordinary channels, so freeing capacity has no notification.
	// Poll only while full, without holding the lock or delaying retirement.
	retry := time.NewTicker(time.Millisecond)
	defer retry.Stop()
	for {
		r.mu.Lock()
		if r.sessions[id] != route {
			r.mu.Unlock()
			return "session_not_found", nil
		}
		select {
		case route.input <- data:
			r.mu.Unlock()
			return "", nil
		default:
			r.mu.Unlock()
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-route.retired:
			return "session_not_found", nil
		case <-deadline.C:
			return "session_input_busy", nil
		case <-retry.C:
		}
	}
}
