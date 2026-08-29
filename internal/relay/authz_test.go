package relay

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOAuthProviderResponsesRequireSuccessAndAreBounded(t *testing.T) {
	response := func(status int, body string) *http.Response {
		return &http.Response{
			StatusCode: status,
			Status:     http.StatusText(status),
			Body:       io.NopCloser(strings.NewReader(body)),
		}
	}
	var got struct {
		AccessToken string `json:"access_token"`
	}
	if err := decodeOAuthProviderJSON(response(http.StatusOK, `{"access_token":"token"}`), &got); err != nil || got.AccessToken != "token" {
		t.Fatalf("valid provider response = %#v, %v", got, err)
	}
	if err := decodeOAuthProviderJSON(response(http.StatusBadGateway, `{"access_token":"attacker-controlled"}`), &got); err == nil {
		t.Fatal("non-success provider response was accepted")
	}
	oversized := `{"padding":"` + strings.Repeat("x", maxOAuthProviderResponseBytes) + `"}`
	if err := decodeOAuthProviderJSON(response(http.StatusOK, oversized), &got); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized provider response error = %v", err)
	}
}

func TestOAuthProviderClientRefusesRedirects(t *testing.T) {
	targetCalled := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	resp, err := newOAuthHTTPClient().Post(source.URL, "application/x-www-form-urlencoded", strings.NewReader("client_secret=secret"))
	if resp != nil {
		closeTestBody(t, resp.Body)
	}
	if err == nil || !strings.Contains(err.Error(), "redirect refused") {
		t.Fatalf("OAuth redirect error = %v", err)
	}
	if targetCalled {
		t.Fatal("OAuth client followed redirect and forwarded the token exchange")
	}
}

func TestDeviceClaimAuditDoesNotRetainBearerCodes(t *testing.T) {
	store := testStore(t)
	s := NewServer(store, ServerConfig{})
	mustTest(t, store.CreateUser("claim-user"))
	mustTest(t, store.CreateSession("claim-session", "claim-user", time.Now().Add(time.Hour)))
	mustTest(t, store.CreateDeviceCode(
		"sensitive-device-grant",
		"ABCDEF",
		"wing-device-123",
		time.Now().Add(time.Hour),
	))

	request := httptest.NewRequest(
		http.MethodPost,
		"/auth/claim",
		strings.NewReader("user_code=ABCDEF"),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "claim-session"})
	response := httptest.NewRecorder()
	s.handleAuthClaim(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("claim status = %d, body = %s", response.Code, response.Body.String())
	}

	var detail string
	if err := store.DB().QueryRow(
		"SELECT detail FROM audit_log WHERE event = 'device_claimed' ORDER BY id DESC LIMIT 1",
	).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if detail != "device=wing-device-123" {
		t.Fatalf("device claim audit detail = %q", detail)
	}
	if strings.Contains(detail, "sensitive-device-grant") || strings.Contains(detail, "ABCDEF") {
		t.Fatalf("device claim audit retained a bearer code: %q", detail)
	}
}

func TestOAuthStateCookieUsesMatchingSecureScopeWhenCleared(t *testing.T) {
	s := NewServer(testStore(t), ServerConfig{
		BaseURL: "https://login.wingthing.test",
		AppHost: "app.wingthing.test",
	})
	setRecorder := httptest.NewRecorder()
	state := s.setOAuthState(setRecorder)
	setCookies := setRecorder.Result().Cookies()
	if len(setCookies) != 1 {
		t.Fatalf("set state cookies = %#v", setCookies)
	}
	set := setCookies[0]
	if set.Name != "oauth_state" || set.Value != state || set.Path != "/auth" || set.Domain != "" || !set.HttpOnly || !set.Secure || set.SameSite != http.SameSiteLaxMode {
		t.Fatalf("state cookie scope = %#v", set)
	}

	request := httptest.NewRequest(http.MethodGet, "/auth/github/callback?state="+state, nil)
	request.AddCookie(&http.Cookie{Name: "oauth_state", Value: state})
	clearRecorder := httptest.NewRecorder()
	if !s.validateOAuthState(clearRecorder, request) {
		t.Fatal("matching OAuth state was rejected")
	}
	clearCookies := clearRecorder.Result().Cookies()
	if len(clearCookies) != 1 {
		t.Fatalf("clear state cookies = %#v", clearCookies)
	}
	clear := clearCookies[0]
	if clear.Name != set.Name || clear.Path != set.Path || clear.Domain != set.Domain || !clear.HttpOnly || !clear.Secure || clear.SameSite != set.SameSite || clear.MaxAge >= 0 {
		t.Fatalf("cleared state cookie scope = %#v, set scope = %#v", clear, set)
	}
}

