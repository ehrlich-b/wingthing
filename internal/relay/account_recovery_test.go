package relay

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/testsocket"
)

type recoveryTransport func(*http.Request) (*http.Response, error)

func (f recoveryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func recoveryUser(t *testing.T, store *RelayStore, id, provider, email string) {
	t.Helper()
	mustTest(t, store.UpsertUser(&User{ID: id, Provider: provider, ProviderID: id, DisplayName: id}))
	if email != "" {
		mustTest(t, store.UpdateUserEmail(id, email))
	}
}

// Exercise the real callback with fixed in-memory provider responses. No
// provider server, network request, production account or login is involved.
func googleRecoveryCallback(t *testing.T, s *Server, id, email string, verified bool, requests ...*http.Request) *httptest.ResponseRecorder {
	t.Helper()
	s.oauthHTTPClient = &http.Client{Transport: recoveryTransport(func(r *http.Request) (*http.Response, error) {
		var payload any
		switch r.URL.String() {
		case "https://oauth2.googleapis.com/token":
			payload = map[string]any{"access_token": "fixture-access-token"}
		case "https://www.googleapis.com/oauth2/v2/userinfo":
			if r.Header.Get("Authorization") != "Bearer fixture-access-token" {
				t.Fatal("userinfo request omitted provider token")
			}
			payload = map[string]any{"id": id, "email": email, "verified_email": verified, "name": "Recovered user"}
		default:
			t.Fatalf("unexpected provider request: %s", r.URL)
		}
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
	})}
	r := httptest.NewRequest(http.MethodGet, "/auth/google/callback?code=fixture-code&state=fixture-state", nil)
	r.AddCookie(&http.Cookie{Name: "oauth_state", Value: "fixture-state"})
	if len(requests) > 0 {
		r = requests[0]
	}
	w := httptest.NewRecorder()
	s.handleGoogleCallback(w, r)
	return w
}

func recoverySession(t *testing.T, s *Server, w *httptest.ResponseRecorder) *User {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookieNameForChannel() {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.AddCookie(c)
			return s.sessionUser(r)
		}
	}
	return nil
}

func TestGoogleRecoveryPreservesMachinesSessionsAndDurableIdentity(t *testing.T) {
	path := filepath.Join(testsocket.Dir(t), "relay.db")
	store, err := OpenRelay(path)
	mustTest(t, err)
	t.Cleanup(func() { _ = store.Close() })
	recoveryUser(t, store, "github-owner", "github", " Owner@Example.COM ")
	mustTest(t, store.CreateSession("existing-session", "github-owner", time.Now().Add(time.Hour)))
	mustTest(t, store.CreateDeviceToken("machine-token", "github-owner", "existing-machine", nil))
	s := NewServer(store, ServerConfig{})
	wing := &ConnectedWing{UserID: "github-owner", WingID: "existing-machine"}
	w := googleRecoveryCallback(t, s, "google-subject", "owner@example.com", true)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("callback status=%d body=%s", w.Code, w.Body)
	}
	user := recoverySession(t, s, w)
	if user == nil || user.ID != "github-owner" || user.Provider != "github" || !s.canAccessWing(user.ID, wing) {
		t.Fatalf("recovery lost original identity or machine access: %#v", user)
	}
	old, err := store.GetSession("existing-session")
	if err != nil || old == nil || old.ID != user.ID {
		t.Fatalf("existing session=%#v err=%v", old, err)
	}
	owner, device, err := store.ValidateToken("machine-token")
	if err != nil || owner != user.ID || device != wing.WingID {
		t.Fatalf("machine ownership=%s/%s err=%v", owner, device, err)
	}
	var count int
	mustTest(t, store.DB().QueryRow("SELECT COUNT(*) FROM users").Scan(&count))
	if count != 1 {
		t.Fatalf("created %d users, want one original user", count)
	}
	mustTest(t, store.Close())
	store, err = OpenRelay(path)
	mustTest(t, err)
	s = NewServer(store, ServerConfig{})
	for _, provider := range []string{"github", "google"} {
		id := "github-owner"
		if provider == "google" {
			id = "google-subject"
		}
		linked, err := store.GetUserByProvider(provider, id)
		if err != nil || linked == nil || linked.ID != "github-owner" {
			t.Fatalf("durable %s identity=%#v err=%v", provider, linked, err)
		}
	}
	// Once linked, the provider subject resolves directly even if its current
	// verified address no longer matches the original recovery address.
	w = googleRecoveryCallback(t, s, "google-subject", "new-address@example.com", true)
	if user := recoverySession(t, s, w); w.Code != http.StatusSeeOther || user == nil || user.ID != "github-owner" {
		t.Fatalf("later direct login status=%d user=%#v", w.Code, user)
	}
}

