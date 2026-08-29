package relay

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestConsumeMagicLinkIsConcurrentAndOneShot(t *testing.T) {
	store, err := OpenRelay(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateMagicLink(
		"magic-id", "person@example.test", "magic-token", time.Now().Add(time.Minute),
	); err != nil {
		t.Fatal(err)
	}

	const attempts = 12
	start := make(chan struct{})
	results := make(chan string, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			email, _ := store.ConsumeMagicLink("magic-token")
			results <- email
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	winners := 0
	for email := range results {
		if email != "" {
			winners++
			if email != "person@example.test" {
				t.Fatalf("consumed email = %q", email)
			}
		}
	}
	if winners != 1 {
		t.Fatalf("successful magic-link consumptions = %d, want 1", winners)
	}
}

func TestConsumeMagicLinkRejectsExpiredTokenWithoutMarkingItUsed(t *testing.T) {
	store := testStore(t)
	if err := store.CreateMagicLink(
		"expired-magic-id", "person@example.test", "expired-magic-token", time.Now().Add(-time.Minute),
	); err != nil {
		t.Fatal(err)
	}
	if email, err := store.ConsumeMagicLink("expired-magic-token"); err == nil || email != "" {
		t.Fatalf("expired magic link = %q, %v; want rejection", email, err)
	}
	var used int
	if err := store.DB().QueryRow(
		"SELECT used FROM magic_links WHERE token = ?", "expired-magic-token",
	).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if used != 0 {
		t.Fatalf("expired magic link used = %d, want 0", used)
	}
}

func TestExchangeClaimedDeviceCodeIsConcurrentAndOneShot(t *testing.T) {
	store, err := OpenRelay(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateUser("exchange-user"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateDeviceCode("exchange-code", "ONE123", "exchange-device", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.ClaimDeviceCode("exchange-code", "exchange-user"); err != nil {
		t.Fatal(err)
	}

	const attempts = 12
	start := make(chan struct{})
	results := make(chan bool, attempts)
	errs := make(chan error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			exchanged, err := store.ExchangeClaimedDeviceCode(
				"exchange-code", fmt.Sprintf("exchange-token-%d", index),
				"exchange-user", "exchange-device", nil,
			)
			results <- exchanged
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent exchange: %v", err)
		}
	}
	winners := 0
	for exchanged := range results {
		if exchanged {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("successful exchanges = %d, want 1", winners)
	}
	var tokens int
	if err := store.DB().QueryRow(
		"SELECT COUNT(*) FROM device_tokens WHERE user_id = ? AND device_id = ?",
		"exchange-user", "exchange-device",
	).Scan(&tokens); err != nil {
		t.Fatal(err)
	}
	if tokens != 1 {
		t.Fatalf("stored tokens = %d, want 1", tokens)
	}
	if code, err := store.GetDeviceCode("exchange-code"); err != nil || code != nil {
		t.Fatalf("consumed device code = %#v, %v; want nil", code, err)
	}
}

func TestCreateDeviceCodeReservesActiveUserCodeConcurrently(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "relay.db")
	first, err := OpenRelay(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := OpenRelay(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })

	stores := []*RelayStore{first, second}
	const attempts = 12
	start := make(chan struct{})
	errs := make(chan error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			errs <- stores[index%len(stores)].CreateDeviceCode(
				fmt.Sprintf("opaque-code-%d", index), "SAME23",
				fmt.Sprintf("device-%d", index), time.Now().Add(time.Minute),
			)
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)

	winners := 0
	for err := range errs {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, ErrDeviceUserCodeExists):
		default:
			t.Fatalf("concurrent device code creation: %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("successful device code creations = %d, want 1", winners)
	}
	var rows int
	if err := first.DB().QueryRow(
		"SELECT COUNT(*) FROM device_codes WHERE user_code = ?", "SAME23",
	).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("stored device codes = %d, want 1", rows)
	}
}

func TestCreateDeviceCodeAllowsExpiredUserCodeReuse(t *testing.T) {
	store := testStore(t)
	if err := store.CreateDeviceCode("expired-code", "REUSE2", "old-device", time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateDeviceCode("active-code", "REUSE2", "new-device", time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("reuse expired user code: %v", err)
	}
	dc, err := store.GetDeviceCodeByUserCode("REUSE2")
	if err != nil {
		t.Fatal(err)
	}
	if dc == nil || dc.Code != "active-code" {
		t.Fatalf("active device code = %#v, want active-code", dc)
	}
}

func TestGetDeviceCodeByUserCodeFailsClosedOnLegacyCollision(t *testing.T) {
	store := testStore(t)
	expires := time.Now().Add(time.Minute).UTC().Format("2006-01-02 15:04:05")
	for _, code := range []string{"legacy-one", "legacy-two"} {
		if _, err := store.DB().Exec(
			"INSERT INTO device_codes (code, user_code, device_id, expires_at) VALUES (?, 'DUPE23', ?, ?)",
			code, code+"-device", expires,
		); err != nil {
			t.Fatal(err)
		}
	}
	if dc, err := store.GetDeviceCodeByUserCode("DUPE23"); err == nil || dc != nil {
		t.Fatalf("legacy collision = %#v, %v; want fail-closed error", dc, err)
	}
}

func TestCreateBuiltinUsersIsConcurrentAndIdempotent(t *testing.T) {
	for _, tc := range []struct {
		name     string
		userID   string
		deviceID string
		create   func(*RelayStore) (*User, string, error)
	}{
		{name: "local", userID: "local", deviceID: "local", create: (*RelayStore).CreateLocalUser},
		{name: "service", userID: roostWingServiceUserID, deviceID: "roost-wing", create: (*RelayStore).CreateServiceUser},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "relay.db")
			first, err := OpenRelay(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = first.Close() })
			second, err := OpenRelay(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = second.Close() })

			stores := []*RelayStore{first, second}
			const attempts = 12
			start := make(chan struct{})
			tokens := make(chan string, attempts)
			errs := make(chan error, attempts)
			var wg sync.WaitGroup
			for i := 0; i < attempts; i++ {
				wg.Add(1)
				go func(index int) {
					defer wg.Done()
					<-start
					u, token, err := tc.create(stores[index%len(stores)])
					if err == nil && (u == nil || u.ID != tc.userID) {
						err = fmt.Errorf("user = %#v, want id %q", u, tc.userID)
					}
					tokens <- token
					errs <- err
				}(i)
			}
			close(start)
			wg.Wait()
			close(tokens)
			close(errs)

			for err := range errs {
				if err != nil {
					t.Fatalf("concurrent creation: %v", err)
				}
			}
			var stable string
			for token := range tokens {
				if token == "" {
					t.Fatal("empty built-in device token")
				}
				if stable == "" {
					stable = token
				} else if token != stable {
					t.Fatalf("token = %q, want stable token %q", token, stable)
				}
			}
			var rows int
			if err := first.DB().QueryRow(
				"SELECT COUNT(*) FROM device_tokens WHERE user_id = ? AND device_id = ?",
				tc.userID, tc.deviceID,
			).Scan(&rows); err != nil {
				t.Fatal(err)
			}
			if rows != 1 {
				t.Fatalf("stored built-in tokens = %d, want 1", rows)
			}
		})
	}
}