func TestSafeRedirectRejectsBrowserNormalizedExternalForms(t *testing.T) {
	for _, redirect := range []string{
		"//attacker.example/path",
		"/\\attacker.example/path",
		"/%5cattacker.example/path",
		"/%2fattacker.example/path",
		"https://attacker.example/path",
		"/safe\r\nLocation: https://attacker.example",
	} {
		if isSafeRedirect(redirect) {
			t.Errorf("isSafeRedirect(%q) = true", redirect)
		}
	}
	for _, redirect := range []string{
		"/",
		"/oauth/authorize?rid=opaque",
		"/invite/token#accept",
	} {
		if !isSafeRedirect(redirect) {
			t.Errorf("isSafeRedirect(%q) = false", redirect)
		}
	}
}

func TestSessionCookieRetainsConfiguredCrossSubdomainScope(t *testing.T) {
	s := NewServer(testStore(t), ServerConfig{
		BaseURL: "https://wingthing.test",
		AppHost: "app.wingthing.test",
	})
	recorder := httptest.NewRecorder()
	s.setSessionCookie(recorder, "session-token")
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("session cookies = %#v", cookies)
	}
	cookie := cookies[0]
	if cookie.Domain != "wingthing.test" || cookie.Path != "/" || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie scope = %#v", cookie)
	}
}

func TestSessionCookieUsesSharedRegistrableDomainForSplitHosts(t *testing.T) {
	s := NewServer(testStore(t), ServerConfig{
		BaseURL: "https://login.roost.example",
		AppHost: "app.roost.example",
	})
	recorder := httptest.NewRecorder()
	s.setSessionCookie(recorder, "session-token")
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("session cookies = %#v", cookies)
	}
	if got := cookies[0].Domain; got != "roost.example" {
		t.Fatalf("session cookie domain = %q, want %q", got, "roost.example")
	}
}

func TestSessionCookieUsesNarrowestSafeSharedDomain(t *testing.T) {
	s := NewServer(testStore(t), ServerConfig{
		BaseURL: "https://login.team.roost.example.com",
		AppHost: "app.team.roost.example.com",
	})
	recorder := httptest.NewRecorder()
	s.setSessionCookie(recorder, "session-token")
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("session cookies = %#v", cookies)
	}
	if got := cookies[0].Domain; got != "team.roost.example.com" {
		t.Fatalf("session cookie domain = %q, want %q", got, "team.roost.example.com")
	}
}

func TestSessionCookieStaysHostOnlyWhenAppAndLoginHostMatch(t *testing.T) {
	s := NewServer(testStore(t), ServerConfig{
		BaseURL: "https://roost.example.com",
		AppHost: "roost.example.com",
	})
	recorder := httptest.NewRecorder()
	s.setSessionCookie(recorder, "session-token")
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Domain != "" {
		t.Fatalf("same-host session cookie = %#v, want host-only", cookies)
	}
}

func TestBrowserCookieSecurityUsesExactParsedScheme(t *testing.T) {
	for _, tc := range []struct {
		name    string
		baseURL string
		secure  bool
	}{
		{name: "uppercase HTTPS", baseURL: "HTTPS://login.example.test", secure: true},
		{name: "plain HTTP", baseURL: "http://login.example.test", secure: false},
		{name: "custom HTTPS prefix", baseURL: "https-proxy://login.example.test", secure: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewServer(testStore(t), ServerConfig{BaseURL: tc.baseURL})
			recorder := httptest.NewRecorder()
			s.setSessionCookie(recorder, "session-token")
			cookies := recorder.Result().Cookies()
			if len(cookies) != 1 || cookies[0].Secure != tc.secure {
				t.Fatalf("cookies = %#v, want Secure=%v", cookies, tc.secure)
			}
		})
	}
}

