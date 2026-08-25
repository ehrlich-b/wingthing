package relay

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestRelayAccessPolicy(t *testing.T) {
	store := testStore(t)
	if err := store.CreateUser("existing"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateUser("new"); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().UTC()
	if _, err := store.DB().Exec("UPDATE users SET created_at = ? WHERE id = ?", cutoff.Add(-time.Hour), "existing"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec("UPDATE users SET created_at = ? WHERE id = ?", cutoff.Add(time.Hour), "new"); err != nil {
		t.Fatal(err)
	}

	server := NewServer(store, ServerConfig{RelayPolicy: RelayPolicyDirectFree, RelayGrandfatherBefore: cutoff})
	if access := server.relayAccess("existing"); !access.Allowed || access.Reason != "temporary-grandfather" {
		t.Fatalf("existing access = %#v", access)
	}
	if access := server.relayAccess("new"); access.Allowed || access.Reason != "direct-only-free" {
		t.Fatalf("new access = %#v", access)
	}

	subID := uuid.NewString()
	if err := store.CreateSubscription(&Subscription{ID: subID, UserID: stringPtr("new"), Plan: "pro", Status: "active", Seats: 1}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateEntitlement(&Entitlement{ID: uuid.NewString(), UserID: "new", SubscriptionID: subID}); err != nil {
		t.Fatal(err)
	}
	if access := server.relayAccess("new"); !access.Allowed || access.Reason != "pro" {
		t.Fatalf("pro access = %#v", access)
	}
}

func TestRelayAccessLegacyAndSelfHosted(t *testing.T) {
	store := testStore(t)
	if err := store.CreateUser("free"); err != nil {
		t.Fatal(err)
	}
	legacy := NewServer(store, ServerConfig{})
	if access := legacy.relayAccess("free"); !access.Allowed || access.Reason != "legacy-policy" {
		t.Fatalf("legacy access = %#v", access)
	}
	selfHosted := NewServer(store, ServerConfig{RelayPolicy: RelayPolicyDirectFree})
	selfHosted.RoostMode = true
	if access := selfHosted.relayAccess("free"); !access.Allowed || access.Reason != "self-hosted" {
		t.Fatalf("self-hosted access = %#v", access)
	}
}

func TestRelayAccessPrefersLoginNodeCacheOnEdge(t *testing.T) {
	store := testStore(t)
	cache := NewEntitlementCache("http://login.internal")
	cache.relay["cached-pro"] = RelayAccess{Allowed: true, Reason: "pro"}
	server := NewServer(store, ServerConfig{NodeRole: "edge", RelayPolicy: RelayPolicyDirectFree})
	server.EntitlementCache = cache

	if access := server.relayAccess("cached-pro"); !access.Allowed || access.Reason != "pro" {
		t.Fatalf("edge cached access = %#v", access)
	}
	if access := server.relayAccess("not-synced"); access.Allowed || access.Reason != "entitlement-unavailable" {
		t.Fatalf("edge unsynced access = %#v", access)
	}
}

func stringPtr(value string) *string { return &value }