func TestGoogleRecoveryRefusesUnsafeEmailAndIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, email string
		verified    bool
		setup       func(*testing.T, *RelayStore)
		denied      bool
	}{
		{name: "unverified exact email", email: "owner@example.com"},
		{name: "different verified email", email: "attacker@example.com", verified: true},
		{name: "partial local name", email: "own@example.com", verified: true},
		{name: "partial domain", email: "owner@example.co", verified: true},
		{name: "plus alias", email: "owner+tag@example.com", verified: true},
		{name: "dot alias", email: "o.wner@example.com", verified: true},
		{name: "missing email", verified: true},
		{name: "invalid email", email: "owner", verified: true},
		{name: "ambiguous normalized email", email: "owner@example.com", verified: true, denied: true, setup: func(t *testing.T, store *RelayStore) {
			recoveryUser(t, store, "second-github", "github", "OWNER@EXAMPLE.COM")
		}},
		{name: "ambiguous across providers", email: "owner@example.com", verified: true, denied: true, setup: func(t *testing.T, store *RelayStore) {
			recoveryUser(t, store, "other-user", "google", "OWNER@EXAMPLE.COM")
		}},
		{name: "different google identity already linked", email: "owner@example.com", verified: true, denied: true, setup: func(t *testing.T, store *RelayStore) {
			_, err := store.RecoverGitHubUser("google", "original-google-subject", "owner@example.com", true)
			mustTest(t, err)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := testStore(t)
			recoveryUser(t, store, "github-owner", "github", "owner@example.com")
			mustTest(t, store.CreateSession("victim-session", "github-owner", time.Now().Add(time.Hour)))
			if tc.setup != nil {
				tc.setup(t, store)
			}
			s := NewServer(store, ServerConfig{})
			w := googleRecoveryCallback(t, s, "attacker-subject", tc.email, tc.verified)
			user := recoverySession(t, s, w)
			if tc.denied && (w.Code != http.StatusForbidden || user != nil) {
				t.Fatalf("unsafe recovery status=%d user=%#v body=%s", w.Code, user, w.Body)
			}
			if !tc.denied && (w.Code != http.StatusSeeOther || user == nil) {
				t.Fatalf("separate signup status=%d user=%#v body=%s", w.Code, user, w.Body)
			}
			wing := &ConnectedWing{UserID: "github-owner", WingID: "victim-machine"}
			if user != nil && (user.ID == "github-owner" || s.canAccessWing(user.ID, wing)) {
				t.Fatalf("attacker gained victim machine access: %#v", user)
			}
			old, err := store.GetSession("victim-session")
			if err != nil || old == nil || old.ID != "github-owner" {
				t.Fatalf("victim session changed: %#v %v", old, err)
			}
			victim, err := store.GetUserByID("github-owner")
			if err != nil || victim.Email == nil || *victim.Email != "owner@example.com" {
				t.Fatalf("victim email changed: %#v %v", victim, err)
			}
			if user != nil && !tc.verified && user.Email != nil {
				t.Fatalf("unverified email stored as recovery credential: %#v", user)
			}
		})
	}
}

