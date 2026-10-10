package wingconnect

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
)

// A transport reader can report failure before its Done notifier is scheduled.
// Closing the failed transport must remove it from routing in that interval.
type delayedDoneConnection struct {
	done  chan struct{}
	calls atomic.Int32
	call  func(json.RawMessage) (map[string]any, bool, error)
}

func (c *delayedDoneConnection) Done() <-chan struct{} { return c.done }
func (c *delayedDoneConnection) Close() error          { return nil }
func (c *delayedDoneConnection) Call(_ context.Context, _ string, args json.RawMessage) (map[string]any, bool, error) {
	c.calls.Add(1)
	return c.call(args)
}

func TestRunRecoveryRetryDiscardsFailedConnectionBeforeDone(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	old := &delayedDoneConnection{done: make(chan struct{}), call: func(json.RawMessage) (map[string]any, bool, error) {
		return nil, true, &controlsocket.TransportError{Err: errors.New("lost receipt")}
	}}
	next := &delayedDoneConnection{done: make(chan struct{}), call: func(args json.RawMessage) (map[string]any, bool, error) {
		var fields map[string]string
		if err := json.Unmarshal(args, &fields); err != nil || fields["idempotency_key"] != "original" || fields["run_id"] != "parent" {
			t.Errorf("retry changed accepted request: %s, %v", args, err)
		}
		return map[string]any{"run_id": "child", "session_id": "single-egg"}, false, nil
	}}
	p := &Pool{ctx: ctx, opts: Options{CallTimeout: time.Minute}, entries: map[string]*entry{}}
	e := &entry{pool: p, name: "one", remote: config.Remote{WingID: "wing-one"}, ctx: ctx, connection: old, attempts: 1, changed: make(chan struct{}), wake: make(chan struct{}, 1)}
	p.entries[e.name] = e
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		select {
		case <-e.wake:
			e.mu.Lock()
			e.connection = next
			e.attempts++
			e.signal()
			e.mu.Unlock()
		case <-ctx.Done():
		}
	}()
	t.Cleanup(func() { cancel(); <-workerDone })
	data, denied, err := p.Call(ctx, e.remote.WingID, "agent_steer", json.RawMessage(`{"run_id":"parent","prompt":"direction","idempotency_key":"original"}`))
	if err != nil || denied || data["run_id"] != "child" || data["idempotency_key"] != "original" || old.calls.Load() != 1 || next.calls.Load() != 1 {
		t.Fatalf("retry reused a failed transport before Done: %v, %v; calls=%d/%d", data, err, old.calls.Load(), next.calls.Load())
	}
}
