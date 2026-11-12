package relay

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/testsocket"
)

// This exercises the published failure sequence through real callbacks with
// in-memory provider responses, without contacting either OAuth provider.
func TestReviewerGoogleAddressClaimedAfterLink(t *testing.T) {
	for _, restart := range []bool{false, true} {
		name := "same_process"
		if restart {
			name = "after_restart"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(testsocket.Dir(t), "relay.db")
			store, err := OpenRelay(path)
			mustTest(t, err)
			t.Cleanup(func() { _ = store.Close() })
			recoveryUser(t, store, "owner", "github", "owner@example.com")
			mustTest(t, store.CreateSession("owner-session", "owner", time.Now().Add(time.Hour)))
			mustTest(t, store.createSignInLink("link_review", "owner", "google", ""))
			s := NewServer(store, ServerConfig{GoogleClientID: "fixture"})
			r := signInRequest(http.MethodGet, "/auth/google/callback?code=fixture&state=link_review", "owner-session")
			r.AddCookie(&http.Cookie{Name: "oauth_state", Value: "link_review"})
			linked := googleRecoveryCallback(t, s, "owner-google", "backup@example.com", true, r)
			if linked.Code != http.StatusSeeOther {
				t.Fatalf("link failed: %d %s", linked.Code, linked.Body)
			}
			owner, err := store.GetUserByProvider("google", "owner-google")
			if err != nil || owner == nil || owner.ID != "owner" {
				t.Fatalf("link ownership: %#v %v", owner, err)
			}
			t.Logf("Google link accepted for owner; stored contact=%v", *owner.Email)
			claim := googleRecoveryCallback(t, s, "claimant-google", "backup@example.com", true)
			claimant := recoverySession(t, s, claim)
			if claim.Code != http.StatusSeeOther || claimant == nil || claimant.ID == "owner" {
				t.Fatalf("claim setup: status=%d user=%#v body=%s", claim.Code, claimant, claim.Body)
			}
			t.Logf("different Google signup claimed backup address; claimant=%s", claimant.ID)
			mustTest(t, store.RemoveSignInMethod("owner", "github"))
			if restart {
				mustTest(t, store.Close())
				store, err = OpenRelay(path)
				mustTest(t, err)
				s = NewServer(store, ServerConfig{GoogleClientID: "fixture"})
			}
			for attempt := 0; attempt < 2; attempt++ {
				login := googleRecoveryCallback(t, s, "owner-google", "backup@example.com", true)
				user := recoverySession(t, s, login)
				if login.Code != http.StatusSeeOther || user == nil || user.ID != "owner" {
					t.Fatalf("sole linked method unusable: status=%d user=%#v body=%s", login.Code, user, login.Body)
				}
			}
			var usersBefore, sessionsBefore, usersAfter, sessionsAfter int
			mustTest(t, store.DB().QueryRow("SELECT COUNT(*) FROM users").Scan(&usersBefore))
			mustTest(t, store.DB().QueryRow("SELECT COUNT(*) FROM sessions").Scan(&sessionsBefore))
			mustTest(t, store.CreateMagicLink("alias", "owner+alias@example.com", "alias", time.Now().Add(time.Minute)))
			alias := httptest.NewRecorder()
			s.handleMagicVerify(alias, signInRequest(http.MethodGet, "/auth/magic/verify?token=alias", ""))
			if alias.Code != http.StatusForbidden || recoverySession(t, s, alias) != nil {
				t.Fatalf("alias accepted: %d", alias.Code)
			}
			mustTest(t, store.DB().QueryRow("SELECT COUNT(*) FROM users").Scan(&usersAfter))
			mustTest(t, store.DB().QueryRow("SELECT COUNT(*) FROM sessions").Scan(&sessionsAfter))
			if usersBefore != usersAfter || sessionsBefore != sessionsAfter {
				t.Fatal("alias created a user or session")
			}
			other, err := store.GetUserByProvider("google", "claimant-google")
			if err != nil || other == nil || other.ID != claimant.ID || other.Email == nil || *other.Email != "backup@example.com" {
				t.Fatalf("claimant changed: %#v %v", other, err)
			}
		})
	}
}

func TestReviewerMismatchRefusal(t *testing.T) {
	store := testStore(t)
	recoveryUser(t, store, "owner", "github", "owner@example.com")
	mustTest(t, store.CreateMagicLink("review-mismatch", "owner+alias@example.com", "review-mismatch", time.Now().Add(time.Minute)))
	s := NewServer(store, ServerConfig{})
	w := httptest.NewRecorder()
	s.handleMagicVerify(w, signInRequest(http.MethodGet, "/auth/magic/verify?token=review-mismatch", ""))
	if user := recoverySession(t, s, w); w.Code != http.StatusForbidden || user != nil {
		t.Fatalf("mismatched address was not refused: status=%d user=%#v", w.Code, user)
	}
	var count int
	mustTest(t, store.DB().QueryRow("SELECT COUNT(*) FROM users").Scan(&count))
	if count != 1 {
		t.Fatalf("mismatch created an account: %d", count)
	}
}
