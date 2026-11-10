package relay

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/testsocket"
)

func signInRequest(method, path, session string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	if session != "" {
		r.AddCookie(&http.Cookie{Name: sessionCookieNameForChannel(), Value: session})
	}
	return r
}

func TestSignInMethodRemoval(t *testing.T) {
	store := testStore(t)
	recoveryUser(t, store, "owner", "github", "owner@example.com")
	recoveryUser(t, store, "other", "google", "other@example.com")
	mustTest(t, store.CreateSession("session", "owner", time.Now().Add(time.Hour)))
	if err := store.RemoveSignInMethod("owner", "github"); !errors.Is(err, ErrLastSignInMethod) {
		t.Fatalf("last removal: %v", err)
	}
	s := NewServer(store, ServerConfig{GoogleClientID: "fixture", SMTPHost: "fixture"})
	call := func(method, path, session string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, signInRequest(method, path, session))
		return w
	}
	w := call(http.MethodDelete, "/api/app/signin/github", "session")
	var refusal map[string]string
	decodeTestJSON(t, w.Body, &refusal)
	if w.Code != http.StatusConflict || refusal["code"] != "last_signin_method" {
		t.Fatalf("typed refusal: %d %#v", w.Code, refusal)
	}
	if w := call(http.MethodDelete, "/api/app/signin/github", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous removal: %d", w.Code)
	}
	mustTest(t, store.LinkSignInMethod("owner", "email", "backup@example.com"))
	w = call(http.MethodGet, "/api/app/signin", "session")
	var list struct {
		Methods []SignInMethod `json:"methods"`
		Google  bool           `json:"can_add_google"`
		Email   bool           `json:"can_add_email"`
	}
	decodeTestJSON(t, w.Body, &list)
	if w.Code != http.StatusOK || len(list.Methods) != 2 || !list.Google || !list.Email {
		t.Fatalf("list: %d %#v", w.Code, list)
	}
	w = call(http.MethodDelete, "/api/app/signin/github", "session")
	if w.Code != http.StatusOK {
		t.Fatalf("remove one of two: %d %s", w.Code, w.Body)
	}
	if u, err := store.GetUserByProvider("github", "owner"); err != nil || u != nil {
		t.Fatalf("removed identity: %#v %v", u, err)
	}
	user, err := store.GetUserByProvider("email", "backup@example.com")
	if err != nil || user == nil || user.ID != "owner" || user.Provider != "email" {
		t.Fatalf("remaining login: %#v %v", user, err)
	}
	if session, err := store.GetSession("session"); err != nil || session == nil || session.ID != user.ID {
		t.Fatalf("session lost: %#v %v", session, err)
	}
	if w := call(http.MethodDelete, "/api/app/signin/email", "session"); w.Code != http.StatusConflict {
		t.Fatalf("last email removal: %d", w.Code)
	}
	if w := call(http.MethodDelete, "/api/app/signin/google", "session"); w.Code != http.StatusNotFound {
		t.Fatalf("other user's method: %d", w.Code)
	}
	if u, err := store.GetUserByProvider("google", "other"); err != nil || u == nil || u.ID != "other" {
		t.Fatalf("other identity changed: %#v %v", u, err)
	}
}

func TestSignInMethodConcurrentRemoval(t *testing.T) {
	store, err := OpenRelay(filepath.Join(testsocket.Dir(t), "relay.db"))
	mustTest(t, err)
	t.Cleanup(func() { _ = store.Close() })
	recoveryUser(t, store, "owner", "github", "owner@example.com")
	mustTest(t, store.LinkSignInMethod("owner", "email", "owner@example.com"))
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, provider := range []string{"github", "email"} {
		wg.Add(1)
		go func() { defer wg.Done(); results <- store.RemoveSignInMethod("owner", provider) }()
	}
	wg.Wait()
	close(results)
	var removed, refused int
	for err := range results {
		if err == nil {
			removed++
		} else if errors.Is(err, ErrLastSignInMethod) {
			refused++
		} else {
			t.Fatal(err)
		}
	}
	if removed != 1 || refused != 1 {
		t.Fatalf("removed=%d refused=%d", removed, refused)
	}
	methods, err := store.ListSignInMethods("owner")
	if err != nil || len(methods) != 1 {
		t.Fatalf("remaining: %#v %v", methods, err)
	}
}

