package relay

import (
	"fmt"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestPTYConfirmedRoutesRemainBoundedAfterDisconnect(t *testing.T) {
	server := NewServer(nil, ServerConfig{ResourceLimits: ResourceLimits{Routes: 3, RoutesPerUser: 2}})
	for i := range 2 {
		id := fmt.Sprintf("confirmed-%d", i)
		if !server.PTY.AddControllerStart(id, &PTYRoute{WingID: "wing", UserID: "owner"}) {
			t.Fatal("route rejected below limit")
		}
		server.forwardPTYToBrowser(id, "wing", []byte(`{"type":"pty.started"}`))
		if route := server.PTY.Get(id); route == nil || route.Provisional {
			t.Fatal("wing did not confirm route")
		}
	}
	if server.PTY.AddControllerStart("over-user", &PTYRoute{WingID: "wing", UserID: "owner"}) ||
		server.PTY.AddViewer("over-viewer", "viewer", "wing", &websocket.Conn{}, "owner") ||
		server.PTY.SetPendingController("over-attach", "wing", "owner", &websocket.Conn{}) {
		t.Fatal("confirmed detached routes escaped per-user capacity")
	}
	if !server.PTY.AddControllerStart("other-user", &PTYRoute{WingID: "wing", UserID: "other"}) {
		t.Fatal("one account consumed another's budget")
	}
	server.forwardPTYToBrowser("other-user", "wing", []byte(`{"type":"pty.started"}`))
	if server.PTY.AddControllerStart("over-global", &PTYRoute{WingID: "wing", UserID: "third"}) ||
		server.PTY.Set("over-set", &PTYRoute{UserID: "third"}) {
		t.Fatal("confirmed routes escaped global capacity")
	}
	if !server.PTY.SetPendingController("confirmed-0", "wing", "another-controller", &websocket.Conn{}) {
		t.Fatal("existing session could not be reattached at capacity")
	}
	if server.PTY.Get("confirmed-0").LimitUserID != "owner" {
		t.Fatal("takeover moved route between account budgets")
	}
	server.PTY.Remove("confirmed-1")
	if !server.PTY.AddControllerStart("replacement", &PTYRoute{WingID: "wing", UserID: "owner"}) {
		t.Fatal("released capacity was not reusable")
	}
}

func TestPTYAbandonedRoutesExpireWithoutEndingWingSessions(t *testing.T) {
	routes := NewPTYRoutes(ResourceLimits{Routes: 2, RoutesPerUser: 2, AbandonedRouteTTL: time.Hour})
	controller := &websocket.Conn{}
	if !routes.Set("abandoned", &PTYRoute{UserID: "owner", WingID: "wing", BrowserConn: controller}) {
		t.Fatal("route rejected")
	}
	routes.ClearBrowser(controller)
	route := routes.Get("abandoned")
	if route.AbandonedAt.IsZero() {
		t.Fatal("disconnect did not mark abandonment")
	}
	route.AbandonedAt = time.Now().Add(-2 * time.Hour)
	routes.ClearBrowser(&websocket.Conn{})
	if route.AbandonedAt.After(time.Now().Add(-time.Hour)) {
		t.Fatal("unrelated disconnect renewed abandoned entry")
	}
	if !routes.Set("active", &PTYRoute{UserID: "owner", BrowserConn: &websocket.Conn{}, AbandonedAt: time.Now().Add(-2 * time.Hour)}) {
		t.Fatal("active route rejected")
	}
	if routes.Get("abandoned") != nil || routes.Get("active") == nil {
		t.Fatal("expiry retained abandonment or removed active route")
	}
	if !routes.SetPendingController("abandoned", "wing", "owner", &websocket.Conn{}) {
		t.Fatal("expired relay entry prevented reattaching wing-owned session")
	}
}

func TestPTYAbandonedRouteExpiryOnLookup(t *testing.T) {
	routes := NewPTYRoutes(ResourceLimits{AbandonedRouteTTL: time.Hour})
	routes.Set("idle", &PTYRoute{AbandonedAt: time.Now().Add(-2 * time.Hour)})
	if routes.Get("idle") != nil || len(routes.routes) != 0 {
		t.Fatal("lookup retained an expired confirmed route")
	}
}
