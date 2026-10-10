package control

import (
	"context"
	"encoding/json"
	"sync"
)

const CancelRequestOperation = "mcp/requests/cancel"

// RequestLifetimes scopes cancellation to requests on one authenticated
// connection. It never cancels the wing-owned durable execution context.
type RequestLifetimes struct {
	mu      sync.Mutex
	pending map[string]context.CancelFunc
}

func (r *RequestLifetimes) Start(parent context.Context, id string) (context.Context, func(), bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pending == nil {
		r.pending = map[string]context.CancelFunc{}
	}
	if _, exists := r.pending[id]; exists {
		return nil, nil, false
	}
	ctx, cancel := context.WithCancel(parent)
	r.pending[id] = cancel
	return ctx, func() { r.mu.Lock(); delete(r.pending, id); r.mu.Unlock(); cancel() }, true
}

func (r *RequestLifetimes) Cancel(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cancel := r.pending[id]; cancel != nil {
		cancel()
	}
}

func CancellationRequest(id string) DirectRequest {
	args, _ := json.Marshal(map[string]any{"requestId": id})
	return DirectRequest{Version: ContractVersion, Tool: CancelRequestOperation, Arguments: args}
}

func CancellationID(request DirectRequest) (string, bool) {
	if request.Tool != CancelRequestOperation {
		return "", false
	}
	var params struct {
		RequestID string `json:"requestId"`
	}
	if request.Version != ContractVersion || json.Unmarshal(request.Arguments, &params) != nil {
		return "", true
	}
	return params.RequestID, true
}
