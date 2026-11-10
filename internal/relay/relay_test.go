package relay

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func mustTest(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func mustTestExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func closeTestBody(t *testing.T, closer io.Closer) {
	t.Helper()
	if err := closer.Close(); err != nil {
		t.Errorf("close response body: %v", err)
	}
}

func decodeTestJSON(t *testing.T, reader io.Reader, value any) {
	t.Helper()
	if err := json.NewDecoder(reader).Decode(value); err != nil {
		t.Fatalf("decode JSON response: %v", err)
	}
}

func testStore(t *testing.T) *RelayStore {
	t.Helper()
	s, err := OpenRelay(":memory:")
	if err != nil {
		t.Fatalf("open relay store: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close relay store: %v", err)
		}
	})
	return s
}

func testServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	store := testStore(t)
	key, _, err := GenerateECKey()
	if err != nil {
		t.Fatalf("generate test jwt key: %v", err)
	}
	srv := NewServer(store, ServerConfig{})
	srv.SetJWTKey(key)
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { ts.Close() })
	return srv, ts
}

// createTestToken creates a user and token in the store, returning the token string.
func createTestToken(t *testing.T, store *RelayStore, deviceID string) (token, userID string) {
	t.Helper()
	userID = "user-" + deviceID
	if err := store.CreateUser(userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	token = "tok-" + deviceID
	if err := store.CreateDeviceToken(token, userID, deviceID, nil); err != nil {
		t.Fatalf("create token: %v", err)
	}
	return token, userID
}

func TestRelayStoreUserAndToken(t *testing.T) {
	s := testStore(t)

	if err := s.CreateUser("u1"); err != nil {
		t.Fatalf("create user: %v", err)
	}

	if err := s.CreateDeviceToken("tok1", "u1", "dev1", nil); err != nil {
		t.Fatalf("create token: %v", err)
	}

	uid, did, err := s.ValidateToken("tok1")
	if err != nil {
		t.Fatalf("validate token: %v", err)
	}
	if uid != "u1" || did != "dev1" {
		t.Errorf("validate token: got uid=%q did=%q, want u1/dev1", uid, did)
	}

	// Invalid token
	_, _, err = s.ValidateToken("bogus")
	if err == nil {
		t.Error("expected error for invalid token")
	}

	// Delete token
	if err := s.DeleteToken("tok1"); err != nil {
		t.Fatalf("delete token: %v", err)
	}
	_, _, err = s.ValidateToken("tok1")
	if err == nil {
		t.Error("expected error after delete")
	}
}

func TestRelayStoreDeviceCodeFlow(t *testing.T) {
	s := testStore(t)

	expires := time.Now().Add(15 * time.Minute)
	if err := s.CreateDeviceCode("dc1", "ABCD12", "dev1", expires); err != nil {
		t.Fatalf("create device code: %v", err)
	}

	dc, err := s.GetDeviceCode("dc1")
	if err != nil {
		t.Fatalf("get device code: %v", err)
	}
	if dc == nil {
		t.Fatal("expected device code, got nil")
	}
	if dc.Claimed {
		t.Error("expected unclaimed")
	}

	// Claim requires a user
	if err := s.CreateUser("u1"); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := s.ClaimDeviceCode("dc1", "u1"); err != nil {
		t.Fatalf("claim device code: %v", err)
	}

	dc, err = s.GetDeviceCode("dc1")
	if err != nil {
		t.Fatalf("get device code after claim: %v", err)
	}
	if !dc.Claimed {
		t.Error("expected claimed")
	}
	if dc.UserID == nil || *dc.UserID != "u1" {
		t.Errorf("expected user_id=u1, got %v", dc.UserID)
	}

	// Double claim should fail
	if err := s.ClaimDeviceCode("dc1", "u1"); err == nil {
		t.Error("expected error on double claim")
	}
}

func TestHealthEndpoint(t *testing.T) {
	_, ts := testServer(t)

	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	var body map[string]bool
	decodeTestJSON(t, resp.Body, &body)
	if !body["ok"] {
		t.Error("expected ok=true")
	}
}

func TestEdgeProxiesHTMLAndHashedAssetsToOneRelease(t *testing.T) {
	srv := NewServer(nil, ServerConfig{NodeRole: "edge"})
	proxied := make(map[string]int)
	srv.SetLoginProxy(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied[r.URL.Path]++
		w.WriteHeader(http.StatusNoContent)
	}))

	for _, path := range []string{"/", "/docs", "/app/", "/assets/current-hash.js", "/api/app/me"} {
		response := httptest.NewRecorder()
		srv.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNoContent || proxied[path] != 1 {
			t.Errorf("edge request %s: status=%d proxied=%d", path, response.Code, proxied[path])
		}
	}

	for _, path := range []string{"/health", "/internal/status", "/ws/pty"} {
		response := httptest.NewRecorder()
		srv.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if proxied[path] != 0 {
			t.Errorf("connection-local edge request %s was proxied", path)
		}
	}
}

