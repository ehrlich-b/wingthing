package relay

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/ehrlich-b/wingthing/internal/ws"
)

func TestNewRelayRouteIDHasSixtyFourBitsOfReadableEntropy(t *testing.T) {
	want := regexp.MustCompile(`^[0-9a-f]{16}$`)
	seen := make(map[string]struct{}, 1000)
	for range 1000 {
		id := newRelayRouteID()
		if !want.MatchString(id) {
			t.Fatalf("relay route ID %q is not 16 lowercase hex characters", id)
		}
		if _, exists := seen[id]; exists {
			t.Fatalf("duplicate relay route ID %q", id)
		}
		seen[id] = struct{}{}
	}
}

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
	} {
		if !routes.CanAuthenticate("session", "", connection) {
			t.Fatalf("%s should be associated with route", name)
		}
	}
	if !routes.CanAuthenticate("session", "viewer", viewer) {
		t.Fatal("viewer should authenticate with its assigned viewer ID")
	}
	if routes.CanAuthenticate("session", "", viewer) || routes.CanAuthenticate("session", "wrong", viewer) {
		t.Fatal("viewer authenticated without its exact viewer ID")
	}
	if routes.CanAuthenticate("session", "", stranger) {
		t.Fatal("unassociated connection was allowed to submit passkey response")
	}

	routes.ClearBrowser(pending)
	if routes.CanAuthenticate("session", "", pending) {
		t.Fatal("closed pending controller remained associated")
	}
	if !routes.CanAuthenticate("session", "", controller) {
		t.Fatal("clearing pending controller displaced authorized controller")
	}

	restartedRoutes := NewPTYRoutes()
	if !restartedRoutes.AddViewer("reclaimed", "viewer", "wing-1", viewer) {
		t.Fatal("spectator route was rejected")
	}
	if !restartedRoutes.CanAuthenticate("reclaimed", "viewer", viewer) {
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
	if routes.CanAuthenticate("session", "viewer", viewer) || routes.CanAuthenticate("session", "", pending) {
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
	if wingID, ok := routes.AuthenticationWing("session", "", first); !ok || wingID != "wing-a" {
		t.Fatalf("first pending controller association = %q, %v", wingID, ok)
	}
	if _, ok := routes.AuthenticationWing("session", "", second); ok {
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

func TestPTYRoutesBoundAndReleaseUnconfirmedStarts(t *testing.T) {
	routes := NewPTYRoutes()
	controllers := make([]*websocket.Conn, maxProvisionalRoutes)
	for index := range maxProvisionalRoutes {
		controllers[index] = &websocket.Conn{}
		id := "pending-" + strconv.Itoa(index)
		if !routes.AddControllerStart(id, &PTYRoute{BrowserConn: controllers[index], WingID: "wing-a"}) {
			t.Fatalf("pending start %d was rejected before the limit", index)
		}
	}
	if routes.AddControllerStart("one-too-many", &PTYRoute{BrowserConn: &websocket.Conn{}, WingID: "wing-a"}) {
		t.Fatal("pending start limit was not enforced")
	}

	for _, controller := range controllers {
		routes.ClearBrowser(controller)
	}
	if len(routes.routes) != 0 {
		t.Fatalf("disconnected pending starts retained %d routes", len(routes.routes))
	}
	if !routes.AddControllerStart("replacement", &PTYRoute{BrowserConn: controllers[0], WingID: "wing-a"}) {
		t.Fatal("released pending capacity was not reusable")
	}
}

func TestPTYRoutesLimitUnconfirmedStartsPerBrowser(t *testing.T) {
	routes := NewPTYRoutes()
	controller := &websocket.Conn{}
	for index := range maxProvisionalRoutesPerBrowser {
		if !routes.AddControllerStart("browser-pending-"+strconv.Itoa(index), &PTYRoute{BrowserConn: controller, WingID: "wing-a"}) {
			t.Fatalf("browser pending start %d was rejected before its limit", index)
		}
	}
	if routes.AddControllerStart("browser-one-too-many", &PTYRoute{BrowserConn: controller, WingID: "wing-a"}) {
		t.Fatal("per-browser pending start limit was not enforced")
	}
	if !routes.AddControllerStart("other-browser", &PTYRoute{BrowserConn: &websocket.Conn{}, WingID: "wing-a"}) {
		t.Fatal("one browser consumed another browser's provisional capacity")
	}
	routes.ClearBrowser(controller)
	if !routes.AddControllerStart("browser-replacement", &PTYRoute{BrowserConn: controller, WingID: "wing-a"}) {
		t.Fatal("released per-browser capacity was not reusable")
	}
}

func TestPTYRoutesRemoveOnlyMatchingProvisionalRoute(t *testing.T) {
	routes := NewPTYRoutes()
	old := &PTYRoute{BrowserConn: &websocket.Conn{}, WingID: "wing-a"}
	if !routes.AddControllerStart("session", old) {
		t.Fatal("initial route was rejected")
	}
	confirmed := &PTYRoute{BrowserConn: &websocket.Conn{}, WingID: "wing-a"}
	routes.Set("session", confirmed)
	routes.removeProvisional("session", old)
	if got := routes.Get("session"); got != confirmed {
		t.Fatal("stale cleanup removed replacement route")
	}

	if !routes.AddControllerStart("pending", old) {
		t.Fatal("pending route was rejected")
	}
	routes.removeProvisional("pending", old)
	if got := routes.Get("pending"); got != nil {
		t.Fatalf("matching provisional route survived cleanup: %#v", got)
	}
}

func TestPTYRoutesOfflineRecipientsAreWingScopedAndDeduplicated(t *testing.T) {
	routes := NewPTYRoutes()
	shared := &websocket.Conn{}
	pending := &websocket.Conn{}
	viewer := &websocket.Conn{}
	routes.Set("one", &PTYRoute{
		BrowserConn: shared, PendingController: pending,
		Viewers: map[string]*websocket.Conn{"same": shared, "viewer": viewer}, WingID: "wing-a",
	})
	routes.Set("two", &PTYRoute{BrowserConn: shared, WingID: "wing-a"})
	routes.Set("other", &PTYRoute{BrowserConn: &websocket.Conn{}, WingID: "wing-b"})

	recipients := routes.offlineRecipients("wing-a")
	if len(recipients) != 3 {
		t.Fatalf("offline recipients = %d, want 3", len(recipients))
	}
	seen := make(map[*websocket.Conn]bool)
	for _, recipient := range recipients {
		if seen[recipient] {
			t.Fatal("offline recipients contained a duplicate connection")
		}
		seen[recipient] = true
	}
	for _, want := range []*websocket.Conn{shared, pending, viewer} {
		if !seen[want] {
			t.Fatal("offline recipients omitted an associated connection")
		}
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
	for name, tc := range map[string]struct {
		viewerID string
		conn     *websocket.Conn
	}{
		"controller": {conn: controller},
		"pending":    {conn: pending},
		"viewer":     {viewerID: "viewer", conn: viewer},
	} {
		if wingID, ok := routes.AuthenticationWing("session", tc.viewerID, tc.conn); !ok || wingID != "wing-a" {
			t.Fatalf("%s authentication route = %q, %v", name, wingID, ok)
		}
	}
	if _, ok := routes.AuthenticationWing("session", "", &websocket.Conn{}); ok {
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
	if !server.registerTunnelRequest("wing-a", "request", browserA, true) {
		t.Fatal("valid tunnel request was rejected")
	}
	if server.registerTunnelRequest("wing-a", "request", browserB, true) {
		t.Fatal("duplicate tunnel request replaced its originating browser")
	}
	if got := server.pendingTunnelBrowser("wing-b", "request", true); got != nil {
		t.Fatal("another wing consumed a pending tunnel request")
	}
	if got := server.pendingTunnelBrowser("wing-a", "request", false); got == nil || got.Browser != browserA || !got.Coordination {
		t.Fatalf("source wing could not resolve its pending tunnel request: %#v", got)
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

func TestTunnelRequestsAreBoundedPerBrowser(t *testing.T) {
	server := &Server{tunnelRequests: make(map[tunnelRequestKey]pendingTunnelRequest)}
	browser := &websocket.Conn{}
	for index := range maxPendingTunnelRequestsPerBrowser {
		if !server.registerTunnelRequest("wing-a", "request-"+strconv.Itoa(index), browser, true) {
			t.Fatalf("browser tunnel request %d rejected before its limit", index)
		}
	}
	if server.registerTunnelRequest("wing-a", "one-too-many", browser, true) {
		t.Fatal("per-browser tunnel request limit was not enforced")
	}
	if !server.registerTunnelRequest("wing-a", "other-browser", &websocket.Conn{}, true) {
		t.Fatal("one browser consumed another browser's tunnel capacity")
	}
	server.clearTunnelRequests(browser)
	if !server.registerTunnelRequest("wing-a", "replacement", browser, true) {
		t.Fatal("released per-browser tunnel capacity was not reusable")
	}
}

func TestOnlyPurposeBindingWingsCanMarkTunnelResponsesAsCoordination(t *testing.T) {
	request := ws.TunnelRequest{Purpose: ws.TunnelPurposeSignal, Payload: "bounded"}
	if trustedCoordinationRequest(&ConnectedWing{}, request) {
		t.Fatal("N-1 wing could bypass live relay entitlement with an unverified purpose")
	}
	if !trustedCoordinationRequest(&ConnectedWing{PurposeBinding: true}, request) {
		t.Fatal("purpose-binding wing's bounded signaling request was not recognized")
	}
	request.Payload = strings.Repeat("x", ws.MaxCoordinationTunnelPayload+1)
	if trustedCoordinationRequest(&ConnectedWing{PurposeBinding: true}, request) {
		t.Fatal("oversized signaling request was marked as coordination")
	}
}

func TestCoordinationTunnelResponseIsBoundedIndependentlyOfWing(t *testing.T) {
	server := &Server{tunnelRequests: make(map[tunnelRequestKey]pendingTunnelRequest)}
	browser := &websocket.Conn{}
	if !server.registerTunnelRequest("wing-a", "request", browser, true) {
		t.Fatal("coordination request was rejected")
	}
	for index := 0; index < maxCoordinationResponseMessages; index++ {
		pending, over := server.takePendingTunnelResponse("wing-a", "request", 1, false)
		if pending == nil || over {
			t.Fatalf("bounded response %d rejected: pending=%#v over=%v", index, pending, over)
		}
	}
	pending, over := server.takePendingTunnelResponse("wing-a", "request", 1, false)
	if pending == nil || !over {
		t.Fatalf("message budget was not enforced: pending=%#v over=%v", pending, over)
	}
	if pending := server.pendingTunnelBrowser("wing-a", "request", false); pending != nil {
		t.Fatal("over-budget request remained registered")
	}

	if !server.registerTunnelRequest("wing-a", "bytes", browser, true) {
		t.Fatal("second coordination request was rejected")
	}
	if pending, over := server.takePendingTunnelResponse("wing-a", "bytes", maxCoordinationResponseBytes+1, false); pending == nil || !over {
		t.Fatalf("byte budget was not enforced: pending=%#v over=%v", pending, over)
	}
}
