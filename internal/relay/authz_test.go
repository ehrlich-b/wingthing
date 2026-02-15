package relay

import (
	"testing"
)

// TestAuthzMatrix is the structural authz wall. It creates three actors:
//   - owner: the wing owner
//   - orgMember: a member of the wing's org
//   - outsider: a random user with no relationship
//
// Session-level authz is now handled by the wing via E2E tunnel.
// Only wing-level access control is enforced at the relay.

func setupAuthzServer(t *testing.T) (*Server, *ConnectedWing, string, string, string) {
	t.Helper()
	store := testStore(t)
	s := NewServer(store, ServerConfig{})

	ownerID := "owner-1"
	memberID := "member-1"
	outsiderID := "outsider-1"

	store.CreateUser(ownerID)
	store.CreateUser(memberID)
	store.CreateUser(outsiderID)

	// CreateOrg auto-adds ownerID as "owner" member (max_seats=1)
	store.CreateOrg("org-1", "Test Org", "test-org", ownerID)
	store.DB().Exec("UPDATE orgs SET max_seats = 10 WHERE id = 'org-1'")
	store.AddOrgMember("org-1", memberID, "member")

	// Connect an org wing owned by owner
	wing := &ConnectedWing{
		ID:     "conn-1",
		UserID: ownerID,
		WingID: "wing-stable-1",
		OrgID:  "org-1",
	}
	s.Wings.Add(wing)

	return s, wing, ownerID, memberID, outsiderID
}

// --- Wing access tests ---

func TestAuthzWingAccess(t *testing.T) {
	s, wing, ownerID, memberID, outsiderID := setupAuthzServer(t)

	tests := []struct {
		name   string
		userID string
		want   bool
	}{
		{"owner can access org wing", ownerID, true},
		{"org member can access org wing", memberID, true},
		{"outsider cannot access org wing", outsiderID, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.canAccessWing(tt.userID, wing)
			if got != tt.want {
				t.Errorf("canAccessWing(%s) = %v, want %v", tt.userID, got, tt.want)
			}
		})
	}
}

func TestAuthzListWings(t *testing.T) {
	s, _, ownerID, memberID, outsiderID := setupAuthzServer(t)

	tests := []struct {
		name   string
		userID string
		want   int
	}{
		{"owner sees org wing", ownerID, 1},
		{"org member sees org wing", memberID, 1},
		{"outsider sees no wings", outsiderID, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wings := s.listAccessibleWings(tt.userID)
			if len(wings) != tt.want {
				t.Errorf("listAccessibleWings(%s) = %d wings, want %d", tt.userID, len(wings), tt.want)
			}
		})
	}
}

// --- Personal wing isolation tests ---

func TestAuthzPersonalWingIsolation(t *testing.T) {
	store := testStore(t)
	s := NewServer(store, ServerConfig{})

	store.CreateUser("user-a")
	store.CreateUser("user-b")

	wingA := &ConnectedWing{
		ID:     "conn-a",
		UserID: "user-a",
		WingID: "wing-a",
	}
	s.Wings.Add(wingA)

	t.Run("other user cannot access personal wing", func(t *testing.T) {
		if s.canAccessWing("user-b", wingA) {
			t.Error("user B should not access user A's personal wing")
		}
	})

	t.Run("other user cannot list personal wing", func(t *testing.T) {
		wings := s.listAccessibleWings("user-b")
		if len(wings) != 0 {
			t.Errorf("user B sees %d wings, want 0", len(wings))
		}
	})
}

// --- Wing event notification tests ---

