package relay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestInviteFlowCookieIsHostOnlyOnSplitHosts(t *testing.T) {
	store := testStore(t)
	srv := NewServer(store, ServerConfig{
		BaseURL: "https://login.roost.example",
		AppHost: "app.roost.example",
	})
	mustTest(t, store.CreateUser("invite-owner"))
	mustTest(t, store.CreateOrg("invite-org", "Invite Org", "invite-org", "invite-owner"))
	mustTest(t, store.CreateOrgInvite("invite-id", "invite-org", "member@example.test", "invite-token", "invite-owner", "member"))

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "https://login.roost.example/invite/invite-token", nil)
	srv.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("invite page status = %d, want 200", recorder.Code)
	}
	var inviteCookie *http.Cookie
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == "invite_token" {
			inviteCookie = cookie
			break
		}
	}
	if inviteCookie == nil {
		t.Fatal("invite flow cookie was not set")
	}
	if inviteCookie.Domain != "" {
		t.Fatalf("invite cookie domain = %q, want host-only", inviteCookie.Domain)
	}
	if !inviteCookie.HttpOnly || !inviteCookie.Secure || inviteCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("invite cookie flags = %#v", inviteCookie)
	}
}

func TestOrgInviteBatchIsBoundedAndDeduplicated(t *testing.T) {
	srv, ts, client, ownerID := testServerWithSession(t)
	mustTest(t, srv.Store.CreateOrg("batch-org", "Batch Org", "batch-org", ownerID))
	mustTestExec(t, srv.Store.DB(), "UPDATE orgs SET max_seats = 200 WHERE id = 'batch-org'")

	emails := make([]string, maxOrgInviteBatch+1)
	for index := range emails {
		emails[index] = fmt.Sprintf("member-%d@example.test", index)
	}
	body, err := json.Marshal(map[string]any{"emails": emails})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Post(ts.URL+"/api/orgs/batch-org/invite", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	closeTestBody(t, response.Body)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized invite status = %d, want 400", response.StatusCode)
	}

	body, err = json.Marshal(map[string]any{"emails": []string{"same@example.test", "SAME@example.test"}})
	if err != nil {
		t.Fatal(err)
	}
	response, err = client.Post(ts.URL+"/api/orgs/batch-org/invite", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestBody(t, response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("deduplicated invite status = %d, want 200", response.StatusCode)
	}
	invites, err := srv.Store.ListPendingInvites("batch-org")
	if err != nil {
		t.Fatal(err)
	}
	if len(invites) != 1 || invites[0].Email != "same@example.test" {
		t.Fatalf("pending invites = %#v, want one normalized invite", invites)
	}
}

func testServerWithSession(t *testing.T) (*Server, *httptest.Server, *http.Client, string) {
	t.Helper()
	store := testStore(t)
	srv := NewServer(store, ServerConfig{})
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { ts.Close() })

	userID := "user-org-test"
	mustTest(t, store.CreateUser(userID))
	token := "session-org-test"
	mustTest(t, store.CreateSession(token, userID, time.Now().Add(time.Hour)))

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
		closeTestBody(t, resp.Body)
	}

	// 6th org should fail with 403
	resp, err := client.Post(ts.URL+"/api/orgs", "application/json",
		strings.NewReader(`{"name":"org-too-many"}`))
	if err != nil {
		t.Fatalf("POST /api/orgs #6: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("6th org: status = %d, want 403", resp.StatusCode)
	}
	var body map[string]string
	decodeTestJSON(t, resp.Body, &body)
	if !strings.Contains(body["error"], "up to 5") {
		t.Errorf("error = %q, want message about 5 org limit", body["error"])
	}
}

func TestCreateOrgDuplicateNameAllowed(t *testing.T) {
	_, ts, client, _ := testServerWithSession(t)

	resp, err := client.Post(ts.URL+"/api/orgs", "application/json",
		strings.NewReader(`{"name":"my team"}`))
	if err != nil {
		t.Fatalf("POST /api/orgs: %v", err)
	}
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("first create: status = %d, want 201", resp.StatusCode)
	}

	// Same name again — should succeed (slugs are not unique)
	resp, err = client.Post(ts.URL+"/api/orgs", "application/json",
		strings.NewReader(`{"name":"my team"}`))
	if err != nil {
		t.Fatalf("POST /api/orgs (dup): %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("duplicate name: status = %d, want 201", resp.StatusCode)
	}
}