func TestOpenRelayEnforcesForeignKeysOnEveryConnection(t *testing.T) {
	store, err := OpenRelay(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	first, err := store.DB().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	second, err := store.DB().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()

	for name, connection := range map[string]*sql.Conn{"first": first, "second": second} {
		var enabled int
		if err := connection.QueryRowContext(context.Background(), "PRAGMA foreign_keys").Scan(&enabled); err != nil {
			t.Fatalf("%s connection foreign_keys: %v", name, err)
		}
		if enabled != 1 {
			t.Fatalf("%s connection foreign_keys = %d, want 1", name, enabled)
		}
	}
}

func TestEnsurePersonalSubscriptionIsConcurrentAndIdempotent(t *testing.T) {
	store, err := OpenRelay(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateUser("concurrent-personal"); err != nil {
		t.Fatal(err)
	}

	var created atomic.Int32
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < cap(errs); i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			userID := "concurrent-personal"
			subID := fmt.Sprintf("concurrent-sub-%02d", index)
			_, wasCreated, err := store.EnsurePersonalSubscription(
				&Subscription{ID: subID, UserID: &userID, Plan: "roost", Status: "active", Seats: 1},
				&Entitlement{ID: fmt.Sprintf("concurrent-ent-%02d", index), UserID: userID, SubscriptionID: subID},
			)
			if err != nil {
				errs <- err
				return
			}
			if wasCreated {
				created.Add(1)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent ensure: %v", err)
	}
	if got := created.Load(); got != 1 {
		t.Fatalf("new subscriptions reported = %d, want 1", got)
	}
	var subscriptions, entitlements int
	if err := store.DB().QueryRow(
		"SELECT COUNT(*) FROM subscriptions WHERE user_id = ? AND status = 'active'", "concurrent-personal",
	).Scan(&subscriptions); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRow(
		"SELECT COUNT(*) FROM entitlements WHERE user_id = ?", "concurrent-personal",
	).Scan(&entitlements); err != nil {
		t.Fatal(err)
	}
	if subscriptions != 1 || entitlements != 1 {
		t.Fatalf("concurrent state: subscriptions=%d entitlements=%d, want 1/1", subscriptions, entitlements)
	}
}

func TestEnsurePersonalSubscriptionRollsBackOnEntitlementIDCollision(t *testing.T) {
	store := testStore(t)
	for _, userID := range []string{"collision-owner", "collision-other"} {
		if err := store.CreateUser(userID); err != nil {
			t.Fatal(err)
		}
	}
	otherUserID := "collision-other"
	other := &Subscription{ID: "collision-other-sub", UserID: &otherUserID, Plan: "pro", Status: "active", Seats: 1}
	if err := store.ActivateSubscription(
		other,
		&Entitlement{ID: "shared-entitlement-id", UserID: otherUserID, SubscriptionID: other.ID},
	); err != nil {
		t.Fatal(err)
	}

	ownerID := "collision-owner"
	candidate := &Subscription{ID: "collision-owner-sub", UserID: &ownerID, Plan: "roost", Status: "active", Seats: 1}
	if _, _, err := store.EnsurePersonalSubscription(
		candidate,
		&Entitlement{ID: "shared-entitlement-id", UserID: ownerID, SubscriptionID: candidate.ID},
	); err == nil {
		t.Fatal("expected entitlement ID collision to fail")
	}
	active, err := store.GetActivePersonalSubscription(ownerID)
	if err != nil {
		t.Fatal(err)
	}
	if active != nil || store.IsUserPro(ownerID) {
		t.Fatalf("failed ensure left plan state behind: active=%#v pro=%v", active, store.IsUserPro(ownerID))
	}
}

func TestActivateSubscriptionRejectsSecondActivePlan(t *testing.T) {
	store := testStore(t)
	if err := store.CreateUser("single-plan-user"); err != nil {
		t.Fatal(err)
	}
	userID := "single-plan-user"
	first := &Subscription{ID: "single-plan-first", UserID: &userID, Plan: "pro", Status: "active", Seats: 1}
	if err := store.ActivateSubscription(first, &Entitlement{ID: "single-plan-first-ent", UserID: userID, SubscriptionID: first.ID}); err != nil {
		t.Fatal(err)
	}
	second := &Subscription{ID: "single-plan-second", UserID: &userID, Plan: "pro", Status: "active", Seats: 1}
	if err := store.ActivateSubscription(
		second,
		&Entitlement{ID: "single-plan-second-ent", UserID: userID, SubscriptionID: second.ID},
	); !errors.Is(err, ErrActivePersonalSubscription) {
		t.Fatalf("second activation error = %v, want %v", err, ErrActivePersonalSubscription)
	}
}

func TestBackfillProUsersWorksWithInMemoryStore(t *testing.T) {
	store := testStore(t)
	if err := store.CreateUser("backfill-memory-user"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateUserTier("backfill-memory-user", "pro"); err != nil {
		t.Fatal(err)
	}
	if err := store.BackfillProUsers(); err != nil {
		t.Fatal(err)
	}
	if !store.IsUserPro("backfill-memory-user") {
		t.Fatal("backfill did not install an active entitlement")
	}
}

func TestRotateDeviceTokenRollsBackWhenReplacementConflicts(t *testing.T) {
	store := testStore(t)
	if err := store.CreateUser("rotate-user"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateDeviceToken("old-token", "rotate-user", "device", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateDeviceToken("occupied-token", "rotate-user", "other-device", nil); err != nil {
		t.Fatal(err)
	}

	if err := store.RotateDeviceToken("old-token", "occupied-token", "rotate-user", "device", nil); err == nil {
		t.Fatal("expected conflicting replacement token to fail")
	}
	userID, deviceID, err := store.ValidateToken("old-token")
	if err != nil {
		t.Fatalf("old token was lost after failed rotation: %v", err)
	}
	if userID != "rotate-user" || deviceID != "device" {
		t.Fatalf("old token identity = %q/%q", userID, deviceID)
	}
}

func TestActivateOrgSubscriptionRollsBackAllState(t *testing.T) {
	store := testStore(t)
	if err := store.CreateUser("org-user"); err != nil {
		t.Fatal(err)
	}
	orgID := "missing-org"
	sub := &Subscription{ID: "orphan-sub", OrgID: &orgID, Plan: "team_monthly", Status: "active", Seats: 3}
	grants := []*Entitlement{{ID: "orphan-ent", UserID: "org-user", SubscriptionID: sub.ID}}

	if _, err := store.ActivateOrgSubscription(sub, orgID, grants); err == nil {
		t.Fatal("expected activation for a missing org to fail")
	}
	var count int
	if err := store.DB().QueryRow("SELECT COUNT(*) FROM subscriptions WHERE id = ?", sub.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("partial subscription remained after rollback: count=%d", count)
	}
	var tier string
	if err := store.DB().QueryRow("SELECT tier FROM users WHERE id = ?", "org-user").Scan(&tier); err != nil {
		t.Fatal(err)
	}
	if tier != "free" {
		t.Fatalf("tier changed after rolled-back activation: %q", tier)
	}
}

func TestExpandOrgSubscriptionRollsBackSeatChange(t *testing.T) {
	store := testStore(t)
	if err := store.CreateUser("owner"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateOrg("org", "Org", "org", "owner"); err != nil {
		t.Fatal(err)
	}
	orgID := "org"
	sub := &Subscription{ID: "team-sub", OrgID: &orgID, Plan: "team_monthly", Status: "active", Seats: 1}
	if _, err := store.ActivateOrgSubscription(sub, orgID, []*Entitlement{{ID: "owner-ent", UserID: "owner", SubscriptionID: sub.ID}}); err != nil {
		t.Fatal(err)
	}

	if _, err := store.ExpandOrgSubscription(sub.ID, "missing-org", 5, nil); err == nil {
		t.Fatal("expected expansion for a missing org to fail")
	}
	active, err := store.GetActiveOrgSubscription(orgID)
	if err != nil {
		t.Fatal(err)
	}
	if active == nil || active.Seats != 1 {
		t.Fatalf("subscription seats changed after rollback: %#v", active)
	}
	org, err := store.GetOrgByID(orgID)
	if err != nil {
		t.Fatal(err)
	}
	if org == nil || org.MaxSeats != 1 {
		t.Fatalf("org seats changed after rollback: %#v", org)
	}
}

func TestExpandOrgSubscriptionCannotReduceSeats(t *testing.T) {
	store := testStore(t)
	if err := store.CreateUser("seat-owner"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateOrg("seat-org", "Seat Org", "seat-org", "seat-owner"); err != nil {
		t.Fatal(err)
	}
	orgID := "seat-org"
	sub := &Subscription{ID: "seat-sub", OrgID: &orgID, Plan: "team", Status: "active", Seats: 3}
	if _, err := store.ActivateOrgSubscription(
		sub,
		orgID,
		[]*Entitlement{{ID: "seat-owner-ent", UserID: "seat-owner", SubscriptionID: sub.ID}},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ExpandOrgSubscription(sub.ID, orgID, 2, nil); !errors.Is(err, ErrOrgSeatsNotIncreased) {
		t.Fatalf("seat reduction error = %v, want %v", err, ErrOrgSeatsNotIncreased)
	}
	active, err := store.GetActiveOrgSubscription(orgID)
	if err != nil {
		t.Fatal(err)
	}
	org, err := store.GetOrgByID(orgID)
	if err != nil {
		t.Fatal(err)
	}
	if active == nil || active.Seats != 3 || org == nil || org.MaxSeats != 3 {
		t.Fatalf("seat reduction changed state: subscription=%#v org=%#v", active, org)
	}
}

func TestCancelOrgSubscriptionPreservesOtherProGrant(t *testing.T) {
	store := testStore(t)
	if err := store.CreateUser("member"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateOrg("cancel-org", "Cancel Org", "cancel-org", "member"); err != nil {
		t.Fatal(err)
	}
	personalUserID := "member"
	personal := &Subscription{ID: "personal-sub", UserID: &personalUserID, Plan: "pro_monthly", Status: "active", Seats: 1}
	if err := store.ActivateSubscription(personal, &Entitlement{ID: "personal-ent", UserID: "member", SubscriptionID: personal.ID}); err != nil {
		t.Fatal(err)
	}
	orgID := "cancel-org"
	team := &Subscription{ID: "cancel-team-sub", OrgID: &orgID, Plan: "team_monthly", Status: "active", Seats: 1}
	if _, err := store.ActivateOrgSubscription(team, orgID, []*Entitlement{{ID: "team-ent", UserID: "member", SubscriptionID: team.ID}}); err != nil {
		t.Fatal(err)
	}

	affected, err := store.CancelOrgSubscription(team.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(affected) != 1 || affected[0] != "member" {
		t.Fatalf("affected users = %#v", affected)
	}
	var tier string
	if err := store.DB().QueryRow("SELECT tier FROM users WHERE id = ?", "member").Scan(&tier); err != nil {
		t.Fatal(err)
	}
	if tier != "pro" {
		t.Fatalf("remaining personal grant should preserve pro tier, got %q", tier)
	}
	if count, err := store.CountEntitlementsBySub(team.ID); err != nil || count != 0 {
		t.Fatalf("team entitlements after cancellation = %d, %v", count, err)
	}
}

func TestCancelPersonalSubscriptionPreservesOtherProGrant(t *testing.T) {
	store := testStore(t)
	if err := store.CreateUser("personal-member"); err != nil {
		t.Fatal(err)
	}
	userID := "personal-member"
	for index, setup := range []struct {
		subID string
		entID string
	}{
		{subID: "personal-first", entID: "personal-first-ent"},
		{subID: "personal-second", entID: "personal-second-ent"},
	} {
		sub := &Subscription{ID: setup.subID, UserID: &userID, Plan: "pro_monthly", Status: "active", Seats: 1}
		ent := &Entitlement{ID: setup.entID, UserID: userID, SubscriptionID: sub.ID}
		if index == 0 {
			if err := store.ActivateSubscription(sub, ent); err != nil {
				t.Fatal(err)
			}
			continue
		}
		// Simulate duplicate active rows left by an older deployment. New
		// activations reject these, but cancellation must remain compatible with
		// data that already exists.
		if err := store.CreateSubscription(sub); err != nil {
			t.Fatal(err)
		}
		if err := store.GrantEntitlement(ent); err != nil {
			t.Fatal(err)
		}
	}

	tier, err := store.CancelPersonalSubscription("personal-first", userID)
	if err != nil {
		t.Fatal(err)
	}
	if tier != "pro" {
		t.Fatalf("remaining personal grant should preserve pro tier, got %q", tier)
	}
	if count, err := store.CountEntitlementsBySub("personal-first"); err != nil || count != 0 {
		t.Fatalf("canceled subscription entitlements = %d, %v", count, err)
	}
}

func TestCancelPersonalSubscriptionRollsBackAllState(t *testing.T) {
	store := testStore(t)
	if err := store.CreateUser("rollback-personal"); err != nil {
		t.Fatal(err)
	}
	userID := "rollback-personal"
	sub := &Subscription{ID: "rollback-personal-sub", UserID: &userID, Plan: "pro_monthly", Status: "active", Seats: 1}
	if err := store.ActivateSubscription(sub, &Entitlement{ID: "rollback-personal-ent", UserID: userID, SubscriptionID: sub.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`CREATE TRIGGER fail_personal_entitlement_delete
		BEFORE DELETE ON entitlements
		WHEN OLD.subscription_id = 'rollback-personal-sub'
		BEGIN
			SELECT RAISE(ABORT, 'forced entitlement delete failure');
		END`); err != nil {
		t.Fatal(err)
	}

	if _, err := store.CancelPersonalSubscription(sub.ID, userID); err == nil {
		t.Fatal("expected cancellation to fail")
	}
	var status string
	if err := store.DB().QueryRow("SELECT status FROM subscriptions WHERE id = ?", sub.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "active" {
		t.Fatalf("subscription status changed despite rollback: %q", status)
	}
	if count, err := store.CountEntitlementsBySub(sub.ID); err != nil || count != 1 {
		t.Fatalf("entitlements after rollback = %d, %v", count, err)
	}
	var tier string
	if err := store.DB().QueryRow("SELECT tier FROM users WHERE id = ?", userID).Scan(&tier); err != nil {
		t.Fatal(err)
	}
	if tier != "pro" {
		t.Fatalf("user tier changed despite rollback: %q", tier)
	}
}

func TestAcceptOrgInviteDoesNotConsumeInviteWhenFull(t *testing.T) {
	store := testStore(t)
	for _, userID := range []string{"invite-owner", "invitee"} {
		if err := store.CreateUser(userID); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.CreateOrg("full-org", "Full Org", "full-org", "invite-owner"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateOrgInvite("invite", "full-org", "invitee@example.test", "invite-token", "invite-owner", "member"); err != nil {
		t.Fatal(err)
	}

	if _, _, _, err := store.AcceptOrgInvite("invite-token", "invitee", "invitee@example.test", "invite-ent"); err == nil {
		t.Fatal("expected full org to reject invite")
	}
	var claimedAt sql.NullTime
	if err := store.DB().QueryRow("SELECT claimed_at FROM org_invites WHERE token = ?", "invite-token").Scan(&claimedAt); err != nil {
		t.Fatal(err)
	}
	if claimedAt.Valid {
		t.Fatal("invite was consumed despite failed membership transaction")
	}
	if store.IsOrgMember("full-org", "invitee") {
		t.Fatal("invitee was added despite full org")
	}
}

func TestCreateOrgInviteRejectsFormerAdmin(t *testing.T) {
	store := testStore(t)
	for _, userID := range []string{"invite-race-owner", "invite-race-admin"} {
		if err := store.CreateUser(userID); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.CreateOrgWithSeats("invite-race-org", "Invite Race Org", "invite-race-org", "invite-race-owner", 2); err != nil {
		t.Fatal(err)
	}
	if err := store.AddOrgMember("invite-race-org", "invite-race-admin", "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RemoveOrgMemberAndEntitlement("invite-race-org", "invite-race-admin"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateOrgInvite(
		"stale-admin-invite", "invite-race-org", "target@example.test", "stale-admin-token", "invite-race-admin", "member",
	); !errors.Is(err, ErrOrgMutationUnauthorized) {
		t.Fatalf("former admin invite error = %v, want %v", err, ErrOrgMutationUnauthorized)
	}
	invite, err := store.GetInviteByToken("stale-admin-token")
	if err != nil {
		t.Fatal(err)
	}
	if invite != nil {
		t.Fatalf("unauthorized invite persisted: %#v", invite)
	}
}

func TestCreateOrgForOwnerLimitedEnforcesLimitInsideTransaction(t *testing.T) {
	store := testStore(t)
	if err := store.CreateUser("limited-org-owner"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("limited-org-%d", i)
		if err := store.CreateOrgForOwnerLimited(id, id, id, "limited-org-owner", 1, 5); err != nil {
			t.Fatalf("create organization %d: %v", i, err)
		}
	}
	if err := store.CreateOrgForOwnerLimited(
		"limited-org-5", "limited-org-5", "limited-org-5", "limited-org-owner", 1, 5,
	); !errors.Is(err, ErrOrgLimitReached) {
		t.Fatalf("sixth organization error = %v, want %v", err, ErrOrgLimitReached)
	}
	if count, err := store.CountOrgsOwnedByUser("limited-org-owner"); err != nil || count != 5 {
		t.Fatalf("owned organizations = %d, %v; want 5", count, err)
	}
}

func TestCreateOrgInviteReportsDuplicate(t *testing.T) {
	store := testStore(t)
	if err := store.CreateUser("duplicate-invite-owner"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateOrg("duplicate-invite-org", "Duplicate Invite Org", "duplicate-invite-org", "duplicate-invite-owner"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateOrgInvite(
		"duplicate-invite-1", "duplicate-invite-org", "target@example.test", "duplicate-token-1", "duplicate-invite-owner", "member",
	); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateOrgInvite(
		"duplicate-invite-2", "duplicate-invite-org", "target@example.test", "duplicate-token-2", "duplicate-invite-owner", "member",
	); !errors.Is(err, ErrOrgInviteExists) {
		t.Fatalf("duplicate invite error = %v, want %v", err, ErrOrgInviteExists)
	}
}

func TestRemoveOrgMemberAuthorizedRejectsFormerAdmin(t *testing.T) {
	store := testStore(t)
	for _, userID := range []string{"remove-race-owner", "remove-race-admin", "remove-race-target"} {
		if err := store.CreateUser(userID); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.CreateOrgWithSeats("remove-race-org", "Remove Race Org", "remove-race-org", "remove-race-owner", 3); err != nil {
		t.Fatal(err)
	}
	if err := store.AddOrgMember("remove-race-org", "remove-race-admin", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := store.AddOrgMember("remove-race-org", "remove-race-target", "member"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RemoveOrgMemberAndEntitlement("remove-race-org", "remove-race-admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RemoveOrgMemberAuthorized(
		"remove-race-org", "remove-race-admin", "remove-race-target",
	); !errors.Is(err, ErrOrgMutationUnauthorized) {
		t.Fatalf("former admin removal error = %v, want %v", err, ErrOrgMutationUnauthorized)
	}
	if !store.IsOrgMember("remove-race-org", "remove-race-target") {
		t.Fatal("former admin removed the target member")
	}
}

func TestRemoveOrgMemberAuthorizedRejectsOwner(t *testing.T) {
	store := testStore(t)
	if err := store.CreateUser("protected-owner"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateOrg("protected-owner-org", "Protected Owner Org", "protected-owner-org", "protected-owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RemoveOrgMemberAuthorized(
		"protected-owner-org", "protected-owner", "protected-owner",
	); !errors.Is(err, ErrOrgOwnerRemoval) {
		t.Fatalf("owner removal error = %v, want %v", err, ErrOrgOwnerRemoval)
	}
	if !store.IsOrgMember("protected-owner-org", "protected-owner") {
		t.Fatal("organization owner membership was removed")
	}
}

func TestRevokeOrgInviteAuthorizedRejectsFormerAdmin(t *testing.T) {
	store := testStore(t)
	for _, userID := range []string{"revoke-race-owner", "revoke-race-admin"} {
		if err := store.CreateUser(userID); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.CreateOrgWithSeats("revoke-race-org", "Revoke Race Org", "revoke-race-org", "revoke-race-owner", 2); err != nil {
		t.Fatal(err)
	}
	if err := store.AddOrgMember("revoke-race-org", "revoke-race-admin", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateOrgInvite(
		"revoke-race-invite", "revoke-race-org", "target@example.test", "revoke-race-token", "revoke-race-owner", "member",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RemoveOrgMemberAndEntitlement("revoke-race-org", "revoke-race-admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RevokeOrgInviteAuthorized(
		"revoke-race-org", "revoke-race-token", "revoke-race-admin",
	); !errors.Is(err, ErrOrgMutationUnauthorized) {
		t.Fatalf("former admin revoke error = %v, want %v", err, ErrOrgMutationUnauthorized)
	}
	invite, err := store.GetInviteByToken("revoke-race-token")
	if err != nil {
		t.Fatal(err)
	}
	if invite == nil {
		t.Fatal("former admin revoked the pending invite")
	}
}

func TestAcceptOrgInviteRollsBackMembershipWhenEntitlementFails(t *testing.T) {
	store := testStore(t)
	for _, userID := range []string{"ent-owner", "ent-invitee"} {
		if err := store.CreateUser(userID); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.CreateOrg("ent-org", "Ent Org", "ent-org", "ent-owner"); err != nil {
		t.Fatal(err)
	}
	orgID := "ent-org"
	sub := &Subscription{ID: "ent-team-sub", OrgID: &orgID, Plan: "team_monthly", Status: "active", Seats: 2}
	if _, err := store.ActivateOrgSubscription(sub, orgID, []*Entitlement{{ID: "ent-owner-grant", UserID: "ent-owner", SubscriptionID: sub.ID}}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateOrgInvite("ent-invite", orgID, "invitee@example.test", "ent-invite-token", "ent-owner", "member"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`CREATE TRIGGER fail_invite_entitlement_insert
		BEFORE INSERT ON entitlements
		WHEN NEW.user_id = 'ent-invitee'
		BEGIN
			SELECT RAISE(ABORT, 'forced entitlement insert failure');
		END`); err != nil {
		t.Fatal(err)
	}

	if _, _, _, err := store.AcceptOrgInvite("ent-invite-token", "ent-invitee", "invitee@example.test", "ent-invite-grant"); err == nil {
		t.Fatal("expected entitlement grant to fail")
	}
	if store.IsOrgMember(orgID, "ent-invitee") {
		t.Fatal("membership remained after entitlement failure")
	}
	var claimedAt sql.NullTime
	if err := store.DB().QueryRow("SELECT claimed_at FROM org_invites WHERE token = ?", "ent-invite-token").Scan(&claimedAt); err != nil {
		t.Fatal(err)
	}
	if claimedAt.Valid {
		t.Fatal("invite was consumed after entitlement failure")
	}
}

func TestRemoveOrgMemberRollsBackMembershipWhenEntitlementDeleteFails(t *testing.T) {
	store := testStore(t)
	for _, userID := range []string{"remove-owner", "remove-member"} {
		if err := store.CreateUser(userID); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.CreateOrg("remove-org", "Remove Org", "remove-org", "remove-owner"); err != nil {
		t.Fatal(err)
	}
	orgID := "remove-org"
	sub := &Subscription{ID: "remove-team-sub", OrgID: &orgID, Plan: "team_monthly", Status: "active", Seats: 2}
	if _, err := store.ActivateOrgSubscription(sub, orgID, []*Entitlement{{ID: "remove-owner-ent", UserID: "remove-owner", SubscriptionID: sub.ID}}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddOrgMember(orgID, "remove-member", "member"); err != nil {
		t.Fatal(err)
	}
	if err := store.GrantEntitlement(&Entitlement{ID: "remove-member-ent", UserID: "remove-member", SubscriptionID: sub.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`CREATE TRIGGER fail_member_entitlement_delete
		BEFORE DELETE ON entitlements
		WHEN OLD.user_id = 'remove-member'
		BEGIN
			SELECT RAISE(ABORT, 'forced entitlement delete failure');
		END`); err != nil {
		t.Fatal(err)
	}

	if _, err := store.RemoveOrgMemberAndEntitlement(orgID, "remove-member"); err == nil {
		t.Fatal("expected member removal to fail")
	}
	if !store.IsOrgMember(orgID, "remove-member") {
		t.Fatal("membership was removed despite entitlement failure")
	}
	if count, err := store.CountEntitlementsBySub(sub.ID); err != nil || count != 2 {
		t.Fatalf("entitlements after rollback = %d, %v", count, err)
	}
}

func TestDeleteOrgRemovesScopedLabels(t *testing.T) {
	store := testStore(t)
	if err := store.CreateUser("label-owner"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateOrg("label-org", "Label Org", "label-org", "label-owner"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetLabel("wing-1", "org", "label-org", "build rig"); err != nil {
		t.Fatal(err)
	}

	if err := store.DeleteOrg("label-org"); err != nil {
		t.Fatal(err)
	}
	var labels int
	if err := store.DB().QueryRow(
		"SELECT COUNT(*) FROM labels WHERE scope_type = 'org' AND scope_id = ?", "label-org",
	).Scan(&labels); err != nil {
		t.Fatal(err)
	}
	if labels != 0 {
		t.Fatalf("org labels after delete = %d, want 0", labels)
	}
}

func TestAuthorizedOrgLabelMutationRejectsFormerAdmin(t *testing.T) {
	store := testStore(t)
	for _, userID := range []string{"label-auth-owner", "label-auth-admin"} {
		if err := store.CreateUser(userID); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.CreateOrgWithSeats("label-auth-org", "Label Auth Org", "label-auth-org", "label-auth-owner", 2); err != nil {
		t.Fatal(err)
	}
	if err := store.AddOrgMember("label-auth-org", "label-auth-admin", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetLabelAuthorized(
		"wing-1", "org", "label-auth-org", "label-auth-admin", false, "before demotion",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RemoveOrgMemberAndEntitlement("label-auth-org", "label-auth-admin"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetLabelAuthorized(
		"wing-1", "org", "label-auth-org", "label-auth-admin", false, "after demotion",
	); !errors.Is(err, ErrOrgMutationUnauthorized) {
		t.Fatalf("former admin label update error = %v, want %v", err, ErrOrgMutationUnauthorized)
	}
	if got := store.ResolveLabel("wing-1", "", "label-auth-org"); got != "before demotion" {
		t.Fatalf("label after rejected update = %q", got)
	}
	if err := store.DeleteLabelAuthorized(
		"wing-1", "org", "label-auth-org", "label-auth-admin", false,
	); !errors.Is(err, ErrOrgMutationUnauthorized) {
		t.Fatalf("former admin label delete error = %v, want %v", err, ErrOrgMutationUnauthorized)
	}
	if got := store.ResolveLabel("wing-1", "", "label-auth-org"); got != "before demotion" {
		t.Fatalf("label after rejected delete = %q", got)
	}
}

func TestDeleteOrgRejectsActiveSubscriptionWithoutPartialCleanup(t *testing.T) {
	store := testStore(t)
	if err := store.CreateUser("active-org-owner"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateOrg("active-org", "Active Org", "active-org", "active-org-owner"); err != nil {
		t.Fatal(err)
	}
	orgID := "active-org"
	sub := &Subscription{ID: "active-org-sub", OrgID: &orgID, Plan: "team_monthly", Status: "active", Seats: 1}
	if _, err := store.ActivateOrgSubscription(
		sub,
		orgID,
		[]*Entitlement{{ID: "active-org-ent", UserID: "active-org-owner", SubscriptionID: sub.ID}},
	); err != nil {
		t.Fatal(err)
	}

	if err := store.DeleteOrg(orgID); !errors.Is(err, ErrActiveOrgSubscription) {
		t.Fatalf("delete active org error = %v, want %v", err, ErrActiveOrgSubscription)
	}
	org, err := store.GetOrgByID(orgID)
	if err != nil {
		t.Fatal(err)
	}
	if org == nil || !store.IsOrgMember(orgID, "active-org-owner") {
		t.Fatal("active org or owner membership was partially deleted")
	}
	active, err := store.GetActiveOrgSubscription(orgID)
	if err != nil || active == nil || active.ID != sub.ID {
		t.Fatalf("active subscription after rejected delete = %#v, %v", active, err)
	}
}

func TestActivateOrgSubscriptionRejectsNonMemberGrant(t *testing.T) {
	store := testStore(t)
	for _, userID := range []string{"member-owner", "non-member"} {
		if err := store.CreateUser(userID); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.CreateOrg("membership-org", "Membership Org", "membership-org", "member-owner"); err != nil {
		t.Fatal(err)
	}
	orgID := "membership-org"
	sub := &Subscription{ID: "membership-org-sub", OrgID: &orgID, Plan: "team", Status: "active", Seats: 2}
	if _, err := store.ActivateOrgSubscription(
		sub,
		orgID,
		[]*Entitlement{{ID: "non-member-ent", UserID: "non-member", SubscriptionID: sub.ID}},
	); err == nil {
		t.Fatal("expected non-member entitlement to reject org activation")
	}
	active, err := store.GetActiveOrgSubscription(orgID)
	if err != nil {
		t.Fatal(err)
	}
	org, err := store.GetOrgByID(orgID)
	if err != nil {
		t.Fatal(err)
	}
	if active != nil || org == nil || org.MaxSeats != 1 || store.IsUserPro("non-member") {
		t.Fatalf("failed org activation left state: active=%#v org=%#v outsider_pro=%v", active, org, store.IsUserPro("non-member"))
	}
}

func TestDeleteOrgAndActivateSubscriptionCannotLeaveOrphan(t *testing.T) {
	store, err := OpenRelay(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateUser("race-owner"); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 20; i++ {
		orgID := fmt.Sprintf("race-org-%02d", i)
		if err := store.CreateOrg(orgID, "Race Org", orgID, "race-owner"); err != nil {
			t.Fatal(err)
		}
		sub := &Subscription{ID: "sub-" + orgID, OrgID: &orgID, Plan: "team", Status: "active", Seats: 1}
		start := make(chan struct{})
		deleteResult := make(chan error, 1)
		activateResult := make(chan error, 1)
		go func() {
			<-start
			deleteResult <- store.DeleteOrg(orgID)
		}()
		go func() {
			<-start
			_, err := store.ActivateOrgSubscription(
				sub,
				orgID,
				[]*Entitlement{{ID: "ent-" + orgID, UserID: "race-owner", SubscriptionID: sub.ID}},
			)
			activateResult <- err
		}()
		close(start)
		deleteErr := <-deleteResult
		activateErr := <-activateResult

		org, err := store.GetOrgByID(orgID)
		if err != nil {
			t.Fatal(err)
		}
		active, err := store.GetActiveOrgSubscription(orgID)
		if err != nil {
			t.Fatal(err)
		}
		if org == nil && active != nil {
			t.Fatalf("iteration %d left orphan subscription %#v (delete=%v activate=%v)", i, active, deleteErr, activateErr)
		}
		if org == nil && deleteErr != nil {
			t.Fatalf("iteration %d deleted org but returned %v", i, deleteErr)
		}
		if active != nil && activateErr != nil {
			t.Fatalf("iteration %d activated subscription but returned %v", i, activateErr)
		}
	}
}

func TestDeleteOrgRollsBackWhenLabelCleanupFails(t *testing.T) {
	store := testStore(t)
	if err := store.CreateUser("label-rollback-owner"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateOrg("label-rollback-org", "Label Rollback Org", "label-rollback-org", "label-rollback-owner"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetLabel("wing-1", "org", "label-rollback-org", "build rig"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`CREATE TRIGGER fail_org_label_delete
		BEFORE DELETE ON labels
		WHEN OLD.scope_type = 'org' AND OLD.scope_id = 'label-rollback-org'
		BEGIN
		SELECT RAISE(ABORT, 'forced org label delete failure');
		END`); err != nil {
		t.Fatal(err)
	}

	if err := store.DeleteOrg("label-rollback-org"); err == nil {
		t.Fatal("expected org deletion to fail")
	}
	org, err := store.GetOrgByID("label-rollback-org")
	if err != nil {
		t.Fatal(err)
	}
	if org == nil {
		t.Fatal("org was partially deleted after label cleanup failure")
	}
	if !store.IsOrgMember("label-rollback-org", "label-rollback-owner") {
		t.Fatal("owner membership was partially deleted after label cleanup failure")
	}
	if got := store.ResolveLabel("wing-1", "", "label-rollback-org"); got != "build rig" {
		t.Fatalf("label after rollback = %q, want build rig", got)
	}
}