func TestSignInEmailCannotCaptureKnownAddress(t *testing.T) {
	for _, provider := range []string{"github", "email"} {
		t.Run(provider, func(t *testing.T) {
			store := testStore(t)
			recoveryUser(t, store, "owner", "google", "owner@example.com")
			other := &User{ID: "other", Provider: provider, ProviderID: " Backup@Example.com "}
			mustTest(t, store.UpsertUser(other))
			if provider == "github" {
				mustTest(t, store.UpdateUserEmail("other", " Backup@Example.com "))
			}
			if err := store.LinkSignInMethod("owner", "email", "backup@example.com"); !errors.Is(err, ErrIdentityInUse) {
				t.Fatalf("captured %s recovery address: %v", provider, err)
			}
			methods, err := store.ListSignInMethods("owner")
			if err != nil || len(methods) != 1 {
				t.Fatalf("changed methods: %#v %v", methods, err)
			}
		})
	}
}

func TestSignInGoogleSplitHostHandoff(t *testing.T) {
	store := testStore(t)
	recoveryUser(t, store, "owner", "github", "owner@example.com")
	mustTest(t, store.CreateSession("session", "owner", time.Now().Add(time.Hour)))
	s := NewServer(store, ServerConfig{BaseURL: "https://login.example.com", AppHost: "app.example.com", GoogleClientID: "fixture"})
	r := signInRequest(http.MethodPost, "https://app.example.com/api/app/signin/google", "session")
	r.Header.Set("Origin", "null")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther || !strings.HasPrefix(w.Header().Get("Location"), "https://login.example.com/auth/google/link?state=link_") {
		t.Fatalf("handoff: %d %s", w.Code, w.Body)
	}
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("app host set a callback cookie")
	}
	r = signInRequest(http.MethodGet, w.Header().Get("Location"), "session")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("login-host start: %d %s", w.Code, w.Body)
	}
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == "oauth_state" && (cookie.Domain != "" || cookie.Path != "/auth" || !cookie.HttpOnly || !cookie.Secure) {
			t.Fatalf("unsafe callback cookie: %#v", cookie)
		}
	}
	location, err := url.Parse(w.Header().Get("Location"))
	mustTest(t, err)
	r = signInRequest(http.MethodGet, "https://login.example.com/auth/google/callback?code=fixture&state="+location.Query().Get("state"), "session")
	for _, cookie := range w.Result().Cookies() {
		r.AddCookie(cookie)
	}
	w = googleRecoveryCallback(t, s, "google-subject", "owner@example.com", true, r)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "https://app.example.com/#account" {
		t.Fatalf("account return: %d %s", w.Code, w.Body)
	}
}

func TestSignInGoogleLinkProof(t *testing.T) {
	for _, tc := range []string{"verified", "unverified", "wrong session", "expired", "identity in use", "no session"} {
		t.Run(tc, func(t *testing.T) {
			store := testStore(t)
			recoveryUser(t, store, "owner", "github", "owner@example.com")
			recoveryUser(t, store, "other", "github", "other@example.com")
			mustTest(t, store.CreateSession("owner-session", "owner", time.Now().Add(time.Hour)))
			mustTest(t, store.CreateSession("other-session", "other", time.Now().Add(time.Hour)))
			s := NewServer(store, ServerConfig{GoogleClientID: "fixture"})
			start := httptest.NewRecorder()
			s.ServeHTTP(start, signInRequest(http.MethodPost, "/api/app/signin/google", "owner-session"))
			if start.Code != http.StatusSeeOther {
				t.Fatalf("start: %d %s", start.Code, start.Body)
			}
			location, err := url.Parse(start.Header().Get("Location"))
			mustTest(t, err)
			state := location.Query().Get("state")
			if location.Path != "/auth/google/link" {
				t.Fatalf("missing login-host handoff: %s", location)
			}
			start = httptest.NewRecorder()
			s.ServeHTTP(start, signInRequest(http.MethodGet, location.String(), "owner-session"))
			if start.Code != http.StatusSeeOther {
				t.Fatalf("login-host handoff: %d %s", start.Code, start.Body)
			}
			if !isSignInLink(state) {
				t.Fatal("missing bound link state")
			}
			if tc == "expired" {
				mustTestExec(t, store.DB(), "UPDATE signin_links SET expires_at = '2000-01-01'")
			}
			if tc == "identity in use" {
				mustTest(t, store.LinkSignInMethod("other", "google", "google-subject"))
			}
			session := "owner-session"
			if tc == "wrong session" {
				session = "other-session"
			}
			if tc == "no session" {
				session = ""
			}
			r := signInRequest(http.MethodGet, "/auth/google/callback?code=fixture&state="+state, session)
			for _, cookie := range start.Result().Cookies() {
				if cookie.Name == "oauth_state" {
					if cookie.Path != "/auth" {
						t.Fatalf("OAuth cookie path %q", cookie.Path)
					}
					r.AddCookie(cookie)
				}
			}
			w := googleRecoveryCallback(t, s, "google-subject", "another-verified@example.com", tc != "unverified", r)
			linked, err := store.GetUserByProvider("google", "google-subject")
			mustTest(t, err)
			if tc == "verified" {
				if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/app/#account" || linked == nil || linked.ID != "owner" {
					t.Fatalf("link: %d %#v %s", w.Code, linked, w.Body)
				}
				if replay := googleRecoveryCallback(t, s, "google-subject", "another-verified@example.com", true, r); replay.Code != http.StatusForbidden {
					t.Fatalf("replayed link: %d", replay.Code)
				}
			} else if w.Code < 400 || (linked != nil && linked.ID == "owner") {
				t.Fatalf("unsafe link: %d %#v", w.Code, linked)
			}
		})
	}
}

