package relay

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func linkGoogleCallback(t *testing.T, s *Server, subject, email string, verified bool) *httptest.ResponseRecorder {
	t.Helper()
	token := "link_" + generateToken()
	mustTest(t, s.Store.createSignInLink(token, "owner", "google", ""))
	r := signInRequest(http.MethodGet, "/auth/google/callback?code=fixture&state="+token, "owner-session")
	r.AddCookie(&http.Cookie{Name: "oauth_state", Value: token})
	return googleRecoveryCallback(t, s, subject, email, verified, r)
}

func requireGoogleLogin(t *testing.T, s *Server, subject, email, wantID string) *httptest.ResponseRecorder {
	t.Helper()
	w := googleRecoveryCallback(t, s, subject, email, true)
	if u := recoverySession(t, s, w); w.Code != http.StatusSeeOther || u == nil || u.ID != wantID {
		t.Fatalf("login %s: status=%d user=%#v body=%s", subject, w.Code, u, w.Body)
	}
	return w
}

func TestSignInGoogleLinkedSubjectLifecycle(t *testing.T) {
	for _, origin := range []string{"explicit_link", "verified_recovery"} {
		for _, address := range []string{" Backup@Example.COM ", "back.up@example.com", "backup+travel@example.com"} {
			t.Run(origin+"/"+address, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "relay.db")
				store, err := OpenRelay(path)
				mustTest(t, err)
				t.Cleanup(func() { _ = store.Close() })
				ownerEmail := "owner@example.com"
				if origin == "verified_recovery" {
					ownerEmail = address
				}
				recoveryUser(t, store, "owner", "github", ownerEmail)
				mustTest(t, store.CreateSession("owner-session", "owner", time.Now().Add(time.Hour)))
				mustTest(t, store.CreateDeviceToken("owner-machine", "owner", "wing", nil))
				s := NewServer(store, ServerConfig{GoogleClientID: "fixture"})
				if origin == "explicit_link" {
					if w := linkGoogleCallback(t, s, "opaque-owner-subject", address, true); w.Code != http.StatusSeeOther {
						t.Fatalf("link: %d %s", w.Code, w.Body)
					}
				} else {
					requireGoogleLogin(t, s, "opaque-owner-subject", address, "owner")
					requireGoogleLogin(t, s, "opaque-owner-subject", "owner@example.com", "owner")
				}
				// An independent Google account claims the linked address by a
				// profile refresh after initially signing up with a different one.
				first := googleRecoveryCallback(t, s, "independent-subject", "independent@example.com", true)
				other := recoverySession(t, s, first)
				if other == nil || other.ID == "owner" {
					t.Fatalf("independent signup: %#v", other)
				}
				normalized, err := normalizeRecoveryEmail(address)
				mustTest(t, err)
				requireGoogleLogin(t, s, "independent-subject", normalized, other.ID)
				mustTest(t, store.CreateDeviceToken("other-machine", other.ID, "other-wing", nil))
				requireGoogleLogin(t, s, "opaque-owner-subject", address, "owner")
				// The linked subject then changes to another account's current
				// verified address. Owning a subject does not transfer an address.
				changed := "independent-current@example.com"
				requireGoogleLogin(t, s, "independent-subject", changed, other.ID)
				requireGoogleLogin(t, s, "opaque-owner-subject", " Independent-Current@Example.COM ", "owner")
				r := signInRequest(http.MethodDelete, "/api/app/signin/github", "owner-session")
				w := httptest.NewRecorder()
				s.ServeHTTP(w, r)
				if w.Code != http.StatusOK {
					t.Fatalf("primary removal: %d %s", w.Code, w.Body)
				}
				for cycle := 0; cycle < 2; cycle++ {
					if cycle > 0 {
						mustTest(t, store.Close())
						store, err = OpenRelay(path)
						mustTest(t, err)
						s = NewServer(store, ServerConfig{GoogleClientID: "fixture"})
					}
					for repeat := 0; repeat < 2; repeat++ {
						requireGoogleLogin(t, s, "opaque-owner-subject", changed, "owner")
					}
					if u, err := store.GetSession("owner-session"); err != nil || u == nil || u.ID != "owner" {
						t.Fatalf("existing session: %#v %v", u, err)
					}
					if u := recoverySession(t, s, first); u == nil || u.ID != other.ID {
						t.Fatalf("other session: %#v", u)
					}
					for _, tc := range []struct{ token, user, wing string }{{"owner-machine", "owner", "wing"}, {"other-machine", other.ID, "other-wing"}} {
						u, wing, err := store.ValidateToken(tc.token)
						if err != nil || u != tc.user || wing != tc.wing {
							t.Fatalf("machine: %s/%s %v", u, wing, err)
						}
					}
					if s.canAccessWing("owner", &ConnectedWing{UserID: other.ID, WingID: "other-wing"}) {
						t.Fatal("cross-account machine access")
					}
					for _, tc := range []struct{ id, email string }{{"owner", "owner@example.com"}, {other.ID, changed}} {
						u, err := store.GetUserByID(tc.id)
						if err != nil || u == nil || u.Email == nil || *u.Email != tc.email {
							t.Fatalf("contact changed: %#v %v", u, err)
						}
					}
					var provenance, proof string
					mustTest(t, store.DB().QueryRow("SELECT provenance, verified_email FROM signin_identity_proofs WHERE user_id='owner'").Scan(&provenance, &proof))
					if provenance != origin || proof != changed {
						t.Fatalf("proof: %q %q", provenance, proof)
					}
				}
				// Dots and plus suffixes remain distinct, including magic login.
				var users, sessions int
				mustTest(t, store.DB().QueryRow("SELECT COUNT(*) FROM users").Scan(&users))
				mustTest(t, store.DB().QueryRow("SELECT COUNT(*) FROM sessions").Scan(&sessions))
				for _, alias := range []string{"o.wner@example.com", "owner+alias@example.com"} {
					token := generateToken()
					mustTest(t, store.CreateMagicLink(token, alias, token, time.Now().Add(time.Minute)))
					w := httptest.NewRecorder()
					s.handleMagicVerify(w, signInRequest(http.MethodGet, "/auth/magic/verify?token="+token, ""))
					if w.Code != http.StatusForbidden || recoverySession(t, s, w) != nil {
						t.Fatalf("alias accepted: %d", w.Code)
					}
				}
				var afterUsers, afterSessions int
				mustTest(t, store.DB().QueryRow("SELECT COUNT(*) FROM users").Scan(&afterUsers))
				mustTest(t, store.DB().QueryRow("SELECT COUNT(*) FROM sessions").Scan(&afterSessions))
				if users != afterUsers || sessions != afterSessions {
					t.Fatal("alias created user/session")
				}
				// Add a verified email through the actual consumed-token callback,
				// remove Google through the route, reopen, then refuse its return.
				token := "link_" + generateToken()
				mustTest(t, store.createSignInLink(token, "owner", "email", "owner@example.com"))
				w = httptest.NewRecorder()
				s.handleMagicVerify(w, signInRequest(http.MethodGet, "/auth/magic/verify?token="+token, "owner-session"))
				if w.Code != http.StatusSeeOther {
					t.Fatalf("email add: %d %s", w.Code, w.Body)
				}
				w = httptest.NewRecorder()
				s.ServeHTTP(w, signInRequest(http.MethodDelete, "/api/app/signin/google", "owner-session"))
				if w.Code != http.StatusOK {
					t.Fatalf("remove google: %d %s", w.Code, w.Body)
				}
				mustTest(t, store.Close())
				store, err = OpenRelay(path)
				mustTest(t, err)
				s = NewServer(store, ServerConfig{})
				w = googleRecoveryCallback(t, s, "opaque-owner-subject", changed, true)
				if w.Code != http.StatusForbidden || recoverySession(t, s, w) != nil {
					t.Fatalf("removed subject returned: %d %s", w.Code, w.Body)
				}
				if u, err := store.GetUserByProvider("google", "opaque-owner-subject"); err != nil || u != nil {
					t.Fatalf("removed subject restored: %#v %v", u, err)
				}
				if u, err := store.GetUserByProvider("github", "owner"); err != nil || u != nil {
					t.Fatalf("removed primary restored: %#v %v", u, err)
				}
			})
		}
	}
}