func TestListOrgsIncludesInvited(t *testing.T) {
	store := testStore(t)
	srv := NewServer(store, ServerConfig{})
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { ts.Close() })

	// Owner creates an org
	ownerID := "owner-1"
	mustTest(t, store.CreateUser(ownerID))
	mustTest(t, store.CreateOrg("org-1", "Owner Org", "owner-org", ownerID))
	mustTestExec(t, store.DB(), "UPDATE orgs SET max_seats = 10 WHERE id = 'org-1'")

	// Member gets added
	memberID := "member-1"
	mustTest(t, store.CreateUser(memberID))
	mustTest(t, store.AddOrgMember("org-1", memberID, "member"))

	// Member creates their own org
	mustTest(t, store.CreateOrg("org-2", "Member Org", "member-org", memberID))

	// Set up session for member
	memberToken := "session-member"
	mustTest(t, store.CreateSession(memberToken, memberID, time.Now().Add(time.Hour)))
	jar := &testCookieJar{cookies: map[string][]*http.Cookie{}}
	jar.cookies[ts.URL] = []*http.Cookie{{Name: "wt_session", Value: memberToken}}
	client := &http.Client{Jar: jar}

	resp, err := client.Get(ts.URL + "/api/orgs")
	if err != nil {
		t.Fatalf("GET /api/orgs: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	var orgs []map[string]any
	decodeTestJSON(t, resp.Body, &orgs)

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

func TestConsumeInvite_EmailMismatchDoesNotBurnToken(t *testing.T) {
	store := testStore(t)
	srv := NewServer(store, ServerConfig{})
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { ts.Close() })

	// Create org and invite for alice@test.com
	ownerID := "owner-inv"
	mustTest(t, store.CreateUser(ownerID))
	mustTest(t, store.CreateOrg("org-inv", "Invite Org", "invite-org", ownerID))
	mustTestExec(t, store.DB(), "UPDATE orgs SET max_seats = 10 WHERE id = 'org-inv'")
	mustTest(t, store.CreateOrgInvite("inv-1", "org-inv", "alice@test.com", "tok-123", ownerID, "member"))

	// Bob logs in and tries to consume alice's invite
	bobID := "bob-user"
	mustTest(t, store.CreateUser(bobID))
	mustTest(t, store.UpdateUserEmail(bobID, "bob@test.com"))
	bobToken := "session-bob"
	mustTest(t, store.CreateSession(bobToken, bobID, time.Now().Add(time.Hour)))
	jar := &testCookieJar{cookies: map[string][]*http.Cookie{}}
	jar.cookies[ts.URL] = []*http.Cookie{{Name: "wt_session", Value: bobToken}}
	bobClient := &http.Client{Jar: jar}

	// POST should fail with email mismatch
	resp, err := bobClient.Post(ts.URL+"/invite/tok-123", "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatalf("POST /invite: %v", err)
	}
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("wrong-email consume: status = %d, want 403", resp.StatusCode)
	}

	// Token should NOT be consumed — alice can still use it
	inv, err := store.GetInviteByToken("tok-123")
	if err != nil {
		t.Fatalf("GetInviteByToken: %v", err)
	}
	if inv == nil {
		t.Fatal("invite should still exist")
	}
	if inv.ClaimedAt != nil {
		t.Error("invite should not be claimed after email mismatch")
	}
}