func TestSignInEmailLinkProof(t *testing.T) {
	for _, tc := range []string{"verified", "expired", "unverified", "identity in use", "not enrolled"} {
		t.Run(tc, func(t *testing.T) {
			store := testStore(t)
			recoveryUser(t, store, "owner", "github", "owner@example.com")
			s := NewServer(store, ServerConfig{SMTPHost: "fixture"})
			if tc == "not enrolled" {
				s.Config.RoostAllowedEmails = []string{"owner@example.com"}
			}
			if tc == "identity in use" {
				recoveryUser(t, store, "other", "google", "other@example.com")
				mustTest(t, store.LinkSignInMethod("other", "email", "backup@example.com"))
			}
			if tc != "unverified" {
				mustTest(t, store.createSignInLink("link_fixture", "owner", "email", "backup@example.com"))
			}
			if tc == "expired" {
				mustTestExec(t, store.DB(), "UPDATE signin_links SET expires_at = '2000-01-01'")
			}
			verify := func() *httptest.ResponseRecorder {
				w := httptest.NewRecorder()
				s.handleMagicVerify(w, signInRequest(http.MethodGet, "/auth/magic/verify?token=link_fixture", ""))
				return w
			}
			w := verify()
			linked, err := store.GetUserByProvider("email", "backup@example.com")
			mustTest(t, err)
			if tc == "verified" {
				if w.Code != http.StatusSeeOther || linked == nil || linked.ID != "owner" {
					t.Fatalf("link: %d %#v", w.Code, linked)
				}
				if verify().Code != http.StatusBadRequest {
					t.Fatal("email link replayed")
				}
				mustTest(t, store.CreateMagicLink("login", "backup@example.com", "login-token", time.Now().Add(time.Minute)))
				login := httptest.NewRecorder()
				s.handleMagicVerify(login, signInRequest(http.MethodGet, "/auth/magic/verify?token=login-token", ""))
				if user := recoverySession(t, s, login); user == nil || user.ID != "owner" {
					t.Fatalf("linked email could not sign in: %#v", user)
				}
			} else if w.Code < 400 || (linked != nil && linked.ID == "owner") {
				t.Fatalf("unsafe link: %d %#v", w.Code, linked)
			}
		})
	}
}

func TestSignInEmailRequestSendsProofBeforeLinking(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	mustTest(t, err)
	t.Cleanup(func() { listener.Close() })
	messages := make(chan string, 1)
	failures := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			failures <- err
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		reader := textproto.NewReader(bufio.NewReader(conn))
		fmt.Fprint(conn, "220 localhost fixture\r\n")
		for {
			line, err := reader.ReadLine()
			if err != nil {
				failures <- err
				return
			}
			switch {
			case strings.HasPrefix(line, "EHLO"):
				fmt.Fprint(conn, "250-localhost\r\n250 AUTH PLAIN\r\n")
			case strings.HasPrefix(line, "AUTH PLAIN"):
				fmt.Fprint(conn, "235 Authenticated\r\n")
			case strings.HasPrefix(line, "MAIL FROM"), strings.HasPrefix(line, "RCPT TO"):
				fmt.Fprint(conn, "250 OK\r\n")
			case line == "DATA":
				fmt.Fprint(conn, "354 Send message\r\n")
				message, err := reader.ReadDotBytes()
				if err != nil {
					failures <- err
					return
				}
				messages <- string(message)
				fmt.Fprint(conn, "250 OK\r\n")
			case line == "QUIT":
				fmt.Fprint(conn, "221 Bye\r\n")
				return
			default:
				failures <- fmt.Errorf("unexpected SMTP command %q", line)
				return
			}
		}
	}()
	store := testStore(t)
	recoveryUser(t, store, "owner", "github", "owner@example.com")
	mustTest(t, store.CreateSession("session", "owner", time.Now().Add(time.Hour)))
	_, port, err := net.SplitHostPort(listener.Addr().String())
	mustTest(t, err)
	s := NewServer(store, ServerConfig{SMTPHost: "127.0.0.1", SMTPPort: port, SMTPFrom: "accounts@example.com", BaseURL: "https://login.example.com"})
	r := httptest.NewRequest(http.MethodPost, "/api/app/signin/email", strings.NewReader("email=Backup%40Example.com"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: sessionCookieNameForChannel(), Value: "session"})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("send: %d %s", w.Code, w.Body)
	}
	methods, err := store.ListSignInMethods("owner")
	if err != nil || len(methods) != 1 {
		t.Fatalf("linked before verification: %#v %v", methods, err)
	}
	var message string
	select {
	case message = <-messages:
	case err := <-failures:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("mail fixture timed out")
	}
	if !strings.Contains(message, "Add a wingthing sign-in method") || !strings.Contains(message, "Only continue if you requested this") {
		t.Fatalf("unclear link intent: %s", message)
	}
	link := regexp.MustCompile(`https://login.example.com/auth/magic/verify\?token=link_[a-f0-9]+`).FindString(message)
	if link == "" {
		t.Fatalf("missing verification link: %s", message)
	}
	w = httptest.NewRecorder()
	s.ServeHTTP(w, signInRequest(http.MethodGet, link, "session"))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("verification: %d %s", w.Code, w.Body)
	}
	linked, err := store.GetUserByProvider("email", "backup@example.com")
	if err != nil || linked == nil || linked.ID != "owner" {
		t.Fatalf("verified method: %#v %v", linked, err)
	}
}