func TestAppRedirectUsesParsedBaseURLScheme(t *testing.T) {
	for _, tc := range []struct {
		baseURL string
		want    string
	}{
		{baseURL: "HTTP://login.example.test", want: "http://app.example.test/"},
		{baseURL: "HTTPS://login.example.test", want: "https://app.example.test/"},
	} {
		t.Run(tc.baseURL, func(t *testing.T) {
			store := testStore(t)
			mustTest(t, store.CreateUser("redirect-user"))
			s := NewServer(store, ServerConfig{BaseURL: tc.baseURL, AppHost: "app.example.test"})
			response := httptest.NewRecorder()
			s.createSessionAndRedirect(
				response,
				httptest.NewRequest(http.MethodGet, "/", nil),
				&User{ID: "redirect-user"},
			)
			if response.Code != http.StatusSeeOther || response.Header().Get("Location") != tc.want {
				t.Fatalf("redirect status=%d location=%q, want %q", response.Code, response.Header().Get("Location"), tc.want)
			}
		})
	}
}

func TestAppURLUsesConfiguredSchemeAndSingleHostMount(t *testing.T) {
	for _, tc := range []struct {
		name    string
		config  ServerConfig
		wantURL string
	}{
		{
			name:    "HTTP split host",
			config:  ServerConfig{BaseURL: "http://login.example.test:8080", AppHost: "app.example.test:8080"},
			wantURL: "http://app.example.test:8080/",
		},
		{
			name:    "HTTPS split host",
			config:  ServerConfig{BaseURL: "https://login.example.test", AppHost: "app.example.test"},
			wantURL: "https://app.example.test/",
		},
		{
			name:    "single host",
			config:  ServerConfig{BaseURL: "https://roost.example.test"},
			wantURL: "/app/",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := NewServer(testStore(t), tc.config)
			if got := server.appURL(); got != tc.wantURL {
				t.Fatalf("appURL() = %q, want %q", got, tc.wantURL)
			}
		})
	}
}

