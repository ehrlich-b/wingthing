package main

import (
	"fmt"

	"github.com/ehrlich-b/wingthing/internal/relay"
	"github.com/google/uuid"
)

func ensureSelfHostedPro(store *relay.RelayStore, userID, plan string) error {
	subID := uuid.New().String()
	sub := &relay.Subscription{ID: subID, UserID: &userID, Plan: plan, Status: "active", Seats: 1}
	ent := &relay.Entitlement{ID: uuid.New().String(), UserID: userID, SubscriptionID: subID}
	if _, _, err := store.EnsurePersonalSubscription(sub, ent); err != nil {
		return fmt.Errorf("activate self-hosted subscription: %w", err)
	}
	return nil
}
