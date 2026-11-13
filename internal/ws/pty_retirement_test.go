package ws

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// Done is evaluated when the full-queue sender enters its bounded wait. This
// fences retirement without sleeping or relying on goroutine scheduling.
type queueWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *queueWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestPTYUnregisterDuringQueueWait(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		name := "exit"
		if replacement {
			name = "replacement"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			client := &Client{}
			connected := make(chan struct{})
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.CloseNow()
				client.mu.Lock()
				client.conn = conn
				client.mu.Unlock()
				close(connected)
				<-release
			}))
			defer server.Close()
			defer close(release)
			browser, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer browser.CloseNow()
			<-connected
			input := make(chan []byte, 1)
			if !client.registerPTYSession("session", input) {
				t.Fatal("register")
			}
			input <- []byte("sentinel")
			waiting := &queueWaitContext{Context: ctx, waiting: make(chan struct{})}
			result := make(chan error, 1)
			data := []byte(`{"type":"pty.input","session_id":"session","controller_id":"A","request_id":"socket-A"}`)
			go func() { result <- client.forwardPTYMessage(waiting, "session", data) }()
			select {
			case <-waiting.waiting:
			case <-ctx.Done():
				t.Fatal("sender never waited")
			}
			client.unregisterPTYSession("session", input)
			<-input // Space becomes available only after retirement.
			newer := make(chan []byte, 1)
			if replacement && !client.registerPTYSession("session", newer) {
				t.Fatal("replace")
			}
			select {
			case err := <-result:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("retired queue sender did not finish")
			}
			if len(input) != 0 || len(newer) != 0 {
				t.Fatal("retired or replacement queue accepted abandoned input")
			}
			var refusal ErrorMsg
			if err := wsjson.Read(ctx, browser, &refusal); err != nil {
				t.Fatal(err)
			}
			if refusal.Type != TypeError || refusal.Code != "session_not_found" || refusal.Operation != TypePTYInput || refusal.SessionID != "session" || refusal.ControllerID != "A" || refusal.RequestID != "socket-A" {
				t.Fatalf("refusal lost originating route: %+v", refusal)
			}
			// The direct adapter shares admission and still returns a typed refusal for
			// the retired session; a newly registered session remains usable.
			direct := client.PushPTYInput("session", data)
			if replacement {
				if direct != nil {
					t.Fatalf("new session refused: %+v", direct)
				}
				var delivered PTYInput
				if err := json.Unmarshal(<-newer, &delivered); err != nil || delivered.SessionID != "session" {
					t.Fatalf("new input: %+v, %v", delivered, err)
				}
				client.unregisterPTYSession("session", newer)
			} else if direct == nil || direct.Type != TypeError || direct.Code != "session_not_found" || direct.Operation != TypePTYInput || direct.SessionID != "session" || direct.RequestID != "socket-A" {
				t.Fatalf("direct refusal: %+v", direct)
			}
		})
	}
}
