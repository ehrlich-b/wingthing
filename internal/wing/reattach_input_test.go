package wing

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

// Exercise the real relay reader, session input loop, encryption and egg RPC.
// Only the egg process and the slow browser replay sink are protocol fixtures.
func TestReattachFullReplayDelivers200InputsOrErrors(t *testing.T) {
	f := newSessionTransportFixture(t)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	replayStarted := make(chan struct{}, 1)
	handler := f.client.OnPTY
	f.client.OnPTY = func(ctx context.Context, start ws.PTYStart, write ws.PTYWriteFunc, input <-chan []byte) {
		handler(ctx, start, func(v any) error {
			if output, ok := v.(ws.PTYOutput); ok && output.Compressed {
				select {
				case replayStarted <- struct{}{}:
				default:
				}
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return write(v)
		}, input)
	}
	f.client.OnTunnel = func(_ context.Context, req ws.TunnelRequest, write ws.PTYWriteFunc) {
		_ = write(ws.TunnelResponse{Type: ws.TypeTunnelResponse, RequestID: req.RequestID})
	}
	key := base64.StdEncoding.EncodeToString(f.browserKey.PublicKey().Bytes())
	const id = "replay-input"
	f.send(ws.PTYStart{Type: ws.TypePTYStart, SessionID: id, Agent: "claude", CWD: f.work, UserID: "alice", Email: "alice@example.com", OrgRole: "owner", PublicKey: key, Cols: 80, Rows: 24})
	f.next(ws.TypePTYStarted)
	ef := f.egg(id)
	ef.mu.Lock()
	ef.snapshot = bytes.Repeat([]byte("replay\n"), (2*1024*1024)/7+1)[:2*1024*1024]
	ef.mu.Unlock()
	// Drain the egg's bounded input sink concurrently, preserving receive order.
	received := make(chan []byte, 201)
	go func() {
		for {
			select {
			case input := <-ef.input:
				received <- input
			case <-f.ctx.Done():
				return
			}
		}
	}()
	f.send(ws.PTYAttach{Type: ws.TypePTYAttach, SessionID: id, ControllerID: "replacement", PublicKey: key, UserID: "alice", Email: "alice@example.com", OrgRole: "owner"})
	f.next(ws.TypePTYStarted)
	select {
	case <-replayStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("reattach never started the full replay")
	}
	sendInput := func(text string) {
		t.Helper()
		encoded, err := auth.Encrypt(f.ptyKey, []byte(text))
		if err != nil {
			t.Fatal(err)
		}
		f.send(ws.PTYInput{Type: ws.TypePTYInput, SessionID: id, Data: encoded})
	}
	for i := range 200 {
		sendInput(fmt.Sprintf("%03d", i))
	}
	// A control response fences the relay reader after all 200 frames, without
	// releasing replay. It also collects every explicit per-session refusal.
	f.send(ws.TunnelRequest{Type: ws.TypeTunnelRequest, RequestID: "input-fence"})
	refused := 0
	fence := time.NewTimer(3 * time.Second)
	defer fence.Stop()
reading:
	for {
		select {
		case data := <-f.replies:
			var response struct {
				Type, Code, Message string
				SessionID           string `json:"session_id"`
				RequestID           string `json:"request_id"`
			}
			if err := json.Unmarshal(data, &response); err != nil {
				t.Fatal(err)
			}
			if response.Type == ws.TypeError {
				if response.SessionID != id || response.Code != "session_input_busy" {
					t.Fatalf("unexpected refusal: %s", data)
				}
				refused++
			}
			if response.Type == ws.TypeTunnelResponse && response.RequestID == "input-fence" {
				break reading
			}
		case <-fence.C:
			t.Fatal("relay input stalled behind replay")
		}
	}
	unblock()
	delivered, previous := 0, -1
	drain := time.NewTimer(3 * time.Second)
	defer drain.Stop()
	for delivered+refused < 200 {
		select {
		case input := <-received:
			number, err := strconv.Atoi(string(input))
			if err != nil || number <= previous || number >= 200 {
				t.Fatalf("out-of-order input after %d: %q", previous, input)
			}
			previous = number
			delivered++
		case <-drain.C:
			t.Fatalf("silent input loss: delivered=%d explicit_errors=%d, want 200 outcomes", delivered, refused)
		}
	}
	t.Logf("2 MiB replay: %d numbered inputs delivered in order, %d explicit errors", delivered, refused)
}
