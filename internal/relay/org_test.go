package relay

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func testServerWithSession(t *testing.T) (*Server, *httptest.Server, *http.Client, string) {
	t.Helper()
	store := testStore(t)
	srv := NewServer(store, ServerConfig{})
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { ts.Close() })

	userID := "user-org-test"
	store.CreateUser(userID)
	token := "session-org-test"
	store.CreateSession(token, userID, time.Now().Add(time.Hour))

	jar := &testCookieJar{cookies: map[string][]*http.Cookie{}}
	u := ts.URL
	jar.cookies[u] = []*http.Cookie{{Name: "wt_session", Value: token}}
	client := &http.Client{Jar: jar}

	return srv, ts, client, userID
}

// testCookieJar is a minimal cookie jar for tests.
type testCookieJar struct {
	cookies map[string][]*http.Cookie
}

func (j *testCookieJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	key := u.Scheme + "://" + u.Host
	j.cookies[key] = append(j.cookies[key], cookies...)
}

func (j *testCookieJar) Cookies(u *url.URL) []*http.Cookie {
	key := u.Scheme + "://" + u.Host
	return j.cookies[key]
}

func TestCreateOrgLimit(t *testing.T) {
	_, ts, client, _ := testServerWithSession(t)

	// Create 5 orgs — all should succeed
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("org-%d", i)
		resp, err := client.Post(ts.URL+"/api/orgs", "application/json",
			strings.NewReader(`{"name":"`+name+`"}`))
		if err != nil {
			t.Fatalf("POST /api/orgs #%d: %v", i, err)
		}
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("org #%d: status = %d, want 201", i, resp.StatusCode)
		}
		resp.Body.Close()
	}

	// 6th org should fail with 403
	resp, err := client.Post(ts.URL+"/api/orgs", "application/json",
		strings.NewReader(`{"name":"org-too-many"}`))
	if err != nil {
		t.Fatalf("POST /api/orgs #6: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("6th org: status = %d, want 403", resp.StatusCode)
	}
	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body)
	if !strings.Contains(body["error"], "up to 5") {
		t.Errorf("error = %q, want message about 5 org limit", body["error"])
	}
}

func TestCreateOrgSlugCollision(t *testing.T) {
	_, ts, client, _ := testServerWithSession(t)

	resp, err := client.Post(ts.URL+"/api/orgs", "application/json",
		strings.NewReader(`{"name":"my team"}`))
	if err != nil {
		t.Fatalf("POST /api/orgs: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("first create: status = %d, want 201", resp.StatusCode)
	}

	// Same name again — slug collision
	resp, err = client.Post(ts.URL+"/api/orgs", "application/json",
		strings.NewReader(`{"name":"my team"}`))
	if err != nil {
		t.Fatalf("POST /api/orgs (dup): %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("duplicate slug: status = %d, want 409", resp.StatusCode)
	}
}

func TestListOrgsIncludesInvited(t *testing.T) {
	store := testStore(t)
	srv := NewServer(store, ServerConfig{})
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { ts.Close() })

	// Owner creates an org
	ownerID := "owner-1"
	store.CreateUser(ownerID)
	store.CreateOrg("org-1", "Owner Org", "owner-org", ownerID)
	store.DB().Exec("UPDATE orgs SET max_seats = 10 WHERE id = 'org-1'")

	// Member gets added
	memberID := "member-1"
	store.CreateUser(memberID)
	store.AddOrgMember("org-1", memberID, "member")

	// Member creates their own org
	store.CreateOrg("org-2", "Member Org", "member-org", memberID)

	// Set up session for member
	memberToken := "session-member"
	store.CreateSession(memberToken, memberID, time.Now().Add(time.Hour))
	jar := &testCookieJar{cookies: map[string][]*http.Cookie{}}
	jar.cookies[ts.URL] = []*http.Cookie{{Name: "wt_session", Value: memberToken}}
	client := &http.Client{Jar: jar}

	resp, err := client.Get(ts.URL + "/api/orgs")
	if err != nil {
		t.Fatalf("GET /api/orgs: %v", err)
	}
	defer resp.Body.Close()

	var orgs []map[string]any
	json.NewDecoder(resp.Body).Decode(&orgs)

	if len(orgs) != 2 {
		t.Fatalf("expected 2 orgs, got %d", len(orgs))
	}

	// Check roles
	slugRoles := map[string]bool{}
	for _, o := range orgs {
		slug := o["slug"].(string)
		isOwner := o["is_owner"].(bool)
		slugRoles[slug] = isOwner
	}
	if slugRoles["owner-org"] != false {
		t.Error("member should not be owner of owner-org")
	}
	if slugRoles["member-org"] != true {
		t.Error("member should be owner of member-org")
	}
}

func TestCountOrgsOwnedByUser(t *testing.T) {
	store := testStore(t)

	userID := "count-test-user"
	store.CreateUser(userID)

	otherID := "other-user"
	store.CreateUser(otherID)

	// Initially zero
	count, err := store.CountOrgsOwnedByUser(userID)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("initial count = %d, want 0", count)
	}

	// Create 3 orgs for user
	for i := 0; i < 3; i++ {
		store.CreateOrg(fmt.Sprintf("org-%d", i), fmt.Sprintf("Org %d", i), fmt.Sprintf("org-%d", i), userID)
	}
	// Create 1 org for other user
	store.CreateOrg("other-org", "Other", "other-org", otherID)

	count, err = store.CountOrgsOwnedByUser(userID)
	if err != nil {
		t.Fatalf("count after create: %v", err)
	}
	if count != 3 {
		t.Errorf("count = %d, want 3", count)
	}

	// Other user's count should be 1
	count, _ = store.CountOrgsOwnedByUser(otherID)
	if count != 1 {
		t.Errorf("other count = %d, want 1", count)
	}
}
