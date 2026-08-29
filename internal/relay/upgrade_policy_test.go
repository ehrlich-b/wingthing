package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func planTestClient(t *testing.T, cfg ServerConfig) (*RelayStore, *httptest.Server, *http.Client, string) {
	t.Helper()
	store := testStore(t)
	server := NewServer(store, cfg)
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)

	userID := "plan-test-user"
	token := "plan-test-session"
	mustTest(t, store.CreateUser(userID))
	mustTest(t, store.CreateSession(token, userID, time.Now().Add(time.Hour)))
	jar := &testCookieJar{cookies: map[string][]*http.Cookie{
		httpServer.URL: {{Name: "wt_session", Value: token}},
	}}
	return store, httpServer, &http.Client{Jar: jar}, userID
}

func TestDirectFreeDeploymentCannotSelfGrantPlans(t *testing.T) {
	store, server, client, userID := planTestClient(t, ServerConfig{RelayPolicy: RelayPolicyDirectFree})

	meResponse, err := client.Get(server.URL + "/api/app/me")
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestBody(t, meResponse.Body)
	var me map[string]any
	decodeTestJSON(t, meResponse.Body, &me)
	if enabled, ok := me["self_service_plans"].(bool); !ok || enabled {
		t.Fatalf("self_service_plans = %#v, want false", me["self_service_plans"])
	}

	response, err := client.Post(server.URL+"/api/app/upgrade", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	closeTestBody(t, response.Body)
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("personal upgrade status = %d, want 403", response.StatusCode)
	}
	if store.IsUserPro(userID) {
		t.Fatal("direct-free personal upgrade granted Pro")
	}

	mustTest(t, store.CreateOrg("plan-test-org", "Plan Test Org", "plan-test-org", userID))
	response, err = client.Post(server.URL+"/api/orgs/plan-test-org/upgrade", "application/json", strings.NewReader(`{"seats":5}`))
	if err != nil {
		t.Fatal(err)
	}
	closeTestBody(t, response.Body)
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("org upgrade status = %d, want 403", response.StatusCode)
	}
	if subscription, err := store.GetActiveOrgSubscription("plan-test-org"); err != nil || subscription != nil {
		t.Fatalf("direct-free org subscription = %#v, %v", subscription, err)
	}
}

func TestLegacyDeploymentRetainsSelfServicePlanBehavior(t *testing.T) {
	store, server, client, userID := planTestClient(t, ServerConfig{})

	response, err := client.Post(server.URL+"/api/app/upgrade", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestBody(t, response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("personal upgrade status = %d, want 200", response.StatusCode)
	}
	if !store.IsUserPro(userID) {
		t.Fatal("legacy personal upgrade did not grant Pro")
	}

	mustTest(t, store.CreateOrg("legacy-plan-org", "Legacy Plan Org", "legacy-plan-org", userID))
	response, err = client.Post(server.URL+"/api/orgs/legacy-plan-org/upgrade", "application/json", strings.NewReader(`{"plan":"team_monthly","seats":3}`))
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestBody(t, response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("org upgrade status = %d, want 200", response.StatusCode)
	}
	subscription, err := store.GetActiveOrgSubscription("legacy-plan-org")
	if err != nil || subscription == nil || subscription.Seats != 3 {
		t.Fatalf("legacy org subscription = %#v, %v", subscription, err)
	}
}

func TestLegacyUpgradeRepairsMissingPersonalEntitlement(t *testing.T) {
	store, server, client, userID := planTestClient(t, ServerConfig{})
	subscription := &Subscription{
		ID:     "orphaned-active-subscription",
		UserID: &userID,
		Plan:   "pro_monthly",
		Status: "active",
		Seats:  1,
	}
	mustTest(t, store.CreateSubscription(subscription))
	if store.IsUserPro(userID) {
		t.Fatal("subscription without entitlement unexpectedly grants Pro")
	}

	response, err := client.Post(server.URL+"/api/app/upgrade", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestBody(t, response.Body)
	if response.StatusCode != http.StatusOK {
		var body map[string]any
		_ = json.NewDecoder(response.Body).Decode(&body)
		t.Fatalf("repair upgrade status = %d, body = %#v", response.StatusCode, body)
	}
	if !store.IsUserPro(userID) {
		t.Fatal("idempotent upgrade did not repair the missing entitlement")
	}
}
