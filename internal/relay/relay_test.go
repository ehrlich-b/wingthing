package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
	srv := NewServer(store, ServerConfig{})
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

func TestVoteAPI(t *testing.T) {
	srv, ts := testServer(t)
	token, _ := createTestToken(t, srv.Store, "dev1")

	// Create a post to vote on
	post := makeEmbedding("post")
	if err := srv.Store.CreateSocialEmbedding(post); err != nil {
		t.Fatalf("create post: %v", err)
	}

	// Vote without auth — should fail
	resp, err := http.Post(ts.URL+"/api/vote", "application/json",
		strings.NewReader(`{"post_id":"`+post.ID+`"}`))
	if err != nil {
		t.Fatalf("POST /api/vote: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthed vote status = %d, want 401", resp.StatusCode)
	}

	// Vote with auth
	req, _ := http.NewRequest("POST", ts.URL+"/api/vote", strings.NewReader(`{"post_id":"`+post.ID+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/vote (authed): %v", err)
	}
	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("authed vote status = %d, want 200", resp.StatusCode)
	}
	if result["ok"] != true {
		t.Errorf("expected ok=true, got %v", result["ok"])
	}
	if result["upvotes"] != float64(1) {
		t.Errorf("upvotes = %v, want 1", result["upvotes"])
	}

	// Vote again (idempotent) — count stays 1
	req, _ = http.NewRequest("POST", ts.URL+"/api/vote", strings.NewReader(`{"post_id":"`+post.ID+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/vote (dup): %v", err)
	}
	json.NewDecoder(resp.Body).Decode(&result)
	resp.Body.Close()
	if result["upvotes"] != float64(1) {
		t.Errorf("dup upvotes = %v, want 1", result["upvotes"])
	}
}

func TestCommentAPI(t *testing.T) {
	srv, ts := testServer(t)
	token, _ := createTestToken(t, srv.Store, "dev1")

	post := makeEmbedding("post")
	if err := srv.Store.CreateSocialEmbedding(post); err != nil {
		t.Fatalf("create post: %v", err)
	}

	// Comment with auth
	req, _ := http.NewRequest("POST", ts.URL+"/api/comment",
		strings.NewReader(`{"post_id":"`+post.ID+`","content":"great post"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/comment: %v", err)
	}
	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("comment status = %d, want 200", resp.StatusCode)
	}
	if result["ok"] != true {
		t.Errorf("expected ok=true")
	}
	commentID, ok := result["comment_id"].(string)
	if !ok || commentID == "" {
		t.Fatal("expected comment_id in response")
	}

	// List comments
	resp, err = http.Get(ts.URL + "/api/comments?post_id=" + post.ID)
	if err != nil {
		t.Fatalf("GET /api/comments: %v", err)
	}
	var comments []map[string]any
	json.NewDecoder(resp.Body).Decode(&comments)
	resp.Body.Close()
	if len(comments) != 1 {
		t.Fatalf("comments count = %d, want 1", len(comments))
	}
	if comments[0]["Content"] != "great post" {
		t.Errorf("comment content = %v, want 'great post'", comments[0]["Content"])
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

func TestStaticSW(t *testing.T) {
	_, ts := testServer(t)

	resp, err := http.Get(ts.URL + "/app/sw.js")
	if err != nil {
		t.Fatalf("GET /app/sw.js: %v", err)
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