func TestAuthDeviceFlow(t *testing.T) {
	srv, ts := testServer(t)
	srv.DevMode = true // auto-claim in dev mode

	// 1. Request device code (dev mode auto-claims)
	resp, err := http.Post(ts.URL+"/auth/device", "application/json",
		strings.NewReader(`{"wing_id":"mac1","public_key":"dGVzdGtleQ=="}`))
	if err != nil {
		t.Fatalf("POST /auth/device: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("device code status = %d", resp.StatusCode)
	}

	var dcResp struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
		VerificationURL string `json:"verification_url"`
	}
	decodeTestJSON(t, resp.Body, &dcResp)
	if dcResp.DeviceCode == "" || dcResp.UserCode == "" {
		t.Fatal("expected device_code and user_code")
	}
	if len(dcResp.UserCode) != 6 {
		t.Errorf("user_code length = %d, want 6", len(dcResp.UserCode))
	}

	// 2. Poll — dev mode auto-claims, so should get JWT immediately
	resp2, err := http.Post(ts.URL+"/auth/token", "application/json",
		strings.NewReader(`{"device_code":"`+dcResp.DeviceCode+`"}`))
	if err != nil {
		t.Fatalf("POST /auth/token: %v", err)
	}
	var tokenResp map[string]any
	decodeTestJSON(t, resp2.Body, &tokenResp)
	closeTestBody(t, resp2.Body)
	if tokenResp["token"] == nil || tokenResp["token"] == "" {
		t.Errorf("expected JWT token in response, got %v", tokenResp)
	}
	// Verify it's a JWT (has dots)
	if tok, ok := tokenResp["token"].(string); ok {
		if !strings.Contains(tok, ".") {
			t.Error("expected JWT format with dots")
		}
	}
	if tokenResp["expires_at"] == nil {
		t.Error("expected expires_at in response")
	}
	if token, ok := tokenResp["token"].(string); ok {
		var storedExpiry sql.NullTime
		if err := srv.Store.DB().QueryRow("SELECT expires_at FROM device_tokens WHERE token = ?", token).Scan(&storedExpiry); err != nil {
			t.Fatal(err)
		}
		if !storedExpiry.Valid {
			t.Fatal("issued JWT was stored as a non-expiring compatibility token")
		}
	}

	// The approved device code is a one-time grant. Replaying it must not mint
	// another token.
	replay, err := http.Post(ts.URL+"/auth/token", "application/json",
		strings.NewReader(`{"device_code":"`+dcResp.DeviceCode+`"}`))
	if err != nil {
		t.Fatalf("replay POST /auth/token: %v", err)
	}
	defer closeTestBody(t, replay.Body)
	var replayResp map[string]string
	decodeTestJSON(t, replay.Body, &replayResp)
	if replayResp["error"] != "invalid_code" {
		t.Fatalf("replayed device code response = %#v, want invalid_code", replayResp)
	}
}