func TestGoogleRecoveryRefusesLegacyEmailAmbiguity(t *testing.T) {
	for _, storedEmail := range []string{"", "other@example.com", "OWNER@example.com"} {
		t.Run("stored email="+storedEmail, func(t *testing.T) {
			store := testStore(t)
			legacy := &User{ID: "legacy-email-owner", Provider: "email", ProviderID: " Owner@Example.COM "}
			mustTest(t, store.UpsertUser(legacy))
			if storedEmail != "" {
				mustTest(t, store.UpdateUserEmail(legacy.ID, storedEmail))
			}
			recoveryUser(t, store, "github-owner", "github", "owner@example.com")
			s := NewServer(store, ServerConfig{})
			w := googleRecoveryCallback(t, s, "new-google-subject", "owner@example.com", true)
			if user := recoverySession(t, s, w); w.Code != http.StatusForbidden || user != nil {
				t.Fatalf("ambiguous legacy email recovery status=%d user=%#v body=%s", w.Code, user, w.Body)
			}
			linked, err := store.GetUserByProvider("google", "new-google-subject")
			if err != nil || linked != nil {
				t.Fatalf("refused recovery registered identity: %#v %v", linked, err)
			}
			var sessions int
			mustTest(t, store.DB().QueryRow("SELECT COUNT(*) FROM sessions").Scan(&sessions))
			if sessions != 0 {
				t.Fatalf("refused recovery created %d sessions", sessions)
			}
			unchanged, err := store.GetUserByID(legacy.ID)
			if err != nil || unchanged == nil {
				t.Fatalf("legacy user missing: %#v %v", unchanged, err)
			}
			if (storedEmail == "" && unchanged.Email != nil) || (storedEmail != "" && (unchanged.Email == nil || *unchanged.Email != storedEmail)) {
				t.Fatalf("legacy email changed: %#v", unchanged.Email)
			}
		})
	}
}

func TestGoogleRecoveryCountsEachUserOnce(t *testing.T) {
	store := testStore(t)
	recoveryUser(t, store, "github-owner", "github", "owner@example.com")
	_, err := store.RecoverGitHubUser("email", " Owner@Example.COM ", "owner@example.com", true)
	mustTest(t, err)
	s := NewServer(store, ServerConfig{})
	w := googleRecoveryCallback(t, s, "new-google-subject", "owner@example.com", true)
	if user := recoverySession(t, s, w); w.Code != http.StatusSeeOther || user == nil || user.ID != "github-owner" {
		t.Fatalf("same user counted twice: status=%d user=%#v body=%s", w.Code, user, w.Body)
	}
}

func TestGoogleRecoveryRequiresStoredGitHubEmail(t *testing.T) {
	store := testStore(t)
	recoveryUser(t, store, "github-owner", "github", "owner@example.com")
	_, err := store.RecoverGitHubUser("email", "owner@example.com", "owner@example.com", true)
	mustTest(t, err)
	mustTest(t, store.UpdateUserEmail("github-owner", "new@example.com"))
	s := NewServer(store, ServerConfig{})
	w := googleRecoveryCallback(t, s, "new-google-subject", "owner@example.com", true)
	if user := recoverySession(t, s, w); user != nil && user.ID == "github-owner" {
		t.Fatal("recovered GitHub account without matching its stored email")
	}
	linked, err := store.GetUserByProvider("google", "new-google-subject")
	if err != nil || (linked != nil && linked.ID == "github-owner") {
		t.Fatalf("linked Google identity without matching stored email: %#v %v", linked, err)
	}
}