func assertEnrollmentSync(t *testing.T, s *Server, want bool) {
	t.Helper()
	if got := s.roostUserIDAllowed("owner"); got != want {
		t.Fatalf("user authorization=%v want=%v", got, want)
	}
	w := httptest.NewRecorder()
	s.handleInternalEntitlements(w, httptest.NewRequest(http.MethodGet, "/internal/entitlements", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("entitlements: %d %s", w.Code, w.Body)
	}
	var entries []EntitlementEntry
	mustTest(t, json.Unmarshal(w.Body.Bytes(), &entries))
	found := false
	for _, e := range entries {
		if e.UserID == "owner" {
			found = true
			if e.Enrolled != want {
				t.Fatalf("edge enrollment=%v want=%v", e.Enrolled, want)
			}
		}
	}
	if !found {
		t.Fatal("owner missing from enrollment synchronization")
	}
	login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.handleInternalEntitlements(w, r) }))
	defer login.Close()
	edge := NewServer(testStore(t), ServerConfig{NodeRole: "edge", LoginNodeAddr: login.URL, RoostAllowedEmails: s.Config.RoostAllowedEmails})
	edge.EntitlementCache = NewEntitlementCache(login.URL)
	edge.EntitlementCache.fetch(t.Context())
	if got := edge.roostUserIDAllowed("owner"); got != want {
		t.Fatalf("edge authorization=%v want=%v", got, want)
	}
}

