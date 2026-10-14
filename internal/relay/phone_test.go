package relay

import (
	"context"
	"encoding/json"
	"github.com/coder/websocket"
	"github.com/ehrlich-b/wingthing/internal/ws"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPhoneRelayOpaqueRoutingWithoutPhoneAccount(t *testing.T) {
	wing, wingPeer := detachWebSocketPair(t)
	s := &Server{LocalMode: true, Wings: NewWingRegistry(), browserConns: map[*websocket.Conn]*browserConnection{}, tunnelRequests: map[tunnelRequestKey]pendingTunnelRequest{}, Bandwidth: NewBandwidthMeter(1<<20, 1<<20, nil)}
	s.Wings.Add(&ConnectedWing{ID: "owner:wing-one", WingID: "wing-one", UserID: "owner", Conn: wing, HostedRelay: "allow"})
	server := httptest.NewServer(http.HandlerFunc(s.handlePhoneWS))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	phone, _, e := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"?wing_id=wing-one", nil)
	if e != nil {
		t.Fatal(e)
	}
	defer phone.CloseNow()
	req := ws.TunnelRequest{Type: ws.TypeTunnelRequest, Purpose: "phone", WingID: "wing-one", RequestID: "request-one", Payload: `{"op":"phone.box","sender_pub":"random-public-key","payload":"opaque-ciphertext"}`, SenderUserID: "forged-admin", SenderOrgRole: "owner", SenderPasskeys: []string{"forged-passkey"}, BrowserPhone: true}
	if e = writeWebSocketJSON(ctx, phone, req); e != nil {
		t.Fatal(e)
	}
	_, b, e := wingPeer.Read(ctx)
	if e != nil {
		t.Fatal(e)
	}
	var got ws.TunnelRequest
	if json.Unmarshal(b, &got) != nil || got.BrowserPhone || got.Payload != req.Payload || got.SenderUserID != "" || got.SenderOrgRole != "" || len(got.SenderPasskeys) != 0 {
		t.Fatal("relay disclosed or elevated phone request")
	}
	// A different wing cannot consume the pending reply.
	wrong := ws.TunnelResponse{Type: ws.TypeTunnelResponse, RequestID: req.RequestID, Payload: "attacker-ciphertext"}
	data, _ := json.Marshal(wrong)
	s.forwardTunnelToBrowser("wing-two", req.RequestID, data, true)
	good := ws.TunnelResponse{Type: ws.TypeTunnelResponse, RequestID: req.RequestID, Payload: "wing-ciphertext"}
	data, _ = json.Marshal(good)
	s.forwardTunnelToBrowser("wing-one", req.RequestID, data, true)
	_, b, e = phone.Read(ctx)
	if e != nil {
		t.Fatal(e)
	}
	var reply ws.TunnelResponse
	if json.Unmarshal(b, &reply) != nil || reply.Payload != "wing-ciphertext" {
		t.Fatal("reply wrong wing or missing owner metering")
	}
	req.WingID = "wing-two"
	req.RequestID = "cross-machine"
	_ = writeWebSocketJSON(ctx, phone, req)
	if _, _, e = phone.Read(ctx); e == nil {
		t.Fatal("phone changed pinned route")
	}
}
func TestPhoneRelayUnreachableDisabledAndAccountHeaders(t *testing.T) {
	s := &Server{LocalMode: true, Wings: NewWingRegistry(), browserConns: map[*websocket.Conn]*browserConnection{}}
	s.Wings.Add(&ConnectedWing{ID: "disabled", WingID: "disabled", HostedRelay: "deny"})
	for _, tc := range []struct {
		query, auth, origin string
		status              int
	}{{"wing_id=absent", "", "", 404}, {"wing_id=disabled", "", "", 403}, {"wing_id=absent", "Bearer account-secret", "", 400}, {"", "", "", 400}} {
		r := httptest.NewRequest("GET", "http://relay/ws/phone?"+tc.query, nil)
		r.Header.Set("Authorization", tc.auth)
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		s.handlePhoneWS(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s: got %d want %d", tc.query, w.Code, tc.status)
		}
	}
}