func TestAuthDeviceFlowNonDevMode(t *testing.T) {
	_, ts := testServer(t)

	// Without dev mode, poll should return authorization_pending
	resp, err := http.Post(ts.URL+"/auth/device", "application/json",
		strings.NewReader(`{"wing_id":"mac1"}`))
	if err != nil {
		t.Fatalf("POST /auth/device: %v", err)
	}
	var dcResp struct {
		DeviceCode string `json:"device_code"`
	}
	decodeTestJSON(t, resp.Body, &dcResp)
	closeTestBody(t, resp.Body)

	resp2, err := http.Post(ts.URL+"/auth/token", "application/json",
		strings.NewReader(`{"device_code":"`+dcResp.DeviceCode+`"}`))
	if err != nil {
		t.Fatalf("POST /auth/token: %v", err)
	}
	var pendingResp map[string]string
	decodeTestJSON(t, resp2.Body, &pendingResp)
	closeTestBody(t, resp2.Body)
	if pendingResp["error"] != "authorization_pending" {
		t.Errorf("expected authorization_pending, got %q", pendingResp["error"])
	}
}

func TestStaticFileServing(t *testing.T) {
	_, ts := testServer(t)

	resp, err := http.Get(ts.URL + "/app/")
	if err != nil {
		t.Fatalf("GET /app/: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Errorf("content-type = %q, want text/html", ct)
	}
}

func TestPatternsPageExplainsOnlySupportedSetups(t *testing.T) {
	_, ts := testServer(t)

	resp, err := http.Get(ts.URL + "/patterns")
	if err != nil {
		t.Fatalf("GET /patterns: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body := new(strings.Builder)
	if _, err := io.Copy(body, resp.Body); err != nil {
		t.Fatalf("read /patterns: %v", err)
	}
	page := body.String()
	for _, want := range []string{
		"Let your current AI launch local sub-agents",
		"Run an agent and return to it later",
		"Let one AI manage agents on several computers",
		"Control a remote agent from a localhost browser",
		"Give a team a private browser-based agent host",
		"Let an AI control agents on your private roost",
		"Use the entitled hosted browser on a remote wing",
		"an exact email enrollment list",
		"an enrolled account on a roost",
		"Each execution wing needs:",
		"authorization for the connector account (personal or organization)",
		"The parent/connector needs:",
		"It runs <code>wt start</code> only when it also executes agents.",
		"You need:",
		"You get:",
		"hosted browser -> encrypted relay -> selected wing -> agent",
		"localhost browser -> local portal -> SSH tunnel -> remote wing -> agent",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("/patterns does not contain %q", want)
		}
	}
	if got := strings.Count(page, `<section class="pattern"`); got != 7 {
		t.Errorf("/patterns contains %d setup cards, want 7", got)
	}
	previous := -1
	for _, route := range []string{
		`data-route="local-human"`,
		`data-route="local-agent"`,
		`data-route="direct-remote"`,
		`data-route="self-hosted-browser"`,
		`data-route="self-hosted-team"`,
		`data-route="self-hosted-agent"`,
		`data-route="hosted-relay"`,
	} {
		index := strings.Index(page, route)
		if index < 0 {
			t.Errorf("/patterns is missing route marker %q", route)
			continue
		}
		if index <= previous {
			t.Errorf("/patterns route marker %q is out of local-first order", route)
		}
		previous = index
	}
	for _, internal := range []string{
		"the workflows people are asking for",
		"scheduled log review",
		"client-side",
		"grandfathered",
		"compose independent roosts",
		"peer directory federation",
	} {
		if strings.Contains(page, internal) {
			t.Errorf("/patterns exposes internal product narration %q", internal)
		}
	}
}

func TestPersonalRemoteWingGuideIsSelfHostedFirst(t *testing.T) {
	_, ts := testServer(t)
	resp, err := http.Get(ts.URL + "/patterns/personal-remote-wing/INSTRUCTIONS.md")
	if err != nil {
		t.Fatalf("GET personal remote wing guide: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	body := new(strings.Builder)
	if _, err := io.Copy(body, resp.Body); err != nil {
		t.Fatalf("read personal remote wing guide: %v", err)
	}
	guide := body.String()
	for _, want := range []string{
		"This is the first browser route to consider and the smallest self-hosted setup.",
		"does not use wingthing.ai or require a hosted-relay entitlement",
		"wt serve --local --https",
		"https://localhost:8443/app/",
		"both private keys remain mode `0600` on this browser computer",
		"installs only the public CA certificate",
		"-R 127.0.0.1:18743:127.0.0.1:8080",
		"claude auth status",
		"paste travels through the encrypted",
		"the roost cannot read it",
		"wt login --roost http://127.0.0.1:18743",
		"Terminal payloads are additionally encrypted between the browser and the wing",
	} {
		if !strings.Contains(guide, want) {
			t.Errorf("personal remote wing guide does not contain %q", want)
		}
	}
	if strings.Contains(strings.ToLower(guide), "grandfather") {
		t.Error("personal remote wing guide discusses grandfathered access")
	}
}

func TestSignedInDocumentationUsesThisRoostAppURL(t *testing.T) {
	for _, test := range []struct {
		name    string
		config  ServerConfig
		wantURL string
	}{
		{name: "single host", config: ServerConfig{}, wantURL: "/app/"},
		{name: "split host", config: ServerConfig{BaseURL: "https://login.example.test", AppHost: "app.example.test"}, wantURL: "https://app.example.test/"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := NewServer(nil, test.config)
			server.LocalMode = true
			server.SetLocalUser(&User{ID: "local", DisplayName: "Local User"})
			request := httptest.NewRequest(http.MethodGet, "http://localhost/docs", nil)
			request.Host = "localhost"
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, request)
			rendered := recorder.Body.String()
			if !strings.Contains(rendered, `href="`+test.wantURL+`" class="nav-cta">Open app</a>`) {
				t.Fatalf("documentation nav did not use app URL %q: %q", test.wantURL, recorder.Body.String())
			}
			for _, forbidden := range []string{">hosted app</a>", ">hosted login</a>"} {
				if strings.Contains(rendered, forbidden) {
					t.Errorf("documentation nav uses deployment-specific label %q", forbidden)
				}
			}
		})
	}
}

func TestSignedInHomeUsesConfiguredAppURLForLinkAndShortcut(t *testing.T) {
	var body bytes.Buffer
	err := homeTmpl.ExecuteTemplate(&body, "base", pageData{
		User: &User{ID: "signed-in", DisplayName: "Signed In"}, AppURL: "https://app.example.test/",
	})
	if err != nil {
		t.Fatal(err)
	}
	rendered := body.String()
	if !strings.Contains(rendered, `id="prompt-app" href="https://app.example.test/"`) {
		t.Fatalf("signed-in home omitted configured app link: %q", rendered)
	}
	if !strings.Contains(rendered, `href="https://app.example.test/" class="nav-cta">Open app</a>`) {
		t.Fatalf("signed-in home omitted deployment-neutral app nav: %q", rendered)
	}
	if strings.Contains(rendered, ">hosted app</a>") || strings.Contains(rendered, ">hosted login</a>") {
		t.Fatalf("signed-in home uses deployment-specific nav label: %q", rendered)
	}
	if strings.Contains(rendered, "h==='wingthing.ai'") || !strings.Contains(rendered, "document.getElementById('prompt-app').href") {
		t.Fatalf("home keyboard shortcut is not driven by the configured app link: %q", rendered)
	}
}

func TestHomepageStoryPublicRoute(t *testing.T) {
	_, ts := testServer(t)
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d", resp.StatusCode)
	}
	page := string(body)
	for _, contract := range []string{
		`href="/install" class="nav-cta">Install wingthing`,
		`href="/login">Log in`,
		`href="/install" class="hero-install">Install wingthing</a>`,
		`href="/patterns">See patterns</a>`,
		"Example · 8 sessions",
		"Agents run on your machines. Watch and steer from the browser, your phone or the CLI.",
	} {
		if !strings.Contains(page, contract) {
			t.Errorf("home is missing %q", contract)
		}
	}
	previous := -1
	for _, section := range []string{`class="hero"`, `class="product-preview"`, `class="how"`, `class="closing"`} {
		index := strings.Index(page, section)
		if index <= previous {
			t.Errorf("home section %q is missing or out of order", section)
		}
		previous = index
	}
	for _, forbidden := range []string{"autoplay", `class="tab-bar"`, `data-route=`, ">hosted app</a>", ">hosted login</a>"} {
		if strings.Contains(page, forbidden) {
			t.Errorf("home includes obsolete contract %q", forbidden)
		}
	}
}

func TestLegacyHeroRedirectsToHomepage(t *testing.T) {
	srv := NewServer(testStore(t), ServerConfig{})
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			response := httptest.NewRecorder()
			srv.ServeHTTP(response, httptest.NewRequest(method, "/hero.mp4", nil))
			if response.Code != http.StatusMovedPermanently {
				t.Fatalf("%s /hero.mp4 status = %d, want 301", method, response.Code)
			}
			if location := response.Header().Get("Location"); location != "/" {
				t.Fatalf("%s /hero.mp4 Location = %q, want /", method, location)
			}
		})
	}
}

