package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIndependentPublicEdgeLegacyLogin(t *testing.T) {
	login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Older login nodes omit free users and publish no enrollment decisions.
		_ = json.NewEncoder(w).Encode([]map[string]string{{"user_id": "pro-owner", "tier": "pro"}})
	}))
	defer login.Close()
	cache := NewEntitlementCache(login.URL)
	cache.fetch(context.Background())
	for _, owner := range []string{"pro-owner", "free-owner"} {
		t.Run(owner, func(t *testing.T) {
			if access := cache.GetRelayAccess(owner); !access.Allowed || access.Reason != "legacy-login" {
				t.Fatalf("legacy sync failed: %#v", access)
			}
			if _, known := cache.GetEnrollment(owner); known {
				t.Fatal("legacy login published an enrollment decision")
			}
			edge := publicEnrollmentEdge(t, cache, owner)
			assertPublicEdgeOwnerAccess(t, edge, owner, true)
			if edge.canAccessWing("unrelated-user", &ConnectedWing{UserID: owner, WingID: "machine"}) {
				t.Fatal("legacy enrollment bypassed machine ownership")
			}
		})
	}
}

func TestIndependentPublicEdgePublishedEnrollmentDecision(t *testing.T) {
	var entries []EntitlementEntry
	login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(entitlementDecisionVersionHeader, "2")
		_ = json.NewEncoder(w).Encode(entries)
	}))
	defer login.Close()
	cache := NewEntitlementCache(login.URL)
	edge := publicEnrollmentEdge(t, cache, "owner")
	// Public directory/ownership access remains available before the first sync
	// and for a newly authenticated user absent from the most recent snapshot.
	assertPublicEdgeOwnerAccess(t, edge, "owner", true)
	cache.fetch(context.Background())
	assertPublicEdgeOwnerAccess(t, edge, "owner", true)
	for _, enrolled := range []bool{false, true, false} {
		entries = []EntitlementEntry{{UserID: "owner", Tier: "free", RelayReason: "direct-only-free", Enrolled: enrolled}}
		cache.fetch(context.Background())
		assertPublicEdgeOwnerAccess(t, edge, "owner", enrolled)
	}
	// The absence of a cache is also a legacy enrollment path on public edges.
	edge.EntitlementCache = nil
	assertPublicEdgeOwnerAccess(t, edge, "owner", true)
}

func TestReviewPublishedDenialSurvivesLegacySync(t *testing.T) {
	var version string
	var entries []EntitlementEntry
	login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if version != "" {
			w.Header().Set(entitlementDecisionVersionHeader, version)
			_ = json.NewEncoder(w).Encode(entries)
			return
		}
		// Legacy responses contain only tiers, with no enrollment proof.
		legacy := make([]map[string]string, 0, len(entries))
		for _, entry := range entries {
			legacy = append(legacy, map[string]string{"user_id": entry.UserID, "tier": entry.Tier})
		}
		_ = json.NewEncoder(w).Encode(legacy)
	}))
	defer login.Close()
	cache := NewEntitlementCache(login.URL)
	edge := publicEnrollmentEdge(t, cache, "owner")
	for _, step := range []struct {
		name    string
		version string
		entries []EntitlementEntry
		known   bool
		allowed bool
	}{
		{name: "initial legacy", allowed: true},
		{name: "published denial", version: "2", entries: []EntitlementEntry{{UserID: "owner", Tier: "free", RelayReason: "roost-enrollment-required"}}, known: true},
		{name: "legacy includes owner", entries: []EntitlementEntry{{UserID: "owner", Tier: "pro"}}, known: true},
		{name: "legacy omits owner", known: true},
		{name: "versioned snapshot omits owner", version: "2", known: true},
		{name: "explicit reauthorization", version: "2", entries: []EntitlementEntry{{UserID: "owner", Tier: "pro", RelayAllowed: true, RelayReason: "pro", Enrolled: true}}, known: true, allowed: true},
		{name: "legacy after reauthorization", known: true, allowed: true},
		{name: "denied again", version: "2", entries: []EntitlementEntry{{UserID: "owner", Tier: "free", RelayReason: "roost-enrollment-required"}}, known: true},
	} {
		t.Run(step.name, func(t *testing.T) {
			version, entries = step.version, step.entries
			cache.fetch(context.Background())
			allowed, known := cache.GetEnrollment("owner")
			if known != step.known || (known && allowed != step.allowed) {
				t.Fatalf("enrollment = (%v, %v), want (%v, %v)", allowed, known, step.allowed, step.known)
			}
			assertPublicEdgeOwnerAccess(t, edge, "owner", step.allowed)
			if _, known := cache.GetEnrollment("new-user"); known || !edge.roostUserIDAllowed("new-user") {
				t.Fatal("public edge must preserve legacy access for a user with no published decision")
			}
			edge.Config.RoostAllowedEmails = []string{"owner@example.com"}
			assertPublicEdgeOwnerAccess(t, edge, "owner", step.known && step.allowed)
			if edge.roostUserIDAllowed("new-user") {
				t.Fatal("allowlisted edge must require a published enrollment decision")
			}
			edge.Config.RoostAllowedEmails = nil
		})
	}
}

func publicEnrollmentEdge(t *testing.T, cache *EntitlementCache, owner string) *Server {
	t.Helper()
	edge := NewServer(testStore(t), ServerConfig{NodeRole: "edge"})
	edge.EntitlementCache = cache
	edge.sessionCache = NewSessionCache()
	edge.sessionCache.entries["owner-session"] = &sessionCacheEntry{
		user: &User{ID: owner}, fetchedAt: time.Now(),
	}
	return edge
}

func assertPublicEdgeOwnerAccess(t *testing.T, edge *Server, owner string, want bool) {
	t.Helper()
	if got := edge.canAccessWing(owner, &ConnectedWing{UserID: owner, WingID: "machine"}); got != want {
		t.Fatalf("owner machine access = %v, want %v", got, want)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/app/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieNameForChannel(), Value: "owner-session"})
	if user := edge.sessionUser(req); (user != nil) != want {
		t.Fatalf("owner session = %#v, want authorized=%v", user, want)
	}
}
