package relay

import (
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestPTYRoutesAuthenticateOnlyAssociatedConnections(t *testing.T) {
	routes := NewPTYRoutes()
	controller := &websocket.Conn{}
	pending := &websocket.Conn{}
	viewer := &websocket.Conn{}
	stranger := &websocket.Conn{}
	routes.Set("session", &PTYRoute{
		BrowserConn:       controller,
		PendingController: pending,
		Viewers:           map[string]*websocket.Conn{"viewer": viewer},
		WingID:            "wing-1",
	})

	for name, connection := range map[string]*websocket.Conn{
		"controller": controller,
		"pending":    pending,
		"viewer":     viewer,
	} {
		if !routes.CanAuthenticate("session", connection) {
			t.Fatalf("%s should be associated with route", name)
		}
	}
	if routes.CanAuthenticate("session", stranger) {
		t.Fatal("unassociated connection was allowed to submit passkey response")
	}

	routes.ClearBrowser(pending)
	if routes.CanAuthenticate("session", pending) {
		t.Fatal("closed pending controller remained associated")
	}
	if !routes.CanAuthenticate("session", controller) {
		t.Fatal("clearing pending controller displaced authorized controller")
	}

	restartedRoutes := NewPTYRoutes()
	if !restartedRoutes.AddViewer("reclaimed", "viewer", "wing-1", viewer) {
		t.Fatal("spectator route was rejected")
	}
	if !restartedRoutes.CanAuthenticate("reclaimed", viewer) {
		t.Fatal("spectator route was not recreated after relay restart")
	}
	if route := restartedRoutes.Get("reclaimed"); route == nil || route.WingID != "wing-1" {
		t.Fatalf("recreated route = %#v", route)
	}
}

func TestPTYRoutesRejectCrossWingAttach(t *testing.T) {
	routes := NewPTYRoutes()
	controller := &websocket.Conn{}
	viewer := &websocket.Conn{}
	pending := &websocket.Conn{}
	routes.Set("session", &PTYRoute{BrowserConn: controller, WingID: "wing-a"})

	if routes.AddViewer("session", "viewer", "wing-b", viewer) {
		t.Fatal("another wing attached a spectator to an existing route")
	}
	if routes.SetPendingController("session", "wing-b", "user-b", pending) {
		t.Fatal("another wing attached a pending controller to an existing route")
	}
	if routes.CanAuthenticate("session", viewer) || routes.CanAuthenticate("session", pending) {
		t.Fatal("rejected cross-wing connection remained associated with the route")
	}
	if !routes.AddViewer("session", "viewer", "wing-a", viewer) {
		t.Fatal("same-wing spectator was rejected")
	}
	if !routes.SetPendingController("session", "wing-a", "user-a", pending) {
		t.Fatal("same-wing pending controller was rejected")
	}
}

func TestPTYRoutesAllowOnlyOnePendingController(t *testing.T) {
	routes := NewPTYRoutes()
	first := &websocket.Conn{}
	second := &websocket.Conn{}
	if !routes.SetPendingController("session", "wing-a", "user-a", first) {
		t.Fatal("first pending controller was rejected")
	}
	if routes.SetPendingController("session", "wing-a", "user-b", second) {
		t.Fatal("second pending controller replaced an in-flight authorization")
	}
	if wingID, ok := routes.AuthenticationWing("session", first); !ok || wingID != "wing-a" {
		t.Fatalf("first pending controller association = %q, %v", wingID, ok)
	}
	if _, ok := routes.AuthenticationWing("session", second); ok {
		t.Fatal("rejected pending controller remained associated with the route")
	}
}

func TestPTYRoutesDiscardUnconfirmedAttachOnDisconnect(t *testing.T) {
	routes := NewPTYRoutes()
	pending := &websocket.Conn{}
	if !routes.SetPendingController("session", "wing-a", "user-a", pending) {
		t.Fatal("pending controller was rejected")
	}
	route := routes.Get("session")
	if route == nil || !route.Provisional {
		t.Fatalf("reattach route = %#v, want provisional", route)
	}
	routes.ClearBrowser(pending)
	if route := routes.Get("session"); route != nil {
		t.Fatalf("disconnected unconfirmed attach pinned route: %#v", route)
	}
	if !routes.SetPendingController("session", "wing-b", "user-b", pending) {
		t.Fatal("released session ID could not be attached to its actual wing")
	}
}

func TestPTYRoutesAuthenticationResolvesAssociatedWing(t *testing.T) {
	routes := NewPTYRoutes()
	controller := &websocket.Conn{}
	pending := &websocket.Conn{}
	viewer := &websocket.Conn{}
	routes.Set("session", &PTYRoute{
		BrowserConn: controller, PendingController: pending,
		Viewers: map[string]*websocket.Conn{"viewer": viewer}, WingID: "wing-a",
	})
	for name, connection := range map[string]*websocket.Conn{
		"controller": controller, "pending": pending, "viewer": viewer,
	} {
		if wingID, ok := routes.AuthenticationWing("session", connection); !ok || wingID != "wing-a" {
			t.Fatalf("%s authentication route = %q, %v", name, wingID, ok)
		}
	}
	if _, ok := routes.AuthenticationWing("session", &websocket.Conn{}); ok {
		t.Fatal("unassociated connection received an authentication route")
	}
}

func TestPTYRoutesControlOnlyFromAuthorizedController(t *testing.T) {
	routes := NewPTYRoutes()
	controller := &websocket.Conn{}
	pending := &websocket.Conn{}
	viewer := &websocket.Conn{}
	stranger := &websocket.Conn{}
	routes.Set("session", &PTYRoute{
		BrowserConn:       controller,
		PendingController: pending,
		Viewers:           map[string]*websocket.Conn{"viewer": viewer},
		WingID:            "wing-1",
	})

	wingID, ok := routes.ControllerWing("session", controller)
	if !ok || wingID != "wing-1" {
		t.Fatalf("authorized controller resolved to %q, %v", wingID, ok)
	}
	for name, connection := range map[string]*websocket.Conn{
		"pending":  pending,
		"viewer":   viewer,
		"stranger": stranger,
	} {
		if wingID, ok := routes.ControllerWing("session", connection); ok {
			t.Fatalf("%s was allowed to control route through %q", name, wingID)
		}
	}
	if _, ok := routes.ControllerWing("missing", controller); ok {
		t.Fatal("controller was allowed to control a missing route")
	}
}

func TestTunnelRequestsAreBoundToSourceWingAndBrowser(t *testing.T) {
	server := &Server{tunnelRequests: make(map[tunnelRequestKey]pendingTunnelRequest)}
	browserA := &websocket.Conn{}
	browserB := &websocket.Conn{}
	if !server.registerTunnelRequest("wing-a", "request", browserA) {
		t.Fatal("valid tunnel request was rejected")
	}
	if server.registerTunnelRequest("wing-a", "request", browserB) {
		t.Fatal("duplicate tunnel request replaced its originating browser")
	}
	if got := server.pendingTunnelBrowser("wing-b", "request", true); got != nil {
		t.Fatal("another wing consumed a pending tunnel request")
	}
	if got := server.pendingTunnelBrowser("wing-a", "request", false); got != browserA {
		t.Fatal("source wing could not resolve its pending tunnel request")
	}
	server.clearTunnelRequests(browserA)
	if got := server.pendingTunnelBrowser("wing-a", "request", false); got != nil {
		t.Fatal("disconnected browser left a pending tunnel request")
	}
	server.tunnelRequests[tunnelRequestKey{WingID: "wing-a", RequestID: "expired"}] = pendingTunnelRequest{
		Browser: browserA, CreatedAt: time.Now().Add(-pendingTunnelRequestTTL - time.Second),
	}
	if got := server.pendingTunnelBrowser("wing-a", "expired", false); got != nil {
		t.Fatal("expired tunnel response reached its former browser")
	}
	if len(server.tunnelRequests) != 0 {
		t.Fatalf("expired tunnel request was not removed: %#v", server.tunnelRequests)
	}
}