func TestRevokeInviteCannotCrossOrgBoundary(t *testing.T) {
	store := testStore(t)
	srv := NewServer(store, ServerConfig{})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	for _, userID := range []string{"owner-a", "owner-b"} {
		mustTest(t, store.CreateUser(userID))
		mustTest(t, store.CreateSession("session-"+userID, userID, time.Now().Add(time.Hour)))
	}
	mustTest(t, store.CreateOrg("org-a", "Org A", "org-a", "owner-a"))
	mustTest(t, store.CreateOrg("org-b", "Org B", "org-b", "owner-b"))
	mustTest(t, store.CreateOrgInvite("invite-b", "org-b", "member@example.test", "token-b", "owner-b", "member"))

	clientFor := func(userID string) *http.Client {
		jar := &testCookieJar{cookies: map[string][]*http.Cookie{}}
		jar.cookies[ts.URL] = []*http.Cookie{{Name: "wt_session", Value: "session-" + userID}}
		return &http.Client{Jar: jar}
	}
	revoke := func(client *http.Client, orgID string) *http.Response {
		t.Helper()
		resp, err := client.Post(
			ts.URL+"/api/orgs/"+orgID+"/invites/token-b/revoke",
			"application/json",
			strings.NewReader("{}"),
		)
		if err != nil {
			t.Fatalf("revoke invite: %v", err)
		}
		return resp
	}

	resp := revoke(clientFor("owner-a"), "org-a")
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-org revoke status = %d, want 404", resp.StatusCode)
	}
	invite, err := store.GetInviteByToken("token-b")
	if err != nil {
		t.Fatal(err)
	}
	if invite == nil || invite.OrgID != "org-b" {
		t.Fatal("cross-org revoke removed the other tenant's invite")
	}

	resp = revoke(clientFor("owner-b"), "org-b")
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("same-org revoke status = %d, want 200", resp.StatusCode)
	}
	invite, err = store.GetInviteByToken("token-b")
	if err != nil {
		t.Fatal(err)
	}
	if invite != nil {
		t.Fatal("same-org revoke left the pending invite behind")
	}
}

func TestDeleteOrgRemovesLiveMemberSubscriptions(t *testing.T) {
	store := testStore(t)
	srv := NewServer(store, ServerConfig{})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	for _, userID := range []string{"delete-owner", "delete-member"} {
		mustTest(t, store.CreateUser(userID))
	}
	mustTest(t, store.CreateSession("delete-owner-session", "delete-owner", time.Now().Add(time.Hour)))
	mustTest(t, store.CreateOrgWithSeats("delete-org", "Delete Org", "delete-org", "delete-owner", 2))
	mustTest(t, store.AddOrgMember("delete-org", "delete-member", "member"))

	ownerEvents := make(chan WingEvent, 1)
	memberEvents := make(chan WingEvent, 1)
	srv.Wings.Subscribe("delete-owner", []string{"delete-org"}, ownerEvents)
	srv.Wings.Subscribe("delete-member", []string{"delete-org"}, memberEvents)
	t.Cleanup(func() {
		srv.Wings.Unsubscribe("delete-owner", ownerEvents)
		srv.Wings.Unsubscribe("delete-member", memberEvents)
	})

	jar := &testCookieJar{cookies: map[string][]*http.Cookie{}}
	jar.cookies[ts.URL] = []*http.Cookie{{Name: "wt_session", Value: "delete-owner-session"}}
	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/orgs/delete-org", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Jar: jar}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete org status = %d, want 200", resp.StatusCode)
	}

	srv.Wings.subMu.RLock()
	remaining := len(srv.Wings.orgSubs["delete-org"])
	srv.Wings.subMu.RUnlock()
	if remaining != 0 {
		t.Fatalf("deleted org still has %d live subscriptions", remaining)
	}
	for name, events := range map[string]<-chan WingEvent{"owner": ownerEvents, "member": memberEvents} {
		select {
		case event := <-events:
			if event.Type != "org.changed" {
				t.Errorf("%s event type = %q, want org.changed", name, event.Type)
			}
		default:
			t.Errorf("%s subscriber did not receive org.changed", name)
		}
	}
}