func TestSignInGoogleLinkedSubjectEnrollmentAndErrors(t *testing.T) {
	for _, private := range []bool{false, true} {
		for _, invalid := range []string{"missing", "unverified", "disallowed"} {
			if !private && invalid == "disallowed" {
				continue
			} // Public roosts have no address allowlist.
			t.Run(fmt.Sprintf("private=%v/%s", private, invalid), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "relay.db")
				store, err := OpenRelay(path)
				mustTest(t, err)
				t.Cleanup(func() { _ = store.Close() })
				recoveryUser(t, store, "owner", "github", "owner@example.com")
				mustTest(t, store.CreateSession("owner-session", "owner", time.Now().Add(time.Hour)))
				config := ServerConfig{GoogleClientID: "fixture"}
				if private {
					config.RoostAllowedEmails = []string{"owner@example.com", "backup@example.com"}
				}
				s := NewServer(store, config)
				email, verified := "backup@example.com", true
				switch invalid {
				case "missing":
					email = ""
				case "unverified":
					verified = false
				case "disallowed":
					email = "outside@example.com"
				}
				if w := linkGoogleCallback(t, s, "owner-google", email, verified); w.Code != http.StatusForbidden {
					t.Fatalf("invalid link: %d %s", w.Code, w.Body)
				}
				if proved, err := store.hasGoogleSignInProof("owner", "owner-google"); err != nil || proved {
					t.Fatalf("invalid link trusted: %v %v", proved, err)
				}
				if w := linkGoogleCallback(t, s, "owner-google", "backup@example.com", true); w.Code != http.StatusSeeOther {
					t.Fatalf("valid link: %d %s", w.Code, w.Body)
				}
				claimant := recoverySession(t, s, googleRecoveryCallback(t, s, "claimant", "backup@example.com", true))
				if claimant == nil || claimant.ID == "owner" {
					t.Fatalf("claimant: %#v", claimant)
				}
				first := requireGoogleLogin(t, s, "owner-google", " Backup@Example.COM ", "owner")
				assertEnrollmentSync(t, s, true)
				denied := googleRecoveryCallback(t, s, "owner-google", email, verified)
				if denied.Code != http.StatusForbidden || recoverySession(t, s, denied) != nil || recoverySession(t, s, first) != nil {
					t.Fatalf("invalid current proof retained authorization: %d %s", denied.Code, denied.Body)
				}
				assertEnrollmentSync(t, s, false)
				mustTest(t, store.Close())
				store, err = OpenRelay(path)
				mustTest(t, err)
				s = NewServer(store, config)
				assertEnrollmentSync(t, s, false)
				second := requireGoogleLogin(t, s, "owner-google", "backup@example.com", "owner")
				assertEnrollmentSync(t, s, true)
				mustTest(t, store.Close())
				store, err = OpenRelay(path)
				mustTest(t, err)
				s = NewServer(store, config)
				if recoverySession(t, s, second) == nil {
					t.Fatal("allowed conflicting enrollment lost on reopen")
				}
				assertEnrollmentSync(t, s, true)
				other, err := store.GetUserByID(claimant.ID)
				if err != nil || other.Email == nil || *other.Email != "backup@example.com" {
					t.Fatalf("claimant address stolen: %#v %v", other, err)
				}
			})
		}
		for _, conflict := range []bool{false, true} {
			t.Run(fmt.Sprintf("private=%v/database_failure/contact_conflict=%v", private, conflict), func(t *testing.T) {
				store := testStore(t)
				recoveryUser(t, store, "owner", "github", "owner@example.com")
				mustTest(t, store.CreateSession("owner-session", "owner", time.Now().Add(time.Hour)))
				config := ServerConfig{GoogleClientID: "fixture"}
				if private {
					config.RoostAllowedEmails = []string{"owner@example.com", "backup@example.com"}
				}
				s := NewServer(store, config)
				if w := linkGoogleCallback(t, s, "owner-google", "backup@example.com", true); w.Code != http.StatusSeeOther {
					t.Fatalf("link: %d", w.Code)
				}
				if conflict {
					claimant := recoverySession(t, s, googleRecoveryCallback(t, s, "claimant", "backup@example.com", true))
					if claimant == nil || claimant.ID == "owner" {
						t.Fatalf("claimant: %#v", claimant)
					}
					mustMigrationExec(t, store.DB(), `CREATE TRIGGER unrelated_proof_failure BEFORE UPDATE ON signin_identity_proofs BEGIN SELECT RAISE(ABORT, 'unrelated proof failure'); END`)
				} else {
					mustMigrationExec(t, store.DB(), `CREATE TRIGGER unrelated_contact_failure BEFORE UPDATE OF email ON users BEGIN SELECT RAISE(ABORT, 'unrelated storage failure'); END`)
				}
				var before, after int
				mustTest(t, store.DB().QueryRow("SELECT COUNT(*) FROM sessions").Scan(&before))
				w := googleRecoveryCallback(t, s, "owner-google", "backup@example.com", true)
				if w.Code != http.StatusInternalServerError || recoverySession(t, s, w) != nil {
					t.Fatalf("database failure swallowed: %d %s", w.Code, w.Body)
				}
				mustTest(t, store.DB().QueryRow("SELECT COUNT(*) FROM sessions").Scan(&after))
				if before != after {
					t.Fatal("database failure issued a session")
				}
			})
		}
		t.Run(fmt.Sprintf("private=%v/legacy_ambiguity", private), func(t *testing.T) {
			store := testStore(t)
			recoveryUser(t, store, "owner", "github", "owner@example.com")
			recoveryUser(t, store, "legacy", "google", "legacy@example.com")
			config := ServerConfig{}
			if private {
				config.RoostAllowedEmails = []string{"owner@example.com", "legacy@example.com"}
			}
			s := NewServer(store, config)
			w := googleRecoveryCallback(t, s, "legacy", "OWNER@EXAMPLE.COM", true)
			if w.Code != http.StatusForbidden || recoverySession(t, s, w) != nil {
				t.Fatalf("legacy primary excused ambiguity: %d %s", w.Code, w.Body)
			}
			if proved, err := store.hasGoogleSignInProof("legacy", "legacy"); err != nil || proved {
				t.Fatalf("invented legacy proof: %v %v", proved, err)
			}
		})
	}
}