func TestAppMeAdvertisesConfiguredLoginBaseForSplitHosts(t *testing.T) {
	s := NewServer(testStore(t), ServerConfig{
		BaseURL: "https://login.roost.example",
		AppHost: "app.roost.example",
	})
	response := httptest.NewRecorder()
	s.handleAppMe(response, httptest.NewRequest(http.MethodGet, "https://app.roost.example/api/app/me", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
	var body map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != "not logged in" || body["login_base_url"] != "https://login.roost.example" {
		t.Fatalf("unauthorized app metadata = %#v", body)
	}
}

func TestSessionCookieRefusesUnsafeCrossHostScope(t *testing.T) {
	for _, tc := range []struct {
		name    string
		baseURL string
		appHost string
	}{
		{name: "unrelated hosts", baseURL: "https://login.example", appHost: "app.example.net"},
		{name: "public suffix only", baseURL: "https://login.co.uk", appHost: "app.co.uk"},
		{name: "localhost", baseURL: "https://localhost:8443", appHost: "localhost:9443"},
		{name: "IP addresses", baseURL: "https://127.0.0.1:8443", appHost: "127.0.0.1:9443"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewServer(testStore(t), ServerConfig{BaseURL: tc.baseURL, AppHost: tc.appHost})
			recorder := httptest.NewRecorder()
			s.setSessionCookie(recorder, "session-token")
			cookies := recorder.Result().Cookies()
			if len(cookies) != 1 {
				t.Fatalf("session cookies = %#v", cookies)
			}
			if got := cookies[0].Domain; got != "" {
				t.Fatalf("session cookie domain = %q, want a host-only cookie", got)
			}
		})
	}
}

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

	mustTest(t, store.CreateUser(ownerID))
	mustTest(t, store.CreateUser(memberID))
	mustTest(t, store.CreateUser(outsiderID))

	// CreateOrg auto-adds ownerID as "owner" member (max_seats=1)
	mustTest(t, store.CreateOrg("org-1", "Test Org", "test-org", ownerID))
	mustTestExec(t, store.DB(), "UPDATE orgs SET max_seats = 10 WHERE id = 'org-1'")
	mustTest(t, store.AddOrgMember("org-1", memberID, "member"))

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

	mustTest(t, store.CreateUser("user-a"))
	mustTest(t, store.CreateUser("user-b"))

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

func TestRoostModeSharesOnlyEmbeddedServiceWing(t *testing.T) {
	store := testStore(t)
	s := NewServer(store, ServerConfig{})
	s.RoostMode = true

	shared := &ConnectedWing{UserID: roostWingServiceUserID, WingID: "shared-roost"}
	personal := &ConnectedWing{UserID: "owner", WingID: "personal"}

	if !s.canAccessWing("any-authenticated-user", shared) {
		t.Fatal("roost users should be able to access the embedded service wing")
	}
	if !s.canAccessWing("owner", personal) {
		t.Fatal("personal wing owner should retain access in roost mode")
	}
	if s.canAccessWing("other-user", personal) {
		t.Fatal("roost mode must not expose an external personal wing to every user")
	}
}

func TestOAuthRoostEnrollmentAllowlistAppliesToCookiesTokensAndWingInventory(t *testing.T) {
	store := testStore(t)
	for _, id := range []string{"allowed", "outsider", roostWingServiceUserID} {
		mustTest(t, store.CreateUser(id))
	}
	if _, err := store.DB().Exec("UPDATE users SET email = ? WHERE id = ?", "alice@example.com", "allowed"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec("UPDATE users SET email = ? WHERE id = ?", "mallory@example.com", "outsider"); err != nil {
		t.Fatal(err)
	}
	mustTest(t, store.CreateSession("allowed-session", "allowed", time.Now().Add(time.Hour)))
	mustTest(t, store.CreateSession("outsider-session", "outsider", time.Now().Add(time.Hour)))
	if err := store.CreateDeviceToken("outsider-token", "outsider", "outsider-device", nil); err != nil {
		t.Fatal(err)
	}

	s := NewServer(store, ServerConfig{RoostAllowedEmails: []string{"ALICE@example.com"}})
	s.RoostMode = true
	shared := &ConnectedWing{UserID: roostWingServiceUserID, WingID: "shared-roost"}
	s.Wings.Add(shared)

	requestWithCookie := func(token string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/api/app/me", nil)
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
		return req
	}
	if user := s.sessionUser(requestWithCookie("allowed-session")); user == nil || user.ID != "allowed" {
		t.Fatalf("allowlisted session user = %#v", user)
	}
	if user := s.sessionUser(requestWithCookie("outsider-session")); user != nil {
		t.Fatalf("non-enrolled session was accepted: %#v", user)
	}
	tokenReq := httptest.NewRequest(http.MethodGet, "/api/auth/check", nil)
	tokenReq.Header.Set("Authorization", "Bearer outsider-token")
	tokenResp := httptest.NewRecorder()
	if userID := s.requireToken(tokenResp, tokenReq); userID != "" || tokenResp.Code != http.StatusForbidden {
		t.Fatalf("non-enrolled token user=%q status=%d body=%s", userID, tokenResp.Code, tokenResp.Body.String())
	}
	loginResp := httptest.NewRecorder()
	s.createSessionAndRedirect(loginResp, httptest.NewRequest(http.MethodGet, "/", nil), &User{ID: "outsider", Email: strPtr("mallory@example.com")})
	if loginResp.Code != http.StatusForbidden {
		t.Fatalf("non-enrolled login status=%d body=%s", loginResp.Code, loginResp.Body.String())
	}
	refreshResp := httptest.NewRecorder()
	s.handleAuthRefresh(refreshResp, httptest.NewRequest(http.MethodPost, "/auth/refresh", strings.NewReader(`{"token":"outsider-token"}`)))
	if refreshResp.Code != http.StatusForbidden {
		t.Fatalf("non-enrolled refresh status=%d body=%s", refreshResp.Code, refreshResp.Body.String())
	}
	if userID, _, err := store.ValidateToken("outsider-token"); err != nil || userID != "outsider" {
		t.Fatalf("denied refresh destroyed existing token: user=%q err=%v", userID, err)
	}
	internalReq := httptest.NewRequest(http.MethodGet, "/internal/session/outsider-session", nil)
	internalReq.SetPathValue("token", "outsider-session")
	internalResp := httptest.NewRecorder()
	s.handleInternalSession(internalResp, internalReq)
	if internalResp.Code != http.StatusForbidden {
		t.Fatalf("edge session validation status=%d body=%s", internalResp.Code, internalResp.Body.String())
	}
	resolveReq := requestWithCookie("allowed-session")
	resolveReq.URL.Path = "/api/app/resolve-email"
	resolveReq.URL.RawQuery = "email=mallory%40example.com"
	resolveResp := httptest.NewRecorder()
	s.handleResolveEmail(resolveResp, resolveReq)
	if resolveResp.Code != http.StatusNotFound {
		t.Fatalf("non-enrolled account was resolvable: status=%d body=%s", resolveResp.Code, resolveResp.Body.String())
	}
	for name, handler := range map[string]http.HandlerFunc{"wing": s.handleWingWS, "pty": s.handlePTYWS} {
		req := httptest.NewRequest(http.MethodGet, "/ws/"+name+"?token=outsider-token", nil)
		resp := httptest.NewRecorder()
		handler(resp, req)
		if resp.Code != http.StatusForbidden {
			t.Errorf("non-enrolled %s websocket status=%d body=%s", name, resp.Code, resp.Body.String())
		}
	}
	if err := store.CreateDeviceCode("outsider-code", "OUT123", "outsider-device-2", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.ClaimDeviceCode("outsider-code", "outsider"); err != nil {
		t.Fatal(err)
	}
	pollResp := httptest.NewRecorder()
	s.handleAuthToken(pollResp, httptest.NewRequest(http.MethodPost, "/auth/token", strings.NewReader(`{"device_code":"outsider-code"}`)))
	if pollResp.Code != http.StatusForbidden {
		t.Fatalf("non-enrolled device claim polling status=%d body=%s", pollResp.Code, pollResp.Body.String())
	}
	if !s.canAccessWing("allowed", shared) || s.canAccessWing("outsider", shared) {
		t.Fatal("roost wing inventory ignored enrollment allowlist")
	}
	if access := s.relayAccess("outsider"); access.Allowed || access.Reason != "roost-enrollment-required" {
		t.Fatalf("outsider relay access = %#v", access)
	}
	if !s.roostUserIDAllowed(roostWingServiceUserID) {
		t.Fatal("embedded service identity was blocked by human enrollment policy")
	}
}

func TestOAuthRoostEmptyEnrollmentListPreservesExistingBehavior(t *testing.T) {
	store := testStore(t)
	mustTest(t, store.CreateUser("existing"))
	s := NewServer(store, ServerConfig{})
	s.RoostMode = true
	if !s.roostUserIDAllowed("existing") {
		t.Fatal("empty allowlist changed historical OAuth roost enrollment")
	}
}

func TestStandaloneOAuthGatewayEnforcesConfiguredEnrollment(t *testing.T) {
	store := testStore(t)
	for _, id := range []string{"allowed", "outsider"} {
		mustTest(t, store.CreateUser(id))
	}
	if _, err := store.DB().Exec("UPDATE users SET email = ? WHERE id = ?", "alice@example.com", "allowed"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec("UPDATE users SET email = ? WHERE id = ?", "mallory@example.com", "outsider"); err != nil {
		t.Fatal(err)
	}
	s := NewServer(store, ServerConfig{RoostAllowedEmails: []string{"alice@example.com"}})
	if s.RoostMode {
		t.Fatal("test must exercise standalone gateway mode")
	}
	if !s.roostUserIDAllowed("allowed") || s.roostUserIDAllowed("outsider") {
		t.Fatal("standalone gateway ignored configured enrollment")
	}
	if access := s.relayAccess("outsider"); access.Allowed || access.Reason != "roost-enrollment-required" {
		t.Fatalf("standalone outsider relay access = %#v", access)
	}
}

func TestOAuthEnrollmentOnEdgeUsesLoginNodeDecision(t *testing.T) {
	cache := NewEntitlementCache("http://login.internal")
	cache.relay["allowed"] = RelayAccess{Allowed: false, Reason: "direct-only-free"}
	cache.relay["pro"] = RelayAccess{Allowed: true, Reason: "pro"}
	cache.relay["outsider"] = RelayAccess{Allowed: false, Reason: "roost-enrollment-required"}
	cache.enrollment["allowed"] = true
	cache.enrollment["pro"] = true
	cache.enrollment["outsider"] = false
	cache.initialized = true
	cache.policyDecisionsKnown = true
	s := NewServer(testStore(t), ServerConfig{
		NodeRole:           "edge",
		RoostAllowedEmails: []string{"alice@example.com"},
	})
	s.EntitlementCache = cache
	s.sessionCache = NewSessionCache()
	s.sessionCache.entries["allowed-session"] = &sessionCacheEntry{
		user:      &User{ID: "allowed", DisplayName: "Alice"},
		fetchedAt: time.Now(),
	}
	s.sessionCache.entries["outsider-session"] = &sessionCacheEntry{
		user:      &User{ID: "outsider", DisplayName: "Mallory"},
		fetchedAt: time.Now(),
	}

	if !s.roostUserIDAllowed("allowed") {
		t.Fatal("enrolled direct-only user was denied at edge")
	}
	if !s.roostUserIDAllowed("pro") {
		t.Fatal("enrolled relay user was denied at edge")
	}
	if s.roostUserIDAllowed("outsider") {
		t.Fatal("non-enrolled user was accepted at edge")
	}
	if s.roostUserIDAllowed("not-synchronized") {
		t.Fatal("missing login-node decision did not fail closed at edge")
	}
	requestWithCookie := func(token string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/api/app/me", nil)
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
		return req
	}
	if user := s.sessionUser(requestWithCookie("allowed-session")); user == nil || user.ID != "allowed" {
		t.Fatalf("enrolled edge session = %#v", user)
	}
	if user := s.sessionUser(requestWithCookie("outsider-session")); user != nil {
		t.Fatalf("revoked edge session was accepted: %#v", user)
	}

	s.EntitlementCache = nil
	if s.roostUserIDAllowed("allowed") {
		t.Fatal("edge without enrollment cache did not fail closed")
	}
}

func TestEdgeOrgRoleResolutionPreservesMembersWithoutInventingAdmin(t *testing.T) {
	edge := NewServer(testStore(t), ServerConfig{NodeRole: "edge"})
	wing := &ConnectedWing{UserID: "wing-owner", WingID: "org-wing", OrgID: "org-1"}

	if got := roleForWingUser(edge, wing, "wing-owner", nil, nil); got != "owner" {
		t.Fatalf("wing owner role = %q", got)
	}
	if got := roleForWingUser(edge, wing, "admin", []string{"org-1"}, map[string]string{"org-1": "admin"}); got != "admin" {
		t.Fatalf("current edge admin role = %q", got)
	}
	if got := roleForWingUser(edge, wing, "member", []string{"org-1"}, nil); got != "member" {
		t.Fatalf("N-1 membership fallback role = %q", got)
	}
	if got := roleForWingUser(edge, wing, "outsider", nil, map[string]string{"org-1": "future-superuser"}); got != "" {
		t.Fatalf("unknown/unauthorized role = %q", got)
	}
}

func TestRemoteUserOrgContextCarriesBearerIdentityToEdge(t *testing.T) {
	login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/user-orgs/member" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"email": "member@example.com", "org_ids": []string{"org-1"},
			"org_roles": map[string]string{"org-1": "member"},
		})
	}))
	defer login.Close()
	edge := NewServer(testStore(t), ServerConfig{NodeRole: "edge", LoginNodeAddr: login.URL})
	identity, ok := edge.remoteUserOrgContext(context.Background(), "member")
	if !ok || identity.Email != "member@example.com" || len(identity.OrgIDs) != 1 || identity.OrgRoles["org-1"] != "member" {
		t.Fatalf("remote edge identity = %#v ok=%v", identity, ok)
	}
}