func TestDeleteOrgWithActiveSubscriptionReturnsCompatibilityError(t *testing.T) {
	store := testStore(t)
	srv := NewServer(store, ServerConfig{})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	mustTest(t, store.CreateUser("subscribed-owner"))
	mustTest(t, store.CreateSession("subscribed-owner-session", "subscribed-owner", time.Now().Add(time.Hour)))
	mustTest(t, store.CreateOrg("subscribed-org", "Subscribed Org", "subscribed-org", "subscribed-owner"))
	orgID := "subscribed-org"
	sub := &Subscription{ID: "subscribed-org-plan", OrgID: &orgID, Plan: "team_monthly", Status: "active", Seats: 1}
	if _, err := store.ActivateOrgSubscription(
		sub,
		orgID,
		[]*Entitlement{{ID: "subscribed-owner-ent", UserID: "subscribed-owner", SubscriptionID: sub.ID}},
	); err != nil {
		t.Fatal(err)
	}

	jar := &testCookieJar{cookies: map[string][]*http.Cookie{}}
	jar.cookies[ts.URL] = []*http.Cookie{{Name: "wt_session", Value: "subscribed-owner-session"}}
	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/orgs/subscribed-org", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Jar: jar}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("delete subscribed org status = %d, want 400", resp.StatusCode)
	}
	var body map[string]any
	decodeTestJSON(t, resp.Body, &body)
	if body["error"] != "cancel the subscription first" {
		t.Fatalf("delete subscribed org error = %#v", body["error"])
	}
	org, err := store.GetOrgByID(orgID)
	if err != nil || org == nil {
		t.Fatalf("subscribed org after rejected delete = %#v, %v", org, err)
	}
}

func TestCountOrgsOwnedByUser(t *testing.T) {
	store := testStore(t)

	userID := "count-test-user"
	mustTest(t, store.CreateUser(userID))

	otherID := "other-user"
	mustTest(t, store.CreateUser(otherID))

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
		mustTest(t, store.CreateOrg(fmt.Sprintf("org-%d", i), fmt.Sprintf("Org %d", i), fmt.Sprintf("org-%d", i), userID))
	}
	// Create 1 org for other user
	mustTest(t, store.CreateOrg("other-org", "Other", "other-org", otherID))

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

func TestRoostAutoCreateOrg(t *testing.T) {
	store := testStore(t)
	userID := "first-user"
	mustTest(t, store.CreateUser(userID))

	// Org doesn't exist yet
	org, err := store.ResolveOrg("myorg", userID)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if org != nil {
		t.Fatal("expected nil org before creation")
	}

	// Simulate roost auto-create: first user creates the org
	mustTest(t, store.CreateOrg("myorg", "myorg", "myorg", userID))
	mustTestExec(t, store.DB(), "UPDATE orgs SET max_seats = 9999 WHERE id = ?", "myorg")

	org, err = store.ResolveOrg("myorg", userID)
	if err != nil {
		t.Fatalf("resolve after create: %v", err)
	}
	if org == nil {
		t.Fatal("expected org after creation")
	}

	// First user is owner (from CreateOrg)
	role := store.GetOrgMemberRole(org.ID, userID)
	if role != "owner" {
		t.Errorf("first user role = %q, want owner", role)
	}
}

func TestRoostAutoAddMember(t *testing.T) {
	store := testStore(t)
	ownerID := "owner"
	memberID := "new-member"
	mustTest(t, store.CreateUser(ownerID))
	mustTest(t, store.CreateUser(memberID))

	mustTest(t, store.CreateOrg("myorg", "myorg", "myorg", ownerID))
	mustTestExec(t, store.DB(), "UPDATE orgs SET max_seats = 9999 WHERE id = ?", "myorg")

	// New user is not a member yet
	role := store.GetOrgMemberRole("myorg", memberID)
	if role != "" {
		t.Errorf("expected empty role, got %q", role)
	}

	// Simulate roost auto-join
	mustTest(t, store.AddOrgMember("myorg", memberID, "member"))

	role = store.GetOrgMemberRole("myorg", memberID)
	if role != "member" {
		t.Errorf("role = %q, want member", role)
	}
}

func TestAddOrgMemberIsIdempotentWhenOrgIsFull(t *testing.T) {
	store := testStore(t)
	mustTest(t, store.CreateUser("full-owner"))
	mustTest(t, store.CreateOrg("full-org", "Full Org", "full-org", "full-owner"))

	if err := store.AddOrgMember("full-org", "full-owner", "member"); err != nil {
		t.Fatalf("re-adding existing owner to full org: %v", err)
	}
	if role := store.GetOrgMemberRole("full-org", "full-owner"); role != "owner" {
		t.Fatalf("idempotent add changed existing role to %q", role)
	}
}
