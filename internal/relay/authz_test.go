package relay

import (
	"testing"
)

// TestAuthzMatrix is the structural authz wall. It creates three actors:
//   - owner: the wing owner
//   - orgMember: a member of the wing's org
//   - outsider: a random user with no relationship
//
// Every session-scoped operation must be tested against all three actors.
// If a new operation is added without updating this test, the test should
// be extended — this is the safety net.

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
		ID:       "conn-1",
		UserID:   ownerID,
		WingID:   "wing-stable-1",
		Hostname: "test-host",
		OrgID:    "org-1",
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

// --- PTY session authz tests (the wall) ---

func TestAuthzPTYGateway(t *testing.T) {
	s, wing, ownerID, memberID, outsiderID := setupAuthzServer(t)

	session := &PTYSession{
		ID:     "sess-1",
		WingID: wing.ID,
		UserID: ownerID,
		Agent:  "claude",
		Status: "active",
	}
	s.PTY.Add(session)

	tests := []struct {
		name       string
		userID     string
		wantAccess bool
	}{
		{"owner can access own session", ownerID, true},
		{"org member can access org session", memberID, true},
		{"outsider cannot access org session", outsiderID, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sess, w := s.getAuthorizedPTY(tt.userID, "sess-1")
			if tt.wantAccess {
				if sess == nil {
					t.Error("getAuthorizedPTY returned nil session, want access")
				}
				if w == nil {
					t.Error("getAuthorizedPTY returned nil wing, want access")
				}
			} else {
				if sess != nil {
					t.Error("getAuthorizedPTY returned session, want nil (no access)")
				}
			}
		})
	}

	t.Run("nonexistent session returns nil", func(t *testing.T) {
		sess, _ := s.getAuthorizedPTY(ownerID, "no-such-session")
		if sess != nil {
			t.Error("expected nil for nonexistent session")
		}
	})
}

// --- Chat session authz tests (the wall) ---

func TestAuthzChatGateway(t *testing.T) {
	s, wing, ownerID, memberID, outsiderID := setupAuthzServer(t)

	cs := &ChatSession{
		ID:     "chat-1",
		WingID: wing.ID,
		UserID: ownerID,
		Agent:  "claude",
		Status: "active",
	}
	s.Chat.Add(cs)

	tests := []struct {
		name       string
		userID     string
		wantAccess bool
	}{
		{"owner can access own chat", ownerID, true},
		{"org member can access org chat", memberID, true},
		{"outsider cannot access org chat", outsiderID, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chat, w := s.getAuthorizedChat(tt.userID, "chat-1")
			if tt.wantAccess {
				if chat == nil {
					t.Error("getAuthorizedChat returned nil, want access")
				}
				if w == nil {
					t.Error("getAuthorizedChat returned nil wing, want access")
				}
			} else {
				if chat != nil {
					t.Error("getAuthorizedChat returned session, want nil (no access)")
				}
			}
		})
	}

	t.Run("nonexistent chat returns nil", func(t *testing.T) {
		chat, _ := s.getAuthorizedChat(ownerID, "no-such-chat")
		if chat != nil {
			t.Error("expected nil for nonexistent chat")
		}
	})
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

	sess := &PTYSession{ID: "sess-a", WingID: wingA.ID, UserID: "user-a", Status: "active"}
	s.PTY.Add(sess)

	t.Run("other user cannot access personal PTY session", func(t *testing.T) {
		session, _ := s.getAuthorizedPTY("user-b", "sess-a")
		if session != nil {
			t.Error("user B should not access user A's PTY session")
		}
	})

	chatA := &ChatSession{ID: "chat-a", WingID: wingA.ID, UserID: "user-a", Status: "active"}
	s.Chat.Add(chatA)

	t.Run("other user cannot access personal chat session", func(t *testing.T) {
		chat, _ := s.getAuthorizedChat("user-b", "chat-a")
		if chat != nil {
			t.Error("user B should not access user A's chat session")
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
	s.Wings.Subscribe("owner-1", ownerCh)
	s.Wings.Subscribe("member-1", memberCh)
	s.Wings.Subscribe("outsider-1", outsiderCh)

	wing := &ConnectedWing{
		ID:       "conn-1",
		UserID:   "owner-1",
		WingID:   "wing-1",
		Hostname: "host",
		OrgID:    "org-1",
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
	s.Wings.Subscribe("user-a", aCh)
	s.Wings.Subscribe("user-b", bCh)

	wing := &ConnectedWing{
		ID:       "conn-a",
		UserID:   "user-a",
		WingID:   "wing-a",
		Hostname: "host-a",
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

// --- Comprehensive: all session-scoped operations matrix ---

func TestAuthzSessionOperationsMatrix(t *testing.T) {
	s, wing, ownerID, memberID, outsiderID := setupAuthzServer(t)

	sess := &PTYSession{ID: "matrix-pty", WingID: wing.ID, UserID: ownerID, Status: "active"}
	s.PTY.Add(sess)
	cs := &ChatSession{ID: "matrix-chat", WingID: wing.ID, UserID: ownerID, Status: "active"}
	s.Chat.Add(cs)

	actors := []struct {
		name   string
		userID string
		allow  bool
	}{
		{"owner", ownerID, true},
		{"org-member", memberID, true},
		{"outsider", outsiderID, false},
	}

	for _, actor := range actors {
		t.Run("pty/"+actor.name, func(t *testing.T) {
			session, _ := s.getAuthorizedPTY(actor.userID, "matrix-pty")
			if actor.allow && session == nil {
				t.Errorf("%s should have PTY access", actor.name)
			}
			if !actor.allow && session != nil {
				t.Errorf("%s should NOT have PTY access", actor.name)
			}
		})
	}

	for _, actor := range actors {
		t.Run("chat/"+actor.name, func(t *testing.T) {
			chat, _ := s.getAuthorizedChat(actor.userID, "matrix-chat")
			if actor.allow && chat == nil {
				t.Errorf("%s should have chat access", actor.name)
			}
			if !actor.allow && chat != nil {
				t.Errorf("%s should NOT have chat access", actor.name)
			}
		})
	}

	for _, actor := range actors {
		t.Run("canAccessSession/"+actor.name, func(t *testing.T) {
			got := s.canAccessSession(actor.userID, sess)
			if got != actor.allow {
				t.Errorf("canAccessSession(%s) = %v, want %v", actor.name, got, actor.allow)
			}
		})
	}
}
