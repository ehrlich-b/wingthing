package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

func TestRoostAttentionPushRecipient(t *testing.T) {
	for _, scenario := range []string{"session-starter", "admin-takeover", "reattached-without-starter", "missing-route", "empty-user", "different-wing"} {
		t.Run(scenario, func(t *testing.T) {
			type push struct{ topic, click string }
			pushes := make(chan push, 4)
			ntfyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				pushes <- push{r.URL.Path, r.Header.Get("Click")}
				w.WriteHeader(http.StatusOK)
			}))
			defer ntfyServer.Close()

			store := testStore(t)
			mustTest(t, store.CreateUser(roostWingServiceUserID))
			mustTest(t, store.CreateDeviceToken("service-token", roostWingServiceUserID, "roost-wing", nil))
			starterToken, starterID := createTestToken(t, store, "b")
			adminToken, adminID := createTestToken(t, store, "admin")
			mustTest(t, store.CreateOrgWithSeats("attention-org", "Attention Org", "attention-org", roostWingServiceUserID, 3))
			mustTest(t, store.AddOrgMember("attention-org", starterID, "member"))
			mustTest(t, store.AddOrgMember("attention-org", adminID, "admin"))
			mustTest(t, store.SetNtfyConfig(roostWingServiceUserID, NtfyConfig{Topic: ntfyServer.URL + "/service", Events: "attention"}))
			mustTest(t, store.SetNtfyConfig(starterID, NtfyConfig{Topic: ntfyServer.URL + "/user-b", Events: "attention"}))
			mustTest(t, store.SetNtfyConfig(adminID, NtfyConfig{Topic: ntfyServer.URL + "/admin", Events: "attention"}))
			baseURL := os.Getenv("WT_BASE_URL")
			if baseURL == "" {
				baseURL = "https://roost.example"
			}
			// serve passes WT_BASE_URL into ServerConfig.BaseURL.
			server := NewServer(store, ServerConfig{BaseURL: baseURL})
			server.RoostMode = true
			relay := httptest.NewServer(server)
			defer relay.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			dial := func(path, token string) *websocket.Conn {
				t.Helper()
				conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(relay.URL, "http")+path, &websocket.DialOptions{
					HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
				})
				mustTest(t, err)
				return conn
			}
			wing := dial("/ws/wing", "service-token")
			defer func() { _ = wing.CloseNow() }()
			mustTest(t, wsjson.Write(ctx, wing, ws.WingRegister{Type: ws.TypeWingRegister, WingID: "shared-roost", OrgSlug: "attention-org"}))
			var ack ws.RegisteredMsg
			mustTest(t, wsjson.Read(ctx, wing, &ack))
			if ack.Type != ws.TypeRegistered {
				t.Fatalf("registration = %+v", ack)
			}

			sessionID, wantTopic, wantUser := "unknown-session", "/service", roostWingServiceUserID
			if scenario == "session-starter" || scenario == "admin-takeover" {
				browser := dial("/ws/pty", starterToken)
				defer func() { _ = browser.CloseNow() }()
				mustTest(t, wsjson.Write(ctx, browser, ws.PTYStart{Type: ws.TypePTYStart, WingID: "shared-roost", Agent: "claude", CWD: "/project"}))
				var start ws.PTYStart
				mustTest(t, wsjson.Read(ctx, wing, &start))
				if start.Type != ws.TypePTYStart || start.UserID != starterID || start.SessionID == "" {
					t.Fatalf("forwarded session start = %+v", start)
				}
				sessionID, wantTopic, wantUser = start.SessionID, "/user-b", starterID
				mustTest(t, wsjson.Write(ctx, wing, ws.PTYStarted{Type: ws.TypePTYStarted, SessionID: sessionID}))
				var started ws.PTYStarted
				mustTest(t, wsjson.Read(ctx, browser, &started))
				if started.Type != ws.TypePTYStarted || started.SessionID != sessionID {
					t.Fatalf("session confirmation = %+v", started)
				}
			} else if scenario == "empty-user" {
				server.PTY.Set(sessionID, &PTYRoute{WingID: "shared-roost"})
			} else if scenario == "different-wing" {
				server.PTY.Set(sessionID, &PTYRoute{WingID: "another-wing", UserID: starterID, StarterUserID: starterID})
			}

			if scenario == "admin-takeover" || scenario == "reattached-without-starter" {
				admin := dial("/ws/pty", adminToken)
				defer func() { _ = admin.CloseNow() }()
				mustTest(t, wsjson.Write(ctx, admin, ws.PTYAttach{Type: ws.TypePTYAttach, WingID: "shared-roost", SessionID: sessionID}))
				var attach ws.PTYAttach
				mustTest(t, wsjson.Read(ctx, wing, &attach))
				if attach.Type != ws.TypePTYAttach || attach.UserID != adminID || attach.OrgRole != "admin" || attach.SessionID != sessionID {
					t.Fatalf("forwarded admin attach = %+v", attach)
				}
				// The wing confirms authorization before the admin takes control.
				mustTest(t, wsjson.Write(ctx, wing, ws.PTYStarted{Type: ws.TypePTYStarted, SessionID: sessionID, ControllerID: attach.ControllerID}))
				var started ws.PTYStarted
				mustTest(t, wsjson.Read(ctx, admin, &started))
				if started.Type != ws.TypePTYStarted || started.SessionID != sessionID {
					t.Fatalf("admin attachment confirmation = %+v", started)
				}
				route := server.PTY.Get(sessionID)
				route.mu.Lock()
				controllerUser := route.UserID
				route.mu.Unlock()
				if controllerUser != adminID {
					t.Fatalf("controller user = %q, want %q", controllerUser, adminID)
				}
			}

			mustTest(t, wsjson.Write(ctx, wing, ws.SessionAttention{
				Type: ws.TypeSessionAttention, SessionID: sessionID, Nonce: "episode-1", Agent: "claude", CWD: "/project",
			}))
			select {
			case got := <-pushes:
				if got.topic != wantTopic || got.click != strings.TrimRight(baseURL, "/")+"/app/#s/"+sessionID {
					t.Fatalf("push = %+v, want topic %s and configured session URL", got, wantTopic)
				}
				t.Logf("session.attention: topic=%s click=%s", got.topic, got.click)
			case <-ctx.Done():
				t.Fatal("no attention push received")
			}
			// Inspect the dedup keys after the HTTP delivery: only the intended
			// recipient may have been scheduled, even when both users enable ntfy.
			server.ntfyNonceMu.Lock()
			defer server.ntfyNonceMu.Unlock()
			if len(server.ntfyNonceSeen) != 1 || !server.ntfyNonceSeen[wantUser+"\x00episode-1"] {
				t.Fatalf("attention scheduled for unexpected users: %v", server.ntfyNonceSeen)
			}
		})
	}
}

func TestNtfyClickURL(t *testing.T) {
	for _, tc := range []struct {
		name, baseURL, appHost, want string
	}{
		{"roost", "https://roost.example", "", "https://roost.example/app/#s/session-1"},
		{"trailing-slash", "https://roost.example/", "", "https://roost.example/app/#s/session-1"},
		{"split-host", "https://wingthing.ai", "app.wingthing.ai", "https://app.wingthing.ai/#s/session-1"},
		{"http-app-host", "http://login.example:8080", "app.example:8080", "http://app.example:8080/#s/session-1"},
		{"local", "http://localhost:8080", "", "http://localhost:8080/app/#s/session-1"},
		{"relative", "", "", "/app/#s/session-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := NewServer(nil, ServerConfig{BaseURL: tc.baseURL, AppHost: tc.appHost})
			if got := server.ntfyClickURL("session-1"); got != tc.want {
				t.Fatalf("click URL = %q, want %q", got, tc.want)
			}
		})
	}
}
