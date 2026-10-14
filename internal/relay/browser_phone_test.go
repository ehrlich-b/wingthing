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

func TestBrowserPhoneExactConfiguredOrigin(t *testing.T) {
	s := &Server{Config: ServerConfig{BaseURL: "https://site.example", AppHost: "app.example"}, LocalMode: true, Wings: NewWingRegistry()}
	for _, origin := range []string{"", "null", "http://app.example", "https://site.example", "https://app.example:443", "https://app.example.evil", "https://app.example/", "https://app.example"} {
		r := httptest.NewRequest("GET", "https://app.example/ws/browser-phone?wing_id=missing", nil)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		s.handleBrowserPhoneWS(w, r)
		want := 403
		if origin == "https://app.example" {
			want = 404
		}
		if w.Code != want {
			t.Fatalf("origin %q: got %d want %d", origin, w.Code, want)
		}
	}
}
func TestBrowserPhoneForwardsOnlyOpaqueBoxesWithoutCookieAuthority(t *testing.T) {
	wing, peer := detachWebSocketPair(t)
	s := &Server{LocalMode: true, Wings: NewWingRegistry(), browserConns: map[*websocket.Conn]*browserConnection{}, tunnelRequests: map[tunnelRequestKey]pendingTunnelRequest{}, Bandwidth: NewBandwidthMeter(1<<20, 1<<20, nil)}
	s.Wings.Add(&ConnectedWing{ID: "owner:one", WingID: "one", UserID: "owner", Conn: wing, HostedRelay: "allow"})
	server := httptest.NewServer(http.HandlerFunc(s.handleBrowserPhoneWS))
	defer server.Close()
	s.Config.BaseURL = server.URL
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, valid := range []bool{true, false} {
		client, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"?wing_id=one", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {server.URL}, "Cookie": {"wingthing_session=forged-owner-cookie"}}})
		if err != nil {
			t.Fatal(err)
		}
		payload := `{"op":"phone.box","sender_pub":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","payload":"opaque"}`
		if !valid {
			payload = `{"op":"pair.start","id":"plain"}`
		}
		req := ws.TunnelRequest{Type: ws.TypeTunnelRequest, Purpose: "phone", WingID: "one", RequestID: "test", Payload: payload, SenderUserID: "admin", SenderOrgRole: "owner", SenderPasskeys: []string{"forged"}}
		if err = writeWebSocketJSON(ctx, client, req); err != nil {
			t.Fatal(err)
		}
		if valid {
			_, data, err := peer.Read(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var got ws.TunnelRequest
			if json.Unmarshal(data, &got) != nil || !got.BrowserPhone || got.Payload != payload || got.SenderUserID != "" || got.SenderOrgRole != "" || len(got.SenderPasskeys) != 0 {
				t.Fatal("identity leaked")
			}
		} else if _, _, err = client.Read(ctx); err == nil {
			t.Fatal("plaintext accepted")
		}
		client.CloseNow()
	}
	// Preserve hosted wing admission even with a matching Origin and cookie.
	s.Wings.Add(&ConnectedWing{ID: "denied", WingID: "denied", HostedRelay: "deny"})
	r := httptest.NewRequest("GET", server.URL+"?wing_id=denied", nil)
	r.Header.Set("Origin", server.URL)
	w := httptest.NewRecorder()
	s.handleBrowserPhoneWS(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}

func TestNativePhoneRouteStillRejectsBrowserOrigin(t *testing.T) {
	s := &Server{LocalMode: true, Wings: NewWingRegistry()}
	s.Wings.Add(&ConnectedWing{ID: "owner:one", WingID: "one", HostedRelay: "allow"})
	r := httptest.NewRequest("GET", "http://relay.example/ws/phone?wing_id=one", nil)
	r.Header.Set("Origin", "https://app.example")
	w := httptest.NewRecorder()
	s.handlePhoneWS(w, r)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "native phone route") {
		t.Fatal("native origin gate changed", w.Code)
	}
}