func TestPublicDemoAssetsSupportPlayback(t *testing.T) {
	_, ts := testServer(t)
	for _, asset := range []struct {
		path, contentType, prefix string
	}{
		{"homepage.webm", "video/webm", "\x1a\x45\xdf\xa3"},
		{"poster.png", "image/png", "\x89PNG"},
		{"captions.vtt", "text/vtt", "WEBVTT"},
	} {
		t.Run(asset.path, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, ts.URL+"/app/demo/"+asset.path, nil)
			mustTest(t, err)
			req.Header.Set("Range", "bytes=0-31")
			resp, err := http.DefaultClient.Do(req)
			mustTest(t, err)
			defer closeTestBody(t, resp.Body)
			body, err := io.ReadAll(resp.Body)
			mustTest(t, err)
			if resp.StatusCode != http.StatusPartialContent || !strings.HasPrefix(resp.Header.Get("Content-Type"), asset.contentType) || !bytes.HasPrefix(body, []byte(asset.prefix)) {
				t.Fatalf("asset %s: status=%d type=%q prefix=%x", asset.path, resp.StatusCode, resp.Header.Get("Content-Type"), body)
			}
		})
	}
}

func TestPublicDocsAndInstallRenderSetupHierarchy(t *testing.T) {
	_, ts := testServer(t)
	for _, test := range []struct {
		path      string
		contracts []string
	}{
		{
			path: "/install",
			contracts: []string{
				"Install wingthing",
				"Use it without an account",
				`class="inst-open">Open the app</a>`,
			},
		},
		{
			path: "/docs",
			contracts: []string{
				"connector token",
				"Run <code>wt start</code> on the parent only when that machine also executes agents",
				"Remote execution through direct MCP or the hosted browser needs a running wing.",
				"Local stdio MCP, local terminal commands, and an embedded self-hosted roost do not require a separate wing daemon.",
			},
		},
	} {
		t.Run(test.path, func(t *testing.T) {
			resp, err := http.Get(ts.URL + test.path)
			if err != nil {
				t.Fatalf("GET %s: %v", test.path, err)
			}
			defer closeTestBody(t, resp.Body)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("GET %s status = %d, want %d", test.path, resp.StatusCode, http.StatusOK)
			}
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read %s: %v", test.path, err)
			}
			page := string(body)
			previous := -1
			routes := []string{`data-route="local-human"`, `data-route="local-agent"`, `data-route="direct-remote"`, `data-route="self-hosted-browser"`, `data-route="hosted-relay"`}
			if test.path == "/install" {
				routes = []string{"curl -fsSL https://wingthing.ai/install.sh | sh", ">wt login<", ">wt start<", `class="inst-open">Open the app</a>`, "Use it without an account", ">wt egg claude --name work<"}
			}
			for _, route := range routes {
				index := strings.Index(page, route)
				if index < 0 {
					t.Fatalf("%s is missing rendered route %q", test.path, route)
				}
				if index <= previous {
					t.Fatalf("%s renders route %q out of first-local-session order", test.path, route)
				}
				previous = index
			}
			for _, contract := range append(test.contracts,
				`href="/install" class="nav-cta">Install wingthing`,
				`href="/login">Log in`,
			) {
				if !strings.Contains(page, contract) {
					t.Errorf("%s does not contain rendered contract %q", test.path, contract)
				}
			}
			for _, forbidden := range []string{">hosted app</a>", ">hosted login</a>"} {
				if strings.Contains(page, forbidden) {
					t.Errorf("%s uses deployment-specific nav label %q", test.path, forbidden)
				}
			}
		})
	}
}

