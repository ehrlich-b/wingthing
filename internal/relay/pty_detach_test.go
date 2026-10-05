package relay

import (
	"testing"

	"github.com/coder/websocket"
)

func TestBrowserDetachCarriesExactControllerAndViewerIDs(t *testing.T) {
	routes := NewPTYRoutes()
	old, pending, viewer, stranger := &websocket.Conn{}, &websocket.Conn{}, &websocket.Conn{}, &websocket.Conn{}
	routes.Set("session", &PTYRoute{WingID: "wing", BrowserConn: old, ControllerID: "old", PendingController: pending, PendingControllerID: "new", Viewers: map[string]*websocket.Conn{"viewer": viewer}})
	captured := routes.browserDetachRoutes(old, "session")
	if len(captured) != 1 || captured[0].message.ControllerID != "old" || captured[0].wingID != "wing" {
		t.Fatalf("old disconnect capture: %v", captured)
	}
	if got := routes.browserDetachRoutes(pending, ""); len(got) != 1 || got[0].message.ControllerID != "new" {
		t.Fatalf("pending disconnect: %v", got)
	}
	if got := routes.browserDetachRoutes(viewer, ""); len(got) != 1 || got[0].message.ViewerID != "viewer" || got[0].message.ControllerID != "" {
		t.Fatalf("observer disconnect: %v", got)
	}
	if got := routes.browserDetachRoutes(stranger, ""); len(got) != 0 {
		t.Fatalf("foreign disconnect: %v", got)
	}
	if got := routes.browserDetachRoutes(old, "different-session"); len(got) != 0 {
		t.Fatalf("wrong target disconnected: %v", got)
	}
	// Simulate promotion between capture and network delivery. The queued old
	// detach retains its old ID, and clearing old connection cannot remove new.
	route := routes.Get("session")
	route.mu.Lock()
	route.BrowserConn = pending
	route.ControllerID = "new"
	route.PendingController = nil
	route.PendingControllerID = ""
	route.mu.Unlock()
	routes.ClearBrowser(old)
	if captured[0].message.ControllerID != "old" || routes.Get("session").BrowserConn != pending {
		t.Fatal("old close displaced replacement")
	}
}
