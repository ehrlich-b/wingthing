package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestInternalSessionAddsEmailAndExactOrgRolesCompatibly(t *testing.T) {
	store := testStore(t)
	if err := store.CreateUser("member"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateUserEmail("member", "member@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateUser("owner"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateOrg("org-1", "Org", "org", "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec("UPDATE orgs SET max_seats = 2 WHERE id = ?", "org-1"); err != nil {
		t.Fatal(err)
	}
	if err := store.AddOrgMember("org-1", "member", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSession("session", "member", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	server := NewServer(store, ServerConfig{})
	request := httptest.NewRequest(http.MethodGet, "/internal/sessions/session", nil)
	request.SetPathValue("token", "session")
	recorder := httptest.NewRecorder()
	server.handleInternalSession(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var current SessionValidation
	if err := json.Unmarshal(recorder.Body.Bytes(), &current); err != nil {
		t.Fatal(err)
	}
	if current.Email != "member@example.com" || current.OrgRoles["org-1"] != "admin" || len(current.OrgIDs) != 1 {
		t.Fatalf("current session identity = %#v", current)
	}
	var legacy struct {
		UserID string   `json:"user_id"`
		OrgIDs []string `json:"org_ids"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &legacy); err != nil || legacy.UserID != "member" || len(legacy.OrgIDs) != 1 {
		t.Fatalf("N-1 session decode = %#v err=%v", legacy, err)
	}
}

func TestEntitlementCachePreservesNMinusOneLoginRelayBehavior(t *testing.T) {
	login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// This is the complete shape emitted by an N-1 login node: tier fields
		// only and no capability header. Its relay policy allowed every user.
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"user_id": "pro-user", "tier": "pro"},
		})
	}))
	defer login.Close()

	cache := NewEntitlementCache(login.URL)
	cache.fetch(context.Background())
	for _, userID := range []string{"pro-user", "free-user-omitted-by-old-query"} {
		if access := cache.GetRelayAccess(userID); !access.Allowed || access.Reason != "legacy-login" {
			t.Fatalf("N-1 relay access for %q = %#v", userID, access)
		}
	}
	if allowed, known := cache.GetEnrollment("pro-user"); allowed || known {
		t.Fatalf("N-1 login fabricated enrollment: allowed=%v known=%v", allowed, known)
	}
}

func TestEntitlementCachePropagatesInternalSecret(t *testing.T) {
	login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(internalSecretHeader); got != "shared-secret" {
			http.Error(w, "missing internal secret", http.StatusForbidden)
			return
		}
		w.Header().Set(entitlementDecisionVersionHeader, "2")
		_ = json.NewEncoder(w).Encode([]EntitlementEntry{{
			UserID: "user", Tier: "pro", RelayAllowed: true, RelayReason: "pro", Enrolled: true,
		}})
	}))
	defer login.Close()

	cache := NewEntitlementCache(login.URL, "shared-secret")
	cache.fetch(context.Background())
	if access := cache.GetRelayAccess("user"); !access.Allowed || access.Reason != "pro" {
		t.Fatalf("secret-authenticated entitlement fetch = %#v", access)
	}
}

func TestEntitlementCacheUsesVersionedRelayAndEnrollmentDecisions(t *testing.T) {
	login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(entitlementDecisionVersionHeader, "2")
		_ = json.NewEncoder(w).Encode([]EntitlementEntry{
			{UserID: "direct", Tier: "free", RelayAllowed: false, RelayReason: "direct-only-free", Enrolled: true},
			{UserID: "pro", Tier: "pro", RelayAllowed: true, RelayReason: "pro", Enrolled: true},
			{UserID: "outsider", Tier: "free", RelayAllowed: false, RelayReason: "roost-enrollment-required", Enrolled: false},
		})
	}))
	defer login.Close()

	cache := NewEntitlementCache(login.URL)
	cache.fetch(context.Background())
	if access := cache.GetRelayAccess("direct"); access.Allowed || access.Reason != "direct-only-free" {
		t.Fatalf("direct access = %#v", access)
	}
	if access := cache.GetRelayAccess("pro"); !access.Allowed || access.Reason != "pro" {
		t.Fatalf("pro access = %#v", access)
	}
	if access := cache.GetRelayAccess("missing"); access.Allowed || access.Reason != "entitlement-unavailable" {
		t.Fatalf("missing access = %#v", access)
	}
	for userID, want := range map[string]bool{"direct": true, "pro": true, "outsider": false} {
		got, known := cache.GetEnrollment(userID)
		if !known || got != want {
			t.Errorf("enrollment %q = %v, known=%v, want %v", userID, got, known, want)
		}
	}
	if allowed, known := cache.GetEnrollment("missing"); allowed || known {
		t.Fatalf("missing enrollment = %v, known=%v", allowed, known)
	}
}

func TestEntitlementCacheRejectsUnknownDecisionVersion(t *testing.T) {
	version := "2"
	login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(entitlementDecisionVersionHeader, version)
		_ = json.NewEncoder(w).Encode([]EntitlementEntry{
			{UserID: "user", Tier: "pro", RelayAllowed: true, RelayReason: "pro", Enrolled: true},
		})
	}))
	defer login.Close()

	cache := NewEntitlementCache(login.URL)
	cache.fetch(context.Background())
	if access := cache.GetRelayAccess("user"); !access.Allowed || access.Reason != "pro" {
		t.Fatalf("initial access = %#v", access)
	}

	version = "3"
	cache.fetch(context.Background())
	if access := cache.GetRelayAccess("user"); !access.Allowed || access.Reason != "pro" {
		t.Fatalf("unknown version replaced last known-good access: %#v", access)
	}
	if allowed, known := cache.GetEnrollment("user"); !known || !allowed {
		t.Fatalf("unknown version replaced last known-good enrollment: allowed=%v known=%v", allowed, known)
	}

	fresh := NewEntitlementCache(login.URL)
	fresh.fetch(context.Background())
	if access := fresh.GetRelayAccess("user"); access.Allowed || access.Reason != "entitlement-unavailable" {
		t.Fatalf("fresh cache accepted unknown version: %#v", access)
	}
	if allowed, known := fresh.GetEnrollment("user"); allowed || known {
		t.Fatalf("fresh cache accepted unknown enrollment: allowed=%v known=%v", allowed, known)
	}
}

func TestEntitlementCacheRejectsOversizedResponseWithoutReplacingGoodState(t *testing.T) {
	var oversized atomic.Bool
	login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(entitlementDecisionVersionHeader, "2")
		if oversized.Load() {
			_, _ = w.Write([]byte(`[]` + strings.Repeat(" ", maxEntitlementResponseBytes)))
			return
		}
		_ = json.NewEncoder(w).Encode([]EntitlementEntry{
			{UserID: "pro", Tier: "pro", RelayAllowed: true, RelayReason: "pro", Enrolled: true},
		})
	}))
	defer login.Close()

	cache := NewEntitlementCache(login.URL)
	cache.fetch(context.Background())
	oversized.Store(true)
	cache.fetch(context.Background())
	if access := cache.GetRelayAccess("pro"); !access.Allowed || access.Reason != "pro" {
		t.Fatalf("oversized response replaced last known-good state: %#v", access)
	}
}

func TestInternalEntitlementsAdvertisesVersionedDecisions(t *testing.T) {
	store := testStore(t)
	if err := store.CreateUser("existing"); err != nil {
		t.Fatal(err)
	}
	server := NewServer(store, ServerConfig{})
	recorder := httptest.NewRecorder()
	server.handleInternalEntitlements(recorder, httptest.NewRequest(http.MethodGet, "/internal/entitlements", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get(entitlementDecisionVersionHeader); got != "2" {
		t.Fatalf("decision version header = %q", got)
	}
	var entries []EntitlementEntry
	if err := json.Unmarshal(recorder.Body.Bytes(), &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].UserID != "existing" || !entries[0].RelayAllowed || entries[0].RelayReason != "legacy-policy" || !entries[0].Enrolled {
		t.Fatalf("entries = %#v", entries)
	}

	// The response remains additive for an N-1 edge: Go's decoder ignores the
	// new decision fields and still recovers the original user/tier contract.
	var legacyEntries []struct {
		UserID string `json:"user_id"`
		Tier   string `json:"tier"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &legacyEntries); err != nil {
		t.Fatal(err)
	}
	if len(legacyEntries) != 1 || legacyEntries[0].UserID != "existing" || legacyEntries[0].Tier != "free" {
		t.Fatalf("legacy entries = %#v", legacyEntries)
	}
}