func TestHomepageStorySandboxBoundary(t *testing.T) {
	var body bytes.Buffer
	if err := homeTmpl.ExecuteTemplate(&body, "base", pageData{}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Set the boundaries",
		"Inspect sandbox policy before launch. Control file, network, and environment access.",
		"Agents run on your machines. Watch and steer from the browser, your phone or the CLI.",
	} {
		if !strings.Contains(body.String(), want) {
			t.Errorf("home omitted execution boundary %q", want)
		}
	}
}

func TestPatternMarkdownRoutesServeCheckedInRecipes(t *testing.T) {
	_, ts := testServer(t)
	paths := []string{
		"/patterns/SKILL.md",
		"/patterns/local-sandbox/INSTRUCTIONS.md",
		"/patterns/local-subagents/INSTRUCTIONS.md",
		"/patterns/hosted-browser-wing/INSTRUCTIONS.md",
		"/patterns/personal-remote-wing/INSTRUCTIONS.md",
		"/patterns/shared-web-roost/INSTRUCTIONS.md",
		"/patterns/shared-roost-agents/INSTRUCTIONS.md",
		"/patterns/remote-orchestration/INSTRUCTIONS.md",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			resp, err := http.Get(ts.URL + path)
			if err != nil {
				t.Fatalf("GET %s: %v", path, err)
			}
			defer closeTestBody(t, resp.Body)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			if got := resp.Header.Get("Content-Type"); got != "text/markdown; charset=utf-8" {
				t.Fatalf("content type = %q", got)
			}
		})
	}
}

