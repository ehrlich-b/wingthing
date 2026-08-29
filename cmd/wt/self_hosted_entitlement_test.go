package main

import (
	"path/filepath"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/relay"
)

func TestEnsureSelfHostedProIsAtomicAndIdempotent(t *testing.T) {
	store, err := relay.OpenRelay(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeForTest(t, "relay store", store)
	if err := store.CreateUser("local-user"); err != nil {
		t.Fatal(err)
	}

	if err := ensureSelfHostedPro(store, "local-user", "local"); err != nil {
		t.Fatal(err)
	}
	if err := ensureSelfHostedPro(store, "local-user", "local"); err != nil {
		t.Fatal(err)
	}
	if !store.IsUserPro("local-user") {
		t.Fatal("self-hosted user was not granted an active entitlement")
	}
	var subscriptions, entitlements int
	if err := store.DB().QueryRow("SELECT COUNT(*) FROM subscriptions WHERE user_id = ?", "local-user").Scan(&subscriptions); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRow("SELECT COUNT(*) FROM entitlements WHERE user_id = ?", "local-user").Scan(&entitlements); err != nil {
		t.Fatal(err)
	}
	if subscriptions != 1 || entitlements != 1 {
		t.Fatalf("idempotent grant created subscriptions=%d entitlements=%d, want 1/1", subscriptions, entitlements)
	}
}

func TestEnsureSelfHostedProRepairsExistingSubscriptionWithoutEntitlement(t *testing.T) {
	store, err := relay.OpenRelay(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeForTest(t, "relay store", store)
	if err := store.CreateUser("existing-user"); err != nil {
		t.Fatal(err)
	}
	userID := "existing-user"
	if err := store.CreateSubscription(&relay.Subscription{
		ID: "existing-sub", UserID: &userID, Plan: "local", Status: "active", Seats: 1,
	}); err != nil {
		t.Fatal(err)
	}

	if err := ensureSelfHostedPro(store, userID, "local"); err != nil {
		t.Fatal(err)
	}
	if !store.IsUserPro(userID) {
		t.Fatal("existing self-hosted subscription was not repaired")
	}
	var subscriptions int
	if err := store.DB().QueryRow("SELECT COUNT(*) FROM subscriptions WHERE user_id = ?", userID).Scan(&subscriptions); err != nil {
		t.Fatal(err)
	}
	if subscriptions != 1 {
		t.Fatalf("repair created %d subscriptions, want 1", subscriptions)
	}
}