func TestGoogleRecoveryExistingIdentityNeverMergesAnotherAccount(t *testing.T) {
	store := testStore(t)
	recoveryUser(t, store, "github-owner", "github", "owner@example.com")
	recoveryUser(t, store, "existing-google", "google", "old@example.com")
	s := NewServer(store, ServerConfig{})
	w := googleRecoveryCallback(t, s, "existing-google", "OWNER@EXAMPLE.COM", true)
	if user := recoverySession(t, s, w); user != nil && user.ID == "github-owner" {
		t.Fatal("existing Google account merged into GitHub owner")
	}
	linked, err := store.GetUserByProvider("google", "existing-google")
	if err != nil || linked == nil || linked.ID != "existing-google" {
		t.Fatalf("existing identity moved: %#v %v", linked, err)
	}
}

func TestGoogleRecoveryUnverifiedSignupCannotCaptureRecovery(t *testing.T) {
	store := testStore(t)
	recoveryUser(t, store, "github-owner", "github", "owner@example.com")
	s := NewServer(store, ServerConfig{})
	attacker := recoverySession(t, s, googleRecoveryCallback(t, s, "attacker", "owner@example.com", false))
	if attacker == nil || attacker.ID == "github-owner" || attacker.Email != nil {
		t.Fatalf("unverified signup = %#v", attacker)
	}
	w := googleRecoveryCallback(t, s, "real-owner", " OWNER@EXAMPLE.COM ", true)
	owner := recoverySession(t, s, w)
	if w.Code != http.StatusSeeOther || owner == nil || owner.ID != "github-owner" {
		t.Fatalf("attacker blocked legitimate recovery status=%d owner=%#v", w.Code, owner)
	}
	if s.canAccessWing(attacker.ID, &ConnectedWing{UserID: owner.ID, WingID: "victim-machine"}) {
		t.Fatal("unverified signup gained access after legitimate recovery")
	}
}

func TestRecoveryPreservesLegacyMagicIdentity(t *testing.T) {
	store := testStore(t)
	mustTest(t, store.UpsertUser(&User{ID: "legacy", Provider: "email", ProviderID: "Owner@Example.COM"}))
	user, err := store.GetUserForEmailSignIn("owner@example.com")
	if err != nil || user == nil || user.ID != "legacy" {
		t.Fatalf("normalized legacy email identity = %#v %v", user, err)
	}
	mustTest(t, store.UpsertUser(&User{ID: "duplicate", Provider: "email", ProviderID: "OWNER@EXAMPLE.COM"}))
	if user, err := store.GetUserForEmailSignIn("owner@example.com"); user != nil || !errors.Is(err, ErrAccountRecoveryRefused) {
		t.Fatalf("ambiguous legacy email identity = %#v %v", user, err)
	}
}

func TestGoogleRecoveryEnrollmentCheckedBeforeLink(t *testing.T) {
	for _, verified := range []bool{false, true} {
		t.Run(fmt.Sprint(verified), func(t *testing.T) {
			store := testStore(t)
			recoveryUser(t, store, "github-owner", "github", "owner@example.com")
			allowed := "someone-else@example.com"
			if !verified {
				allowed = "owner@example.com"
			}
			s := NewServer(store, ServerConfig{RoostAllowedEmails: []string{allowed}})
			w := googleRecoveryCallback(t, s, "google-subject", "owner@example.com", verified)
			if w.Code != http.StatusForbidden || recoverySession(t, s, w) != nil {
				t.Fatalf("enrollment bypass status=%d", w.Code)
			}
			linked, err := store.GetUserByProvider("google", "google-subject")
			if err != nil || linked != nil {
				t.Fatalf("denied callback registered identity: %#v %v", linked, err)
			}
		})
	}
}