func TestOAuthRoostProviderEmailMustBeCurrentAndVerified(t *testing.T) {
	s := NewServer(testStore(t), ServerConfig{RoostAllowedEmails: []string{"alice@example.com"}})
	s.RoostMode = true
	for _, tc := range []struct {
		name     string
		email    string
		verified bool
		want     bool
	}{
		{name: "exact verified address", email: "alice@example.com", verified: true, want: true},
		{name: "case normalized verified address", email: "ALICE@EXAMPLE.COM", verified: true, want: true},
		{name: "unverified listed address", email: "alice@example.com", verified: false, want: false},
		{name: "missing provider address", verified: false, want: false},
		{name: "different verified address", email: "mallory@example.com", verified: true, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.roostProviderEmailAllowed(tc.email, tc.verified); got != tc.want {
				t.Fatalf("roostProviderEmailAllowed(%q, %v) = %v, want %v", tc.email, tc.verified, got, tc.want)
			}
		})
	}

	compat := NewServer(testStore(t), ServerConfig{})
	compat.RoostMode = true
	if !compat.roostProviderEmailAllowed("", false) {
		t.Fatal("empty enrollment list stopped preserving authenticated-provider compatibility")
	}
}

func TestDeniedOAuthRefreshRevokesStoredEnrollmentAndExistingSessions(t *testing.T) {
	store := testStore(t)
	mustTest(t, store.CreateUser("existing"))
	mustTest(t, store.UpdateUserEmail("existing", "alice@example.com"))
	mustTest(t, store.CreateSession("old-session", "existing", time.Now().Add(time.Hour)))
	server := NewServer(store, ServerConfig{RoostAllowedEmails: []string{"alice@example.com"}})
	request := httptest.NewRequest(http.MethodGet, "/api/app/me", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "old-session"})
	if user := server.sessionUser(request); user == nil {
		t.Fatal("precondition: enrolled session was denied")
	}

	user, err := store.GetUserByID("existing")
	if err != nil {
		t.Fatal(err)
	}
	if err := server.clearDeniedOAuthEnrollment(user); err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetUserByID("existing")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Email != nil {
		t.Fatalf("denied provider identity retained stale enrolled email %q", *stored.Email)
	}
	if user := server.sessionUser(request); user != nil {
		t.Fatalf("old session remained authorized after provider enrollment denial: %#v", user)
	}
}

