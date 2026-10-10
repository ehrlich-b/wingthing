package ws

import "sync"

// PTYRegistry belongs to the wing runtime, before any relay connects. The relay
// and direct adapters subscribe to this local input routing; disconnecting
// either transport does not remove an accepted session.
type PTYRegistry struct {
	mu       sync.Mutex
	sessions map[string]chan []byte
}

func (r *PTYRegistry) HasPTYSession(id string) bool {
	return r.lookup(id) != nil
}

func (r *PTYRegistry) lookup(id string) chan []byte {
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
		r.sessions = make(map[string]chan []byte)
	}
	r.sessions[id] = input
	return true
}

func (r *PTYRegistry) unregister(id string, input chan []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sessions[id] == input {
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