func TestGoogleRecoveryLinkedIdentityStillEnforcesEnrollment(t *testing.T) {
	for _, verified := range []bool{false, true} {
		t.Run(fmt.Sprint(verified), func(t *testing.T) {
			store := testStore(t)
			recoveryUser(t, store, "github-owner", "github", "owner@example.com")
			s := NewServer(store, ServerConfig{RoostAllowedEmails: []string{"owner@example.com"}})
			first := googleRecoveryCallback(t, s, "google-subject", "owner@example.com", true)
			if first.Code != http.StatusSeeOther || recoverySession(t, s, first) == nil {
				t.Fatalf("enrolled recovery status=%d body=%s", first.Code, first.Body)
			}
			email := "owner@example.com"
			if verified {
				email = "unlisted@example.com"
			}
			denied := googleRecoveryCallback(t, s, "google-subject", email, verified)
			if denied.Code != http.StatusForbidden || recoverySession(t, s, denied) != nil || recoverySession(t, s, first) != nil {
				t.Fatal("linked identity bypassed current provider enrollment or retained an enrolled session")
			}
			linked, err := store.GetUserByProvider("google", "google-subject")
			if err != nil || linked == nil || linked.ID != "github-owner" || linked.Email != nil {
				t.Fatalf("denied linked identity = %#v %v", linked, err)
			}
		})
	}
}

func TestMagicLinkRecovery(t *testing.T) {
	for _, tc := range []string{"recover", "ambiguous", "not enrolled", "expired", "invalid"} {
		t.Run(tc, func(t *testing.T) {
			store := testStore(t)
			recoveryUser(t, store, "github-owner", "github", " Owner@Example.COM ")
			s := NewServer(store, ServerConfig{SMTPHost: "fixture"})
			if tc == "ambiguous" {
				recoveryUser(t, store, "second", "github", "OWNER@EXAMPLE.COM")
			}
			if tc == "not enrolled" {
				s.Config.RoostAllowedEmails = []string{"someone-else@example.com"}
			}
			expiry := time.Now().Add(time.Minute)
			if tc == "expired" {
				expiry = time.Now().Add(-time.Minute)
			}
			if tc != "invalid" {
				mustTest(t, store.CreateMagicLink("link-id", "owner@example.com", "fixture-token", expiry))
			}
			verify := func() *httptest.ResponseRecorder {
				w := httptest.NewRecorder()
				s.handleMagicVerify(w, httptest.NewRequest(http.MethodGet, "/auth/magic/verify?token=fixture-token", nil))
				return w
			}
			w := verify()
			user := recoverySession(t, s, w)
			linked, err := store.GetUserByProvider("email", "owner@example.com")
			mustTest(t, err)
			if tc == "recover" {
				if w.Code != http.StatusSeeOther || user == nil || user.ID != "github-owner" || linked == nil || linked.ID != user.ID {
					t.Fatalf("magic recovery status=%d user=%#v linked=%#v", w.Code, user, linked)
				}
				if replay := verify(); replay.Code != http.StatusBadRequest || recoverySession(t, s, replay) != nil {
					t.Fatal("magic link replay authenticated")
				}
				later, err := store.GetUserForEmailSignIn(" OWNER@EXAMPLE.COM ")
				if err != nil || later == nil || later.ID != user.ID {
					t.Fatalf("later email login=%#v %v", later, err)
				}
			} else if user != nil || linked != nil || w.Code < 400 {
				t.Fatalf("unsafe magic recovery status=%d user=%#v linked=%#v", w.Code, user, linked)
			}
		})
	}
}