func TestUnimplementedPatternsAreNotPublished(t *testing.T) {
	_, ts := testServer(t)
	resp, err := http.Get(ts.URL + "/patterns/independent-roosts/INSTRUCTIONS.md")
	if err != nil {
		t.Fatalf("GET removed pattern: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("removed pattern status = %d, want 404", resp.StatusCode)
	}
}

func TestStaticSW(t *testing.T) {
	_, ts := testServer(t)

	resp, err := http.Get(ts.URL + "/app/sw.js")
	if err != nil {
		t.Fatalf("GET /app/sw.js: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestStaticFontsAreCacheableAndAppIsNot(t *testing.T) {
	_, ts := testServer(t)

	resp, err := http.Get(ts.URL + "/app/fonts/JetBrainsMono-Regular.woff2")
	if err != nil {
		t.Fatalf("GET font: %v", err)
	}
	closeTestBody(t, resp.Body)
	// The placeholder web/dist has no fonts; the header is the contract here.
	if got := resp.Header.Get("Cache-Control"); got != "public, max-age=604800" {
		t.Fatalf("font Cache-Control = %q", got)
	}
	resp, err = http.Get(ts.URL + "/app/sw.js")
	if err != nil {
		t.Fatalf("GET sw.js: %v", err)
	}
	closeTestBody(t, resp.Body)
	if got := resp.Header.Get("Cache-Control"); got != "" {
		t.Fatalf("sw.js Cache-Control = %q, want none", got)
	}
}

func TestStaticManifest(t *testing.T) {
	_, ts := testServer(t)

	resp, err := http.Get(ts.URL + "/app/manifest.json")
	if err != nil {
		t.Fatalf("GET /app/manifest.json: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	var body map[string]any
	decodeTestJSON(t, resp.Body, &body)
	if body["name"] != "wingthing" {
		t.Errorf("manifest name = %q, want wingthing", body["name"])
	}
}

func TestAuthCheckReturnsUserInfo(t *testing.T) {
	srv, ts := testServer(t)
	srv.DevMode = true

	// Create a user with profile data
	token, userID := createTestToken(t, srv.Store, "dev1")
	mustTestExec(t, srv.Store.DB(), "UPDATE users SET display_name = ?, email = ?, provider = ? WHERE id = ?",
		"Test User", "user@example.com", "github", userID)

	req, _ := http.NewRequest("GET", ts.URL+"/auth/check", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /auth/check: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body map[string]any
	decodeTestJSON(t, resp.Body, &body)
	if body["ok"] != true {
		t.Errorf("ok = %v, want true", body["ok"])
	}
	if body["user_id"] != userID {
		t.Errorf("user_id = %v, want %s", body["user_id"], userID)
	}
	if body["display_name"] != "Test User" {
		t.Errorf("display_name = %v, want Test User", body["display_name"])
	}
	if body["email"] != "user@example.com" {
		t.Errorf("email = %v, want user@example.com", body["email"])
	}
	if body["provider"] != "github" {
		t.Errorf("provider = %v, want github", body["provider"])
	}
}

func TestAuthCheckUnauthorized(t *testing.T) {
	_, ts := testServer(t)

	req, _ := http.NewRequest("GET", ts.URL+"/auth/check", nil)
	req.Header.Set("Authorization", "Bearer invalid-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /auth/check: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != 401 {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestAuthCheckNoAuthHeader(t *testing.T) {
	_, ts := testServer(t)

	resp, err := http.Get(ts.URL + "/auth/check")
	if err != nil {
		t.Fatalf("GET /auth/check: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != 401 {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestAuthTokenReturnsUserInfo(t *testing.T) {
	srv, ts := testServer(t)
	srv.DevMode = true

	// Device flow: request code (dev mode auto-claims)
	resp, err := http.Post(ts.URL+"/auth/device", "application/json",
		strings.NewReader(`{"wing_id":"mac1","public_key":"dGVzdGtleQ=="}`))
	if err != nil {
		t.Fatalf("POST /auth/device: %v", err)
	}
	var dcResp struct {
		DeviceCode string `json:"device_code"`
	}
	decodeTestJSON(t, resp.Body, &dcResp)
	closeTestBody(t, resp.Body)

	// Set user profile on the auto-created dev user before polling for token
	// Dev mode creates user with ID = "test-user"
	mustTestExec(t, srv.Store.DB(), "UPDATE users SET display_name = ?, email = ?, provider = ? WHERE id = ?",
		"Alice Example", "alice@example.com", "google", "test-user")

	// Poll for token
	resp2, err := http.Post(ts.URL+"/auth/token", "application/json",
		strings.NewReader(`{"device_code":"`+dcResp.DeviceCode+`"}`))
	if err != nil {
		t.Fatalf("POST /auth/token: %v", err)
	}
	var tokenResp map[string]any
	decodeTestJSON(t, resp2.Body, &tokenResp)
	closeTestBody(t, resp2.Body)

	if tokenResp["token"] == nil || tokenResp["token"] == "" {
		t.Fatal("expected token in response")
	}
	if tokenResp["display_name"] != "Alice Example" {
		t.Errorf("display_name = %v, want Alice Example", tokenResp["display_name"])
	}
	if tokenResp["email"] != "alice@example.com" {
		t.Errorf("email = %v, want alice@example.com", tokenResp["email"])
	}
	if tokenResp["provider"] != "google" {
		t.Errorf("provider = %v, want google", tokenResp["provider"])
	}
}

func TestWSHostRouting(t *testing.T) {
	store := testStore(t)
	key, _, err := GenerateECKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	srv := NewServer(store, ServerConfig{WSHost: "ws.test.local"})
	srv.SetJWTKey(key)

	token, _ := createTestToken(t, store, "dev1")

	tests := []struct {
		name string
		host string
		path string
		want int
	}{
		{"ws host /health allowed", "ws.test.local", "/health", 200},
		{"ws host /auth/check allowed", "ws.test.local", "/auth/check?token=" + token, 200},
		{"ws host /auth/device blocked", "ws.test.local", "/auth/device", 404},
		{"ws host /auth/token blocked", "ws.test.local", "/auth/token", 404},
		{"ws host /auth/claim blocked", "ws.test.local", "/auth/claim", 404},
		{"ws host /auth/google blocked", "ws.test.local", "/auth/google", 404},
		{"ws host /api/app/me blocked", "ws.test.local", "/api/app/me", 404},
		{"ws host / blocked", "ws.test.local", "/", 404},
		{"ws host /app blocked", "ws.test.local", "/app", 404},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.path, nil)
			req.Host = tt.host
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)
			if w.Code != tt.want {
				t.Errorf("%s %s: status = %d, want %d", tt.host, tt.path, w.Code, tt.want)
			}
		})
	}
}

func TestWingRegistryPublishesImmutablePolicySnapshots(t *testing.T) {
	registry := NewWingRegistry()
	original := &ConnectedWing{
		ID: "connection", WingID: "wing", Locked: false,
		AllowedCount: 1, DirectMCP: true, HostedRelay: "allow",
	}
	stored := registry.Add(original)
	if stored.Revision != 1 {
		t.Fatalf("initial registry revision = %d, want 1", stored.Revision)
	}
	original.HostedRelay = "deny"
	if got := registry.FindByID("wing"); got.HostedRelay != "allow" {
		t.Fatalf("registry retained caller-owned mutable entry: %#v", got)
	}

	updated := registry.UpdateConfig("connection", true, 2, false, "deny")
	if updated == nil || !updated.Locked || updated.AllowedCount != 2 || updated.DirectMCP || updated.HostedRelay != "deny" {
		t.Fatalf("updated entry = %#v", updated)
	}
	if updated.Revision != 2 {
		t.Fatalf("updated registry revision = %d, want 2", updated.Revision)
	}
	if stored.Locked || stored.AllowedCount != 1 || !stored.DirectMCP || stored.HostedRelay != "allow" {
		t.Fatalf("policy update mutated a previously published snapshot: %#v", stored)
	}

	beforeTouch := updated.LastSeen
	registry.Touch("connection")
	if !updated.LastSeen.Equal(beforeTouch) {
		t.Fatal("heartbeat mutated a previously published snapshot")
	}
	if current := registry.FindByID("wing"); !current.LastSeen.After(beforeTouch) {
		t.Fatalf("heartbeat was not published: before=%v current=%v", beforeTouch, current.LastSeen)
	}
}

func TestWingRegistryFindByIDPrefersFreshestConnection(t *testing.T) {
	registry := NewWingRegistry()
	older := time.Now().Add(-time.Minute)
	newer := time.Now()
	registry.Add(&ConnectedWing{ID: "old-connection", WingID: "same-wing", ConnectedAt: older, LastSeen: newer.Add(time.Minute)})
	registry.Add(&ConnectedWing{ID: "new-connection", WingID: "same-wing", ConnectedAt: newer, LastSeen: newer})

	if got := registry.FindByID("same-wing"); got == nil || got.ID != "new-connection" {
		t.Fatalf("selected connection = %#v", got)
	}
}

func TestWingRegistryActivationSupersedesReconnectWithoutFalseOffline(t *testing.T) {
	registry := NewWingRegistry()
	base := time.Now()
	old, stale, active := registry.Activate(&ConnectedWing{ID: "old", WingID: "wing", ConnectedAt: base})
	if !active || len(stale) != 0 || old.ID != "old" {
		t.Fatalf("first activation = old=%#v stale=%#v active=%v", old, stale, active)
	}
	current, stale, active := registry.Activate(&ConnectedWing{ID: "current", WingID: "wing", ConnectedAt: base.Add(time.Second)})
	if !active || current.ID != "current" || len(stale) != 1 || stale[0].ID != "old" {
		t.Fatalf("replacement activation = current=%#v stale=%#v active=%v", current, stale, active)
	}
	if removed, offline := registry.RemoveConnection("old"); removed != nil || offline {
		t.Fatalf("superseded removal = removed=%#v offline=%v", removed, offline)
	}
	if removed, offline := registry.RemoveConnection("current"); removed == nil || !offline {
		t.Fatalf("current removal = removed=%#v offline=%v", removed, offline)
	}
}

func TestWingRegistryActivationCannotEvictAnotherOwner(t *testing.T) {
	registry := NewWingRegistry()
	base := time.Now()
	original, stale, active := registry.Activate(&ConnectedWing{ID: "alice-connection", UserID: "alice", WingID: "shared-id", ConnectedAt: base})
	if !active || len(stale) != 0 || original.UserID != "alice" {
		t.Fatalf("first activation = original=%#v stale=%#v active=%v", original, stale, active)
	}
	intruder, stale, active := registry.Activate(&ConnectedWing{ID: "mallory-connection", UserID: "mallory", WingID: "shared-id", ConnectedAt: base.Add(time.Second)})
	if active || len(stale) != 0 || intruder.UserID != "mallory" {
		t.Fatalf("cross-owner activation = intruder=%#v stale=%#v active=%v", intruder, stale, active)
	}
	if got := registry.FindByID("shared-id"); got == nil || got.ID != "alice-connection" || got.UserID != "alice" {
		t.Fatalf("cross-owner collision replaced legitimate wing: %#v", got)
	}
}