func TestLogoutExpiresTheSameCrossSubdomainCookieThatLoginSets(t *testing.T) {
	store := testStore(t)
	mustTest(t, store.CreateUser("user-1"))
	mustTest(t, store.CreateSession("session-1", "user-1", time.Now().Add(time.Hour)))
	server := NewServer(store, ServerConfig{
		BaseURL: "https://wingthing.ai",
		AppHost: "app.wingthing.ai",
	})
	request := httptest.NewRequest(http.MethodPost, "https://wingthing.ai/logout", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "session-1"})
	recorder := httptest.NewRecorder()

	server.handleLogout(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("logout status = %d", recorder.Code)
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("logout cookies = %#v", cookies)
	}
	got := cookies[0]
	if got.Name != sessionCookieName || got.Domain != "wingthing.ai" || got.Path != "/" || got.MaxAge >= 0 || !got.Secure || !got.HttpOnly {
		t.Fatalf("logout cookie does not match login scope: %#v", got)
	}
	if user, err := store.GetSession("session-1"); err != nil || user != nil {
		t.Fatalf("deleted session lookup = %#v, %v", user, err)
	}
}

func TestWingRegistrationMustMatchCredential(t *testing.T) {
	server := &Server{}
	if !server.wingRegistrationAllowed("owner", "wing-a", "wing-a") {
		t.Fatal("matching credential and registration IDs were rejected")
	}
	if server.wingRegistrationAllowed("owner", "wing-a", "wing-b") {
		t.Fatal("credential registered as another wing")
	}
	if server.wingRegistrationAllowed("owner", "wing-a", "") {
		t.Fatal("empty wing registration was accepted")
	}
	if server.wingRegistrationAllowed("owner", "wing-a", strings.Repeat("x", 201)) {
		t.Fatal("oversized wing registration was accepted")
	}

	server.LocalMode = true
	if !server.wingRegistrationAllowed("local", "local", "configured-wing") {
		t.Fatal("embedded local wing token was rejected")
	}
	server.LocalMode = false
	server.RoostMode = true
	if !server.wingRegistrationAllowed(roostWingServiceUserID, "roost-wing", "configured-wing") {
		t.Fatal("embedded roost service wing token was rejected")
	}
	if server.wingRegistrationAllowed("member", "member-wing", "victim-wing") {
		t.Fatal("ordinary roost user registered a mismatched wing")
	}
}