func TestConcurrentGoogleRecoveryCannotReplaceIdentity(t *testing.T) {
	store, err := OpenRelay(filepath.Join(testsocket.Dir(t), "relay.db"))
	mustTest(t, err)
	t.Cleanup(func() { _ = store.Close() })
	recoveryUser(t, store, "github-owner", "github", "owner@example.com")
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	start := make(chan struct{})
	for _, id := range []string{"first-google", "second-google"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := store.RecoverGitHubUser("google", id, "owner@example.com", true)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	var succeeded, refused int
	for err := range errs {
		if err == nil {
			succeeded++
		} else if errors.Is(err, ErrAccountRecoveryRefused) {
			refused++
		} else {
			t.Fatal(err)
		}
	}
	if succeeded != 1 || refused != 1 {
		t.Fatalf("concurrent recoveries succeeded=%d refused=%d", succeeded, refused)
	}
}

func TestRecoveryLoginPageGitHubOffAndOn(t *testing.T) {
	const recoveryLine = "Signed up with GitHub? Continue with Google or email using the same address and your account carries over."
	for _, github := range []bool{false, true} {
		t.Run(fmt.Sprint(github), func(t *testing.T) {
			cfg := ServerConfig{GoogleClientID: "fixture-google", SMTPHost: "fixture-smtp"}
			if github {
				cfg.GitHubClientID = "fixture-github"
			}
			s := NewServer(testStore(t), cfg)
			w := httptest.NewRecorder()
			s.handleLogin(w, httptest.NewRequest(http.MethodGet, "/login", nil))
			body := w.Body.String()
			if w.Code != http.StatusOK || strings.Contains(body, `href="/auth/github"`) != github || strings.Contains(body, recoveryLine) == github {
				t.Fatalf("GitHub=%v login page status=%d body=%s", github, w.Code, body)
			}
			if !strings.Contains(body, `href="/auth/google"`) || !strings.Contains(body, `action="/auth/magic"`) {
				t.Fatal("remaining login options missing")
			}
			if !github && (strings.Count(body, recoveryLine) != 1 || strings.Index(body, recoveryLine) < strings.Index(body, "</form>")) {
				t.Fatal("recovery line must appear once beneath remaining options")
			}
		})
	}
}

// Recreate the pre-008 database writes: OAuth inserted a user before saving
// its email, while magic-link signup saved only its email provider ID.
func TestRecoveryRefusesOutageIdentityCollisionsAfterUpgrade(t *testing.T) {
	for _, provider := range []string{"google", "email"} {
		for _, address := range []string{"owner@example.com", "OWNER@EXAMPLE.COM"} {
			t.Run(provider+"/"+address, func(t *testing.T) {
				path := filepath.Join(testsocket.Dir(t), "relay.db")
				store, err := OpenRelay(path)
				mustTest(t, err)
				_, err = store.DB().Exec(`DROP TRIGGER users_register_identity;
					DROP TABLE user_identities;
					DELETE FROM schema_migrations WHERE version = '008_user_identities.sql'`)
				mustTest(t, err)
				recoveryUser(t, store, "github-owner", "github", "owner@example.com")
				mustTest(t, store.CreateSession("original-session", "github-owner", time.Now().Add(time.Hour)))
				mustTest(t, store.CreateDeviceToken("original-machine", "github-owner", "wing", nil))
				providerID := "outage-google"
				if provider == "email" {
					providerID = address
				}
				mustTest(t, store.UpsertUser(&User{ID: "outage-user", Provider: provider, ProviderID: providerID}))
				if provider == "google" {
					err := store.UpdateUserEmail("outage-user", address)
					if address == "owner@example.com" && err == nil {
						t.Fatal("old Google callback should fail after persisting the empty user")
					}
					if address != "owner@example.com" {
						mustTest(t, err)
					}
				}
				mustTest(t, store.Close())
				store, err = OpenRelay(path)
				mustTest(t, err)
				t.Cleanup(func() { _ = store.Close() })
				s := NewServer(store, ServerConfig{SMTPHost: "fixture"})
				for attempt := 0; attempt < 2; attempt++ {
					var w *httptest.ResponseRecorder
					if provider == "google" {
						w = googleRecoveryCallback(t, s, providerID, address, true)
					} else {
						token := fmt.Sprintf("outage-token-%d", attempt)
						mustTest(t, store.CreateMagicLink(fmt.Sprint(attempt), address, token, time.Now().Add(time.Minute)))
						w = httptest.NewRecorder()
						s.handleMagicVerify(w, httptest.NewRequest(http.MethodGet, "/auth/magic/verify?token="+token, nil))
					}
					if w.Code != http.StatusForbidden || recoverySession(t, s, w) != nil {
						t.Fatalf("outage collision status=%d body=%s", w.Code, w.Body)
					}
				}
				identity, err := store.GetUserByProvider(provider, providerID)
				if err != nil || identity == nil || identity.ID != "outage-user" {
					t.Fatalf("outage identity changed: %#v %v", identity, err)
				}
				owner, err := store.GetSession("original-session")
				if err != nil || owner == nil || owner.ID != "github-owner" {
					t.Fatalf("original session changed: %#v %v", owner, err)
				}
				userID, _, err := store.ValidateToken("original-machine")
				if err != nil || userID != "github-owner" {
					t.Fatalf("original machine changed: %s %v", userID, err)
				}
				var sessions int
				mustTest(t, store.DB().QueryRow("SELECT COUNT(*) FROM sessions").Scan(&sessions))
				if sessions != 1 {
					t.Fatalf("refused collision created sessions: %d", sessions)
				}
			})
		}
	}
}

func TestRecoveryPreservesNonGitHubGoogleSignupAndRefusesUnlinkedEmail(t *testing.T) {
	for _, original := range []string{"google", "email"} {
		t.Run(original, func(t *testing.T) {
			store := testStore(t)
			address := "owner@example.com"
			if original == "email" {
				mustTest(t, store.UpsertUser(&User{ID: "original", Provider: "email", ProviderID: address}))
			} else {
				recoveryUser(t, store, "original", original, address)
			}
			s := NewServer(store, ServerConfig{SMTPHost: "fixture"})
			var w *httptest.ResponseRecorder
			if original == "email" {
				w = googleRecoveryCallback(t, s, "new-google", address, true)
			} else {
				mustTest(t, store.CreateMagicLink("link", address, "token", time.Now().Add(time.Minute)))
				w = httptest.NewRecorder()
				s.handleMagicVerify(w, httptest.NewRequest(http.MethodGet, "/auth/magic/verify?token=token", nil))
			}
			user := recoverySession(t, s, w)
			if original == "email" {
				if w.Code != http.StatusSeeOther || user == nil || user.ID == "original" {
					t.Fatalf("Google signup changed: status=%d user=%#v body=%s", w.Code, user, w.Body)
				}
			} else if w.Code != http.StatusForbidden || user != nil {
				t.Fatalf("unlinked email must not create a separate account: status=%d user=%#v", w.Code, user)
			}
		})
	}
}

func TestRecoveryExistingEmailThenGoogleLogin(t *testing.T) {
	store := testStore(t)
	s := NewServer(store, ServerConfig{SMTPHost: "fixture"})
	const address = "owner@example.com"
	// Existing email accounts still sign in; only implicit signup is refused.
	mustTest(t, store.UpsertUser(&User{ID: "email-owner", Provider: "email", ProviderID: address}))
	mustTest(t, store.CreateMagicLink("signup-link", address, "signup-token", time.Now().Add(time.Minute)))
	w := httptest.NewRecorder()
	s.handleMagicVerify(w, httptest.NewRequest(http.MethodGet, "/auth/magic/verify?token=signup-token", nil))
	emailUser := recoverySession(t, s, w)
	if w.Code != http.StatusSeeOther || emailUser == nil || emailUser.Provider != "email" {
		t.Fatalf("email signup status=%d user=%#v body=%s", w.Code, emailUser, w.Body)
	}
	var googleUserID string
	for attempt := 1; attempt <= 2; attempt++ {
		w = googleRecoveryCallback(t, s, "new-google", address, true)
		user := recoverySession(t, s, w)
		if w.Code != http.StatusSeeOther || user == nil {
			t.Errorf("Google attempt %d status=%d user=%#v body=%s", attempt, w.Code, user, w.Body)
			continue
		}
		if user.ID == emailUser.ID || user.Provider != "google" {
			t.Fatalf("Google signup changed separate identity: %#v", user)
		}
		if attempt == 1 {
			googleUserID = user.ID
		} else if user.ID != googleUserID {
			t.Fatalf("Google retry changed user: got %s want %s", user.ID, googleUserID)
		}
	}
}

func TestRecoveryRefusalLogsUserIDsWithoutEmail(t *testing.T) {
	for _, tc := range []struct {
		name, provider string
	}{
		{"new ambiguous identity", "google"},
		{"new ambiguous identity", "email"},
		{"existing identity", "google"},
		{"existing identity", "email"},
		{"duplicate email identities", "email"},
		{"different linked identity", "email"},
	} {
		t.Run(tc.provider+"/"+tc.name, func(t *testing.T) {
			store := testStore(t)
			const address = "élodie@example.com"
			recoveryUser(t, store, "github-owner", "github", "\tÉLODIE@example.com\t")
			wantID := "github-owner"
			switch tc.name {
			case "new ambiguous identity":
				recoveryUser(t, store, "other-owner", "github", address)
			case "existing identity", "duplicate email identities":
				providerID := "existing-google"
				if tc.provider == "email" {
					providerID = address
				}
				mustTest(t, store.UpsertUser(&User{ID: "alternate-owner", Provider: tc.provider, ProviderID: providerID}))
				wantID = "alternate-owner"
				if tc.name == "duplicate email identities" {
					mustTest(t, store.UpsertUser(&User{ID: "duplicate-owner", Provider: "email", ProviderID: "ÉLODIE@example.com"}))
				}
			case "different linked identity":
				_, err := store.RecoverGitHubUser("email", "different@example.com", address, true)
				mustTest(t, err)
			}
			var output bytes.Buffer
			previous := log.Writer()
			log.SetOutput(&output)
			t.Cleanup(func() { log.SetOutput(previous) })
			s := NewServer(store, ServerConfig{SMTPHost: "fixture"})
			var w *httptest.ResponseRecorder
			if tc.provider == "google" {
				w = googleRecoveryCallback(t, s, "existing-google", address, true)
			} else {
				mustTest(t, store.CreateMagicLink("link", address, "token", time.Now().Add(time.Minute)))
				w = httptest.NewRecorder()
				s.handleMagicVerify(w, httptest.NewRequest(http.MethodGet, "/auth/magic/verify?token=token", nil))
			}
			if w.Code != http.StatusForbidden || recoverySession(t, s, w) != nil {
				t.Fatalf("refusal status=%d body=%s", w.Code, w.Body)
			}
			entry := output.String()
			if strings.Count(entry, "account recovery refused:") != 1 || !strings.Contains(entry, "provider="+tc.provider) || !strings.Contains(entry, wantID) || strings.Contains(entry, "@") {
				t.Fatalf("refusal log must identify user and provider without email: %q", entry)
			}
			if tc.provider == "google" && !strings.Contains(entry, `provider_id="existing-google"`) {
				t.Fatalf("Google refusal log must identify the provider subject: %q", entry)
			}
		})
	}
}

func TestRecoveryLoginHintRequiresRemainingOption(t *testing.T) {
	for _, google := range []bool{false, true} {
		for _, email := range []bool{false, true} {
			cfg := ServerConfig{}
			if google {
				cfg.GoogleClientID = "fixture"
			}
			if email {
				cfg.SMTPHost = "fixture"
			}
			s := NewServer(testStore(t), cfg)
			w := httptest.NewRecorder()
			s.handleLogin(w, httptest.NewRequest(http.MethodGet, "/login", nil))
			if got := strings.Contains(w.Body.String(), "Signed up with GitHub?"); got != (google || email) {
				t.Errorf("Google=%v email=%v recovery hint=%v", google, email, got)
			}
		}
	}
}
