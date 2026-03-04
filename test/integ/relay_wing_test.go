//go:build e2e

package integ

import (
	"context"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/ehrlich-b/wingthing/internal/ws"
)

func TestWingRegistersAndHeartbeats(t *testing.T) {
	srv, ts, store := testRelayAndWS(t)
	token, _ := createTestUser(t, store, "wing1")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Connect wing WebSocket
	conn, _, err := websocket.Dial(ctx, wsURL(ts)+"/ws/wing?token="+token, nil)
	if err != nil {
		t.Fatalf("dial wing ws: %v", err)
	}
	defer conn.CloseNow()

	// Send wing.register
	reg := ws.WingRegister{
		Type:     ws.TypeWingRegister,
		WingID:   "test-wing-1",
		Hostname: "testhost",
		Platform: "darwin",
		Agents:   []string{"claude"},
	}
	if err := wsjson.Write(ctx, conn, reg); err != nil {
		t.Fatalf("write wing.register: %v", err)
	}

	// Read registered ack
	var ack ws.RegisteredMsg
	if err := wsjson.Read(ctx, conn, &ack); err != nil {
		t.Fatalf("read registered ack: %v", err)
	}
	if ack.Type != ws.TypeRegistered {
		t.Fatalf("expected type %q, got %q", ws.TypeRegistered, ack.Type)
	}
	if ack.WingID == "" {
		t.Fatal("expected non-empty wing ID in ack")
	}

	// Send heartbeat
	hb := ws.WingHeartbeat{
		Type:   ws.TypeWingHeartbeat,
		WingID: "test-wing-1",
	}
	if err := wsjson.Write(ctx, conn, hb); err != nil {
		t.Fatalf("write heartbeat: %v", err)
	}

	// Verify wing appears in registry
	found := srv.Wings.All()
	if len(found) != 1 {
		t.Fatalf("expected 1 wing in registry, got %d", len(found))
	}
	if found[0].WingID != "test-wing-1" {
		t.Errorf("expected wing_id test-wing-1, got %s", found[0].WingID)
	}
}
