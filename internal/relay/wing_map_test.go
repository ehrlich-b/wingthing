package relay

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/ws"
)

func TestWingMapHostedRelayPolicyPropagationAndCompatibility(t *testing.T) {
	server := &Server{WingMap: NewWingMap()}

	register := httptest.NewRequest("POST", "/internal/wing-register", strings.NewReader(`{
        "wing_id":"current-wing","machine_id":"edge-a","user_id":"alice",
        "hosted_relay":"deny"
    }`))
	registerResult := httptest.NewRecorder()
	server.handleWingRegister(registerResult, register)
	if registerResult.Code != 200 {
		t.Fatalf("register status = %d body=%s", registerResult.Code, registerResult.Body.String())
	}
	current, ok := server.WingMap.Locate("current-wing")
	if !ok || current.HostedRelay != ws.HostedRelayDeny {
		t.Fatalf("current wing location = %#v, found=%v", current, ok)
	}

	// N-1 edge snapshots omit hosted_relay; the N login node must preserve the
	// deployed allow behavior rather than treating omission as a denial.
	syncRequest := httptest.NewRequest("POST", "/internal/wing-sync", strings.NewReader(`{
        "machine_id":"edge-b","snapshot_at":1,
        "wings":[{"wing_id":"legacy-wing","user_id":"bob"}]
    }`))
	syncResult := httptest.NewRecorder()
	server.handleWingSync(syncResult, syncRequest)
	if syncResult.Code != 200 {
		t.Fatalf("sync status = %d body=%s", syncResult.Code, syncResult.Body.String())
	}
	legacy, ok := server.WingMap.Locate("legacy-wing")
	if !ok || legacy.HostedRelay != "" || !ws.HostedRelayAllowed(legacy.HostedRelay) {
		t.Fatalf("legacy wing location = %#v, found=%v", legacy, ok)
	}
}