func TestAuthzOrgWingNotifications(t *testing.T) {
	store := testStore(t)
	s := NewServer(store, ServerConfig{})

	store.CreateUser("owner-1")
	store.CreateUser("member-1")
	store.CreateUser("outsider-1")

	store.CreateOrg("org-1", "Test Org", "test-org", "owner-1")
	store.DB().Exec("UPDATE orgs SET max_seats = 10 WHERE id = 'org-1'")
	store.AddOrgMember("org-1", "member-1", "member")

	ownerCh := make(chan WingEvent, 4)
	memberCh := make(chan WingEvent, 4)
	outsiderCh := make(chan WingEvent, 4)
	s.Wings.Subscribe("owner-1", []string{"org-1"}, ownerCh)
	s.Wings.Subscribe("member-1", []string{"org-1"}, memberCh)
	s.Wings.Subscribe("outsider-1", nil, outsiderCh)

	wing := &ConnectedWing{
		ID:     "conn-1",
		UserID: "owner-1",
		WingID: "wing-1",
		OrgID:  "org-1",
	}
	s.Wings.Add(wing)

	// Owner should get notification
	select {
	case ev := <-ownerCh:
		if ev.Type != "wing.online" {
			t.Errorf("owner got event type %q, want wing.online", ev.Type)
		}
	default:
		t.Error("owner should have received wing.online")
	}

	// Org member should get notification
	select {
	case ev := <-memberCh:
		if ev.Type != "wing.online" {
			t.Errorf("member got event type %q, want wing.online", ev.Type)
		}
	default:
		t.Error("org member should have received wing.online")
	}

	// Outsider should NOT get notification
	select {
	case ev := <-outsiderCh:
		t.Errorf("outsider should not get event, got %q", ev.Type)
	default:
		// good
	}

	// Disconnect wing
	s.Wings.Remove(wing.ID)

	// Owner should get offline
	select {
	case ev := <-ownerCh:
		if ev.Type != "wing.offline" {
			t.Errorf("owner got event type %q, want wing.offline", ev.Type)
		}
	default:
		t.Error("owner should have received wing.offline")
	}

	// Org member should get offline
	select {
	case ev := <-memberCh:
		if ev.Type != "wing.offline" {
			t.Errorf("member got event type %q, want wing.offline", ev.Type)
		}
	default:
		t.Error("org member should have received wing.offline")
	}

	// Outsider still nothing
	select {
	case ev := <-outsiderCh:
		t.Errorf("outsider should not get offline event, got %q", ev.Type)
	default:
		// good
	}
}

// --- Personal wing notifications should NOT leak to others ---

func TestAuthzPersonalWingNotifications(t *testing.T) {
	store := testStore(t)
	s := NewServer(store, ServerConfig{})

	store.CreateUser("user-a")
	store.CreateUser("user-b")

	aCh := make(chan WingEvent, 4)
	bCh := make(chan WingEvent, 4)
	s.Wings.Subscribe("user-a", nil, aCh)
	s.Wings.Subscribe("user-b", nil, bCh)

	wing := &ConnectedWing{
		ID:     "conn-a",
		UserID: "user-a",
		WingID: "wing-a",
	}
	s.Wings.Add(wing)

	select {
	case <-aCh:
		// good
	default:
		t.Error("owner should have received wing.online")
	}

	select {
	case ev := <-bCh:
		t.Errorf("user B should not see user A's personal wing, got %q", ev.Type)
	default:
		// good
	}
}

// --- Redirect safety tests ---

func TestSafeRedirect(t *testing.T) {
	tests := []struct {
		dest string
		safe bool
	}{
		{"/app", true},
		{"/app/dashboard", true},
		{"//evil.com", false},
		{"https://evil.com", false},
		{"http://evil.com", false},
		{"", false},
		{"evil.com", false},
		{"/", true},
	}
	for _, tt := range tests {
		t.Run(tt.dest, func(t *testing.T) {
			if got := isSafeRedirect(tt.dest); got != tt.safe {
				t.Errorf("isSafeRedirect(%q) = %v, want %v", tt.dest, got, tt.safe)
			}
		})
	}
}

// --- Wing owner check ---

func TestAuthzWingOwnership(t *testing.T) {
	s, wing, ownerID, memberID, outsiderID := setupAuthzServer(t)

	tests := []struct {
		name    string
		userID  string
		isOwner bool
	}{
		{"owner is owner", ownerID, true},
		{"org member is not owner", memberID, false},
		{"outsider is not owner", outsiderID, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.isWingOwner(tt.userID, wing)
			if got != tt.isOwner {
				t.Errorf("isWingOwner(%s) = %v, want %v", tt.userID, got, tt.isOwner)
			}
		})
	}
}
