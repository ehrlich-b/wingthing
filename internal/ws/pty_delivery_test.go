package ws

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestPTYDeliveryRefusesFullAndMissingSession(t *testing.T) {
	for _, kind := range []string{TypePTYAttach, TypePTYInput, TypePTYDetach, TypePasskeyResponse} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			replies := make(chan ErrorMsg, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.CloseNow()
				var reg WingRegister
				if err := wsjson.Read(ctx, conn, &reg); err != nil {
					t.Error(err)
					return
				}
				for _, id := range []string{"full", "missing"} {
					if err := wsjson.Write(ctx, conn, map[string]string{"type": kind, "session_id": id, "controller_id": "attempt", "request_id": "sender"}); err != nil {
						t.Error(err)
						return
					}
					var reply ErrorMsg
					if err := wsjson.Read(ctx, conn, &reply); err != nil {
						t.Error(err)
						return
					}
					replies <- reply
				}
			}))
			defer server.Close()
			client := &Client{RoostURL: "ws" + strings.TrimPrefix(server.URL, "http")}
			input := make(chan []byte, 64)
			if !client.registerPTYSession("full", input) {
				t.Fatal("register")
			}
			for range cap(input) {
				input <- []byte("queued")
			}
			began := time.Now()
			_, _ = client.connectAndServe(ctx)
			for i, code := range []string{"session_input_busy", "session_not_found"} {
				select {
				case reply := <-replies:
					id := "full"
					if i == 1 {
						id = "missing"
					}
					if reply.Type != TypeError || reply.Code != code || reply.Operation != kind || reply.SessionID != id || reply.ControllerID != "attempt" || reply.RequestID != "sender" {
						t.Fatalf("refusal: %+v", reply)
					}
				default:
					t.Fatal("message was silently lost")
				}
			}
			if elapsed := time.Since(began); elapsed < ptyInputWait || elapsed > time.Second {
				t.Fatalf("unbounded or absent queue wait: %s", elapsed)
			}
			if len(input) != cap(input) {
				t.Fatal("refused message changed queued inputs")
			}
		})
	}
}

func TestP2PInputWaitsForQueueSpace(t *testing.T) {
	client := &Client{}
	input := make(chan []byte, 1)
	if !client.registerPTYSession("session", input) {
		t.Fatal("register")
	}
	input <- []byte("first")
	result := make(chan *ErrorMsg, 1)
	go func() { result <- client.PushPTYInput("session", []byte("second")) }()
	select {
	case refusal := <-result:
		t.Fatalf("did not wait for queue space: %+v", refusal)
	case <-time.After(ptyInputWait / 4):
	}
	if got := string(<-input); got != "first" {
		t.Fatalf("first input = %q", got)
	}
	select {
	case refusal := <-result:
		if refusal != nil {
			t.Fatalf("available space refused: %+v", refusal)
		}
	case <-time.After(time.Second):
		t.Fatal("queue wait did not finish")
	}
	if got := string(<-input); got != "second" {
		t.Fatalf("second input = %q", got)
	}
}
