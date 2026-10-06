package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

func TestEntitlementCacheRefetchesAtTTLAndFailsClosedAfterGrace(t *testing.T) {
	var fail, allow atomic.Bool
	var calls atomic.Int32
	allow.Store(true)
	login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if fail.Load() {
			http.Error(w, "unavailable", 503)
			return
		}
		w.Header().Set(entitlementDecisionVersionHeader, "2")
		_ = json.NewEncoder(w).Encode([]EntitlementEntry{{UserID: "user", Tier: "pro", RelayAllowed: allow.Load(), RelayReason: "pro", Enrolled: true}})
	}))
	defer login.Close()
	cache := NewEntitlementCache(login.URL)
	cache.fetch(context.Background())
	age := func(d time.Duration) {
		cache.mu.Lock()
		cache.updatedAt = time.Now().Add(-d)
		cache.retryAt = time.Time{}
		cache.mu.Unlock()
	}
	allow.Store(false)
	age(16 * time.Minute)
	if access := cache.GetRelayAccess("user"); access.Allowed || calls.Load() != 2 {
		t.Fatalf("expired allow was not refetched: %#v calls=%d", access, calls.Load())
	}
	allow.Store(true)
	cache.fetch(context.Background())
	fail.Store(true)
	age(16 * time.Minute)
	if access := cache.GetRelayAccess("user"); !access.Allowed {
		t.Fatalf("bounded grace was not honored: %#v", access)
	}
	age(21 * time.Minute)
	if access := cache.GetRelayAccess("user"); access.Allowed || access.Reason != "entitlement-stale" {
		t.Fatalf("stale paid access survived failed sync: %#v", access)
	}
	if tier := cache.GetTier("user"); tier != "free" {
		t.Fatalf("stale paid tier = %q", tier)
	}
	if allowed, known := cache.GetEnrollment("user"); allowed || known {
		t.Fatal("stale enrollment remained authoritative")
	}
	before := calls.Load()
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() { _ = cache.GetRelayAccess("user") })
	}
	wg.Wait()
	if calls.Load() != before {
		t.Fatal("stale reads caused a retry storm")
	}
	fail.Store(false)
	age(21 * time.Minute)
	if access := cache.GetRelayAccess("user"); !access.Allowed {
		t.Fatalf("healthy refetch did not restore access: %#v", access)
	}
}

func TestEntitlementCacheLegacyAllowAlsoExpires(t *testing.T) {
	cache := NewEntitlementCache("http://127.0.0.1:1")
	cache.initialized = true
	cache.updatedAt = time.Now().Add(-21 * time.Minute)
	cache.retryAt = time.Now().Add(time.Minute)
	if access := cache.GetRelayAccess("legacy"); access.Allowed || access.Reason != "entitlement-stale" {
		t.Fatalf("legacy allow survived TTL: %#v", access)
	}
}

func TestEntitlementCachePaginatesAndPublishesOnlyCompleteSyncs(t *testing.T) {
	store := testStore(t)
	for i := range 5 {
		mustTest(t, store.CreateUser(fmt.Sprintf("user-%d", i)))
	}
	server := NewServer(store, ServerConfig{})
	var fail atomic.Bool
	var calls atomic.Int32
	login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if fail.Load() && r.URL.Query().Get("after") != "" {
			http.Error(w, "failed later page", 503)
			return
		}
		server.handleInternalEntitlements(w, r)
	}))
	defer login.Close()
	cache := NewEntitlementCache(login.URL)
	cache.pageSize = 2
	cache.fetch(context.Background())
	if calls.Load() != 3 || len(cache.tiers) != 5 || !cache.GetRelayAccess("user-4").Allowed {
		t.Fatalf("incomplete paginated sync: calls=%d users=%d", calls.Load(), len(cache.tiers))
	}
	updated := cache.updatedAt
	mustTestExec(t, store.DB(), "DELETE FROM users WHERE id = 'user-0'")
	fail.Store(true)
	cache.fetch(context.Background())
	if !cache.updatedAt.Equal(updated) || len(cache.tiers) != 5 || !cache.GetRelayAccess("user-0").Allowed {
		t.Fatal("failed later page published partial decisions or refreshed their age")
	}
	fail.Store(false)
	cache.fetch(context.Background())
	if cache.GetRelayAccess("user-0").Allowed || len(cache.tiers) != 4 {
		t.Fatal("complete paginated sync retained removed user")
	}
	for _, query := range []string{"limit=1001", "limit=0", "limit=oops", "after=user-0"} {
		rr := httptest.NewRecorder()
		server.handleInternalEntitlements(rr, httptest.NewRequest(http.MethodGet, "/internal/entitlements?"+query, nil))
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("invalid page query %q accepted", query)
		}
	}
}

func TestEntitlementCacheRejectsNonAdvancingPagination(t *testing.T) {
	login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(entitlementDecisionVersionHeader, "2")
		w.Header().Set(entitlementNextHeader, "user")
		_ = json.NewEncoder(w).Encode([]EntitlementEntry{{UserID: "user", Tier: "pro", RelayAllowed: true, RelayReason: "pro"}})
	}))
	defer login.Close()
	cache := NewEntitlementCache(login.URL)
	cache.fetch(context.Background())
	if access := cache.GetRelayAccess("user"); access.Allowed {
		t.Fatal("repeated pagination cursor published an allow")
	}
}
