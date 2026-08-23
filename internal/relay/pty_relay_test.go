package relay

import (
	"testing"

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
	restartedRoutes.AddViewer("reclaimed", "viewer", "wing-1", viewer)
	if !restartedRoutes.CanAuthenticate("reclaimed", viewer) {
		t.Fatal("spectator route was not recreated after relay restart")
	}
	if route := restartedRoutes.Get("reclaimed"); route == nil || route.WingID != "wing-1" {
		t.Fatalf("recreated route = %#v", route)
	}
}
