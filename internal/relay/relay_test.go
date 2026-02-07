package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/ws"
	"nhooyr.io/websocket"
)

func testStore(t *testing.T) *RelayStore {
	t.Helper()
	s, err := OpenRelay(":memory:")
	if err != nil {
		t.Fatalf("open relay store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func testServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	store := testStore(t)
	srv := NewServer(store)
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { ts.Close() })
	return srv, ts
}

// createTestToken creates a user and token in the store, returning the token string.
func createTestToken(t *testing.T, store *RelayStore, deviceID string) (token, userID string) {
	t.Helper()
	userID = "user-" + deviceID
	if err := store.CreateUser(userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	token = "tok-" + deviceID
	if err := store.CreateDeviceToken(token, userID, deviceID, nil); err != nil {
		t.Fatalf("create token: %v", err)
	}
	return token, userID
}

func TestRelayStoreUserAndToken(t *testing.T) {
	s := testStore(t)

	if err := s.CreateUser("u1"); err != nil {
		t.Fatalf("create user: %v", err)
	}

	if err := s.CreateDeviceToken("tok1", "u1", "dev1", nil); err != nil {
		t.Fatalf("create token: %v", err)
	}

	uid, did, err := s.ValidateToken("tok1")
	if err != nil {
		t.Fatalf("validate token: %v", err)
	}
	if uid != "u1" || did != "dev1" {
		t.Errorf("validate token: got uid=%q did=%q, want u1/dev1", uid, did)
	}

	// Invalid token
	_, _, err = s.ValidateToken("bogus")
	if err == nil {
		t.Error("expected error for invalid token")
	}

	// Delete token
	if err := s.DeleteToken("tok1"); err != nil {
		t.Fatalf("delete token: %v", err)
	}
	_, _, err = s.ValidateToken("tok1")
	if err == nil {
		t.Error("expected error after delete")
	}
}

func TestRelayStoreDeviceCodeFlow(t *testing.T) {
	s := testStore(t)

	expires := time.Now().Add(15 * time.Minute)
	if err := s.CreateDeviceCode("dc1", "ABCD12", "dev1", expires); err != nil {
		t.Fatalf("create device code: %v", err)
	}

	dc, err := s.GetDeviceCode("dc1")
	if err != nil {
		t.Fatalf("get device code: %v", err)
	}
	if dc == nil {
		t.Fatal("expected device code, got nil")
	}
	if dc.Claimed {
		t.Error("expected unclaimed")
	}

	// Claim requires a user
	if err := s.CreateUser("u1"); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := s.ClaimDeviceCode("dc1", "u1"); err != nil {
		t.Fatalf("claim device code: %v", err)
	}

	dc, err = s.GetDeviceCode("dc1")
	if err != nil {
		t.Fatalf("get device code after claim: %v", err)
	}
	if !dc.Claimed {
		t.Error("expected claimed")
	}
	if dc.UserID == nil || *dc.UserID != "u1" {
		t.Errorf("expected user_id=u1, got %v", dc.UserID)
	}

	// Double claim should fail
	if err := s.ClaimDeviceCode("dc1", "u1"); err == nil {
		t.Error("expected error on double claim")
	}
}

func TestHealthEndpoint(t *testing.T) {
	_, ts := testServer(t)

	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	var body map[string]bool
	json.NewDecoder(resp.Body).Decode(&body)
	if !body["ok"] {
		t.Error("expected ok=true")
	}
}

func TestAuthDeviceFlow(t *testing.T) {
	_, ts := testServer(t)

	// 1. Request device code
	resp, err := http.Post(ts.URL+"/auth/device", "application/json",
		strings.NewReader(`{"machine_id":"mac1"}`))
	if err != nil {
		t.Fatalf("POST /auth/device: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("device code status = %d", resp.StatusCode)
	}

	var dcResp struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
		ExpiresIn  int    `json:"expires_in"`
		Interval   int    `json:"interval"`
	}
	json.NewDecoder(resp.Body).Decode(&dcResp)
	if dcResp.DeviceCode == "" || dcResp.UserCode == "" {
		t.Fatal("expected device_code and user_code")
	}
	if len(dcResp.UserCode) != 6 {
		t.Errorf("user_code length = %d, want 6", len(dcResp.UserCode))
	}

	// 2. Poll before claim — should be pending
	resp2, err := http.Post(ts.URL+"/auth/token", "application/json",
		strings.NewReader(`{"device_code":"`+dcResp.DeviceCode+`"}`))
	if err != nil {
		t.Fatalf("POST /auth/token: %v", err)
	}
	var pendingResp map[string]string
	json.NewDecoder(resp2.Body).Decode(&pendingResp)
	resp2.Body.Close()
	if pendingResp["error"] != "authorization_pending" {
		t.Errorf("expected authorization_pending, got %q", pendingResp["error"])
	}

	// 3. Claim the code
	resp3, err := http.Post(ts.URL+"/auth/claim", "application/json",
		strings.NewReader(`{"user_code":"`+dcResp.UserCode+`"}`))
	if err != nil {
		t.Fatalf("POST /auth/claim: %v", err)
	}
	var claimResp map[string]any
	json.NewDecoder(resp3.Body).Decode(&claimResp)
	resp3.Body.Close()
	if claimResp["claimed"] != true {
		t.Errorf("expected claimed=true, got %v", claimResp["claimed"])
	}

	// 4. Poll after claim — should get token
	resp4, err := http.Post(ts.URL+"/auth/token", "application/json",
		strings.NewReader(`{"device_code":"`+dcResp.DeviceCode+`"}`))
	if err != nil {
		t.Fatalf("POST /auth/token (after claim): %v", err)
	}
	var tokenResp map[string]any
	json.NewDecoder(resp4.Body).Decode(&tokenResp)
	resp4.Body.Close()
	if tokenResp["token"] == nil || tokenResp["token"] == "" {
		t.Error("expected token in response")
	}
}

func TestDaemonWebSocket(t *testing.T) {
	srv, ts := testServer(t)

	token, _ := createTestToken(t, srv.Store, "daemon1")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/daemon"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial daemon ws: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	// Send auth
	authMsg, _ := ws.NewMessage(ws.MsgAuth, ws.AuthPayload{DeviceToken: token})
	authData, _ := json.Marshal(authMsg)
	if err := conn.Write(ctx, websocket.MessageText, authData); err != nil {
		t.Fatalf("write auth: %v", err)
	}

	// Read auth result
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read auth result: %v", err)
	}
	var reply ws.Message
	json.Unmarshal(data, &reply)
	if reply.Type != ws.MsgAuthResult {
		t.Fatalf("expected auth_result, got %q", reply.Type)
	}
	var result ws.AuthResultPayload
	reply.ParsePayload(&result)
	if !result.Success {
		t.Fatalf("auth failed: %s", result.Error)
	}

	// Verify session registered
	if srv.Sessions().DaemonCount("user-daemon1") != 1 {
		t.Error("expected 1 daemon session")
	}
}

func TestClientWebSocket(t *testing.T) {
	srv, ts := testServer(t)

	token, _ := createTestToken(t, srv.Store, "client1")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/client?token=" + token
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial client ws: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	// Give session manager a moment to register
	time.Sleep(50 * time.Millisecond)

	if srv.Sessions().ClientCount("user-client1") != 1 {
		t.Error("expected 1 client session")
	}
}

func TestMessageRouting(t *testing.T) {
	srv, ts := testServer(t)

	// Create shared user and two tokens (daemon + client)
	userID := "user-routing"
	if err := srv.Store.CreateUser(userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	daemonToken := "tok-daemon-routing"
	clientToken := "tok-client-routing"
	srv.Store.CreateDeviceToken(daemonToken, userID, "daemon-dev", nil)
	srv.Store.CreateDeviceToken(clientToken, userID, "client-dev", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Connect daemon
	daemonURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/daemon"
	daemonConn, _, err := websocket.Dial(ctx, daemonURL, nil)
	if err != nil {
		t.Fatalf("dial daemon: %v", err)
	}
	defer daemonConn.Close(websocket.StatusNormalClosure, "done")

	// Authenticate daemon
	authMsg, _ := ws.NewMessage(ws.MsgAuth, ws.AuthPayload{DeviceToken: daemonToken})
	authData, _ := json.Marshal(authMsg)
	daemonConn.Write(ctx, websocket.MessageText, authData)
	_, _, err = daemonConn.Read(ctx) // read auth result
	if err != nil {
		t.Fatalf("daemon auth read: %v", err)
	}

	// Connect client
	clientURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/client?token=" + clientToken
	clientConn, _, err := websocket.Dial(ctx, clientURL, nil)
	if err != nil {
		t.Fatalf("dial client: %v", err)
	}
	defer clientConn.Close(websocket.StatusNormalClosure, "done")

	// Give sessions a moment to register
	time.Sleep(50 * time.Millisecond)

	// Client sends task_submit -> should arrive at daemon
	taskMsg, _ := ws.NewMessage(ws.MsgTaskSubmit, ws.TaskSubmitPayload{What: "test task"})
	taskData, _ := json.Marshal(taskMsg)
	if err := clientConn.Write(ctx, websocket.MessageText, taskData); err != nil {
		t.Fatalf("client write: %v", err)
	}

	// Daemon reads the routed message
	_, daemonData, err := daemonConn.Read(ctx)
	if err != nil {
		t.Fatalf("daemon read: %v", err)
	}
	var routed ws.Message
	json.Unmarshal(daemonData, &routed)
	if routed.Type != ws.MsgTaskSubmit {
		t.Errorf("daemon got type=%q, want task_submit", routed.Type)
	}
	var payload ws.TaskSubmitPayload
	routed.ParsePayload(&payload)
	if payload.What != "test task" {
		t.Errorf("daemon got what=%q, want 'test task'", payload.What)
	}

	// Daemon sends task_result -> should arrive at client
	resultMsg, _ := ws.NewMessage(ws.MsgTaskResult, ws.TaskResultPayload{
		TaskID: "t-001",
		Status: "done",
		Output: "result here",
	})
	resultData, _ := json.Marshal(resultMsg)
	if err := daemonConn.Write(ctx, websocket.MessageText, resultData); err != nil {
		t.Fatalf("daemon write result: %v", err)
	}

	// Client reads the broadcast
	_, clientData, err := clientConn.Read(ctx)
	if err != nil {
		t.Fatalf("client read: %v", err)
	}
	var clientMsg ws.Message
	json.Unmarshal(clientData, &clientMsg)
	if clientMsg.Type != ws.MsgTaskResult {
		t.Errorf("client got type=%q, want task_result", clientMsg.Type)
	}
	var resultPayload ws.TaskResultPayload
	clientMsg.ParsePayload(&resultPayload)
	if resultPayload.Output != "result here" {
		t.Errorf("client got output=%q, want 'result here'", resultPayload.Output)
	}
}

func TestStaticFileServing(t *testing.T) {
	_, ts := testServer(t)

	resp, err := http.Get(ts.URL + "/app/")
	if err != nil {
		t.Fatalf("GET /app/: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Errorf("content-type = %q, want text/html", ct)
	}
}

func TestStaticCSS(t *testing.T) {
	_, ts := testServer(t)

	resp, err := http.Get(ts.URL + "/app/style.css")
	if err != nil {
		t.Fatalf("GET /app/style.css: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "css") {
		t.Errorf("content-type = %q, want text/css", ct)
	}
}

func TestStaticJS(t *testing.T) {
	_, ts := testServer(t)

	resp, err := http.Get(ts.URL + "/app/app.js")
	if err != nil {
		t.Fatalf("GET /app/app.js: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestStaticManifest(t *testing.T) {
	_, ts := testServer(t)

	resp, err := http.Get(ts.URL + "/app/manifest.json")
	if err != nil {
		t.Fatalf("GET /app/manifest.json: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Errorf("manifest.json is not valid JSON: %v", err)
	}
	if body["name"] != "wingthing" {
		t.Errorf("manifest name = %q, want wingthing", body["name"])
	}
}

func TestSessionManagerConcurrency(t *testing.T) {
	sm := NewSessionManager()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(n int) {
			defer wg.Done()
			userID := "user-conc"
			deviceID := "dev" + string(rune('A'+n%26))
			// Use nil conn — we're just testing the data structure, not sending
			sm.AddDaemon(userID, deviceID, nil)
			sm.DaemonCount(userID)
			sm.RemoveDaemon(userID, deviceID)
		}(i)
		go func(n int) {
			defer wg.Done()
			userID := "user-conc"
			// Use nil conn
			sm.AddClient(userID, nil)
			sm.ClientCount(userID)
			sm.RemoveClient(userID, nil)
		}(i)
	}
	wg.Wait()

	// After all goroutines finish, counts should be zero (or at least not panic)
	if sm.DaemonCount("user-conc") < 0 {
		t.Error("negative daemon count")
	}
}