// --- Wing event notification tests ---

func TestAuthzOrgWingNotifications(t *testing.T) {
	store := testStore(t)
	s := NewServer(store, ServerConfig{})

	mustTest(t, store.CreateUser("owner-1"))
	mustTest(t, store.CreateUser("member-1"))
	mustTest(t, store.CreateUser("outsider-1"))

	mustTest(t, store.CreateOrg("org-1", "Test Org", "test-org", "owner-1"))
	mustTestExec(t, store.DB(), "UPDATE orgs SET max_seats = 10 WHERE id = 'org-1'")
	mustTest(t, store.AddOrgMember("org-1", "member-1", "member"))

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
	s.dispatchWingEvent("wing.online", wing)

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
	if w := s.Wings.Remove(wing.ID); w != nil {
		s.dispatchWingEvent("wing.offline", w)
	}

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

	mustTest(t, store.CreateUser("user-a"))
	mustTest(t, store.CreateUser("user-b"))

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
	s.dispatchWingEvent("wing.online", wing)

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

func TestRelayResponsesSetBrowserSecurityHeadersBeforeRouting(t *testing.T) {
	server := NewServer(nil, ServerConfig{})
	server.LocalMode = true
	for _, host := range []string{"localhost", "attacker.example"} {
		t.Run(host, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://"+host+"/health", nil)
			request.Host = host
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, request)
			for header, want := range map[string]string{
				"Content-Security-Policy": "frame-ancestors 'none'",
				"X-Frame-Options":         "DENY",
				"X-Content-Type-Options":  "nosniff",
				"Referrer-Policy":         "no-referrer",
			} {
				if got := recorder.Header().Get(header); got != want {
					t.Errorf("%s = %q, want %q", header, got, want)
				}
			}
		})
	}
}

