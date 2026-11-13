package ws

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestRelayInputCannotSupplyDataChannelOrigin(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		var registration WingRegister
		if err := wsjson.Read(ctx, conn, &registration); err != nil {
			t.Error(err)
			return
		}
		if err := wsjson.Write(ctx, conn, map[string]string{"type": TypePTYInput, "session_id": "session", "controller_id": "authenticated-controller", "request_id": "authenticated-socket", "data": "encrypted", "wing_input_origin": "forged"}); err != nil {
			t.Error(err)
			return
		}
		<-ctx.Done()
	}))
	defer server.Close()
	client := &Client{RoostURL: "ws" + strings.TrimPrefix(server.URL, "http")}
	input := make(chan []byte, 1)
	if !client.registerPTYSession("session", input) {
		t.Fatal("register")
	}
	done := make(chan struct{})
	go func() { defer close(done); _, _ = client.connectAndServe(ctx) }()
	select {
	case data := <-input:
		var fields map[string]string
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		if _, ok := fields["wing_input_origin"]; ok {
			t.Fatalf("socket forged local origin: %s", data)
		}
		if fields["request_id"] != "authenticated-socket" || fields["controller_id"] != "authenticated-controller" || fields["data"] != "encrypted" {
			t.Fatalf("lost authenticated relay routing: %s", data)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	cancel()
	<-done
}