func TestSignInMagicRecoveryAddressProof(t *testing.T) {
	for _, tc := range []string{"verified", "mismatched", "unverified"} {
		t.Run(tc, func(t *testing.T) {
			store := testStore(t)
			recoveryUser(t, store, "github-owner", "github", "Owner@Example.com")
			s := NewServer(store, ServerConfig{})
			email := "owner@example.com"
			if tc == "mismatched" {
				email = "owner+alias@example.com"
			}
			if tc != "unverified" {
				mustTest(t, store.CreateMagicLink("fixture", email, "fixture-token", time.Now().Add(time.Minute)))
			}
			w := httptest.NewRecorder()
			s.handleMagicVerify(w, signInRequest(http.MethodGet, "/auth/magic/verify?token=fixture-token", ""))
			user := recoverySession(t, s, w)
			if tc == "verified" {
				if w.Code != http.StatusSeeOther || user == nil || user.ID != "github-owner" {
					t.Fatalf("recovery: %d %#v", w.Code, user)
				}
			} else if user != nil && user.ID == "github-owner" {
				t.Fatalf("unsafe recovery: %#v", user)
			}
			if tc == "mismatched" && (w.Code != http.StatusSeeOther || user == nil || user.ID == "github-owner") {
				t.Fatalf("mismatched address must remain a separate signup: %d %#v", w.Code, user)
			}
			if tc == "unverified" && (w.Code != http.StatusBadRequest || user != nil) {
				t.Fatalf("unverified login: %d %#v", w.Code, user)
			}
		})
	}
}

func TestSignInMethodsRequireSessionAndSameOrigin(t *testing.T) {
	store := testStore(t)
	recoveryUser(t, store, "owner", "github", "owner@example.com")
	mustTest(t, store.CreateSession("session", "owner", time.Now().Add(time.Hour)))
	s := NewServer(store, ServerConfig{BaseURL: "https://app.example.com", GoogleClientID: "fixture"})
	for _, path := range []string{"/api/app/signin/email", "/api/app/signin/google", "/api/app/signin/github"} {
		method := http.MethodPost
		if strings.HasSuffix(path, "github") {
			method = http.MethodDelete
		}
		for _, session := range []string{"", "session"} {
			r := signInRequest(method, path, session)
			if session != "" {
				r.Header.Set("Origin", "https://attacker.example")
				r.Header.Set("Sec-Fetch-Site", "cross-site")
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			want := http.StatusUnauthorized
			if session != "" {
				want = http.StatusForbidden
			}
			if w.Code != want {
				t.Fatalf("%s session=%q: %d %s", path, session, w.Code, w.Body)
			}
		}
	}
}

func TestSignInLoginOffersEmailBesideProviders(t *testing.T) {
	s := NewServer(testStore(t), ServerConfig{GitHubClientID: "fixture", GoogleClientID: "fixture", SMTPHost: "fixture"})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, signInRequest(http.MethodGet, "/login", ""))
	for _, text := range []string{"/auth/github", "/auth/google", "/auth/magic", "Email link", "Email address"} {
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), text) {
			t.Fatalf("login missing %q: %d", text, w.Code)
		}
	}
	// Ensure errors are structured JSON, not a generic HTML page.
	w = httptest.NewRecorder()
	signInError(w, ErrLastSignInMethod)
	if !json.Valid(w.Body.Bytes()) {
		t.Fatal("refusal is not JSON")
	}
}