func TestPersonalizedSitePagesNeverEnterSharedCache(t *testing.T) {
	server := NewServer(nil, ServerConfig{})
	for _, test := range []struct {
		name        string
		path        string
		withSession bool
		wantCache   string
	}{
		{name: "anonymous docs", path: "/docs", wantCache: "public, max-age=900, s-maxage=900"},
		{name: "anonymous query", path: "/patterns?source=test", wantCache: "public, max-age=60, s-maxage=60"},
		{name: "session docs", path: "/docs", withSession: true, wantCache: "private, no-store"},
		{name: "login", path: "/login?next=%2Fapp%2F", wantCache: "private, no-store"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "https://wingthing.example"+test.path, nil)
			if test.withSession {
				request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "possibly-valid"})
			}
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, request)
			if got := recorder.Header().Get("Cache-Control"); got != test.wantCache {
				t.Errorf("Cache-Control = %q, want %q", got, test.wantCache)
			}
			if vary := recorder.Header().Values("Vary"); len(vary) == 0 || !strings.Contains(strings.Join(vary, ","), "Cookie") {
				t.Errorf("Vary = %#v, want Cookie", vary)
			}
		})
	}
}

func TestPrivateControlResponsesNeverEnterCaches(t *testing.T) {
	server := NewServer(nil, ServerConfig{})
	for _, path := range []string{
		"/api/app/me",
		"/auth/check",
		"/oauth/token",
		"/internal/entitlements",
		"/invite/secret-token",
		"/mcp",
	} {
		t.Run(path, func(t *testing.T) {
			// OPTIONS deliberately misses every registered route. The cache boundary
			// belongs to ServeHTTP and must cover early errors as well as handlers.
			request := httptest.NewRequest(http.MethodOptions, "https://wingthing.example"+path, nil)
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, request)

			if got := recorder.Header().Get("Cache-Control"); got != "private, no-store" {
				t.Errorf("Cache-Control = %q, want private, no-store", got)
			}
			if got := recorder.Header().Get("Pragma"); got != "no-cache" {
				t.Errorf("Pragma = %q, want no-cache", got)
			}
			vary := strings.ToLower(strings.Join(recorder.Header().Values("Vary"), ","))
			for _, want := range []string{"cookie", "authorization"} {
				if !strings.Contains(vary, want) {
					t.Errorf("Vary = %q, want %s", vary, want)
				}
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
