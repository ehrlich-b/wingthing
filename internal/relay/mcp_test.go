package relay

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/mcp"
)

func TestRoostNativeMCPAllowsAuthenticatedUserWithoutExecutableToolRole(t *testing.T) {
	srv, ts := testServer(t)
	srv.RoostMode = true
	if err := srv.Store.CreateUser("carol"); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Store.DB().Exec("UPDATE users SET email = ? WHERE id = ?", "carol@example.com", "carol"); err != nil {
		t.Fatal(err)
	}
	if err := srv.Store.CreateSession("sess-carol", "carol", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	srv.EnableMCP(nil, nil, mcp.NativeTool{
		Name: "terminal_list", Title: "List owned terminals",
		InputSchema: map[string]any{"type": "object", "additionalProperties": false},
		Call: func(_ context.Context, principal mcp.Principal, _ json.RawMessage) (map[string]any, bool, error) {
			return map[string]any{"owner_id": principal.UserID, "actor_id": principal.ClientID}, false, nil
		},
	})

	clientID := oauthRegister(t, ts.URL, "http://localhost:9999/cb")
	verifier := "verifier-abcdefghijklmnopqrstuvwxyz-0123456789"
	sum := sha256.Sum256([]byte(verifier))
	code := oauthAuthorize(t, ts.URL, clientID, "http://localhost:9999/cb",
		base64.RawURLEncoding.EncodeToString(sum[:]), "sess-carol")
	token := oauthToken(t, ts.URL, clientID, "http://localhost:9999/cb", code, verifier)
	if names := mcpToolNames(t, ts.URL, token); !names["terminal_list"] {
		t.Fatalf("native control tool missing: %v", names)
	}

	body := `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"terminal_list","arguments":{}}}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("MCP-Protocol-Version", "2025-11-25")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var result struct {
		Result struct {
			Structured map[string]any `json:"structuredContent"`
			IsError    bool           `json:"isError"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Result.IsError || result.Result.Structured["owner_id"] != "carol" || result.Result.Structured["actor_id"] != clientID {
		t.Fatalf("native result = %#v", result.Result)
	}
	var audit string
	if err := srv.Store.DB().QueryRow("SELECT detail FROM audit_log WHERE event = 'mcp_control_call' ORDER BY id DESC LIMIT 1").Scan(&audit); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(audit, `"owner_id":"carol"`) || !strings.Contains(audit, `"actor_id":"`+clientID+`"`) {
		t.Fatalf("native audit = %s", audit)
	}
}

// mcpTestServer builds a roost with the MCP surface enabled, one email'd user in role "eng"
// (which is denied slide-db), and a web session for that user.
func mcpTestServer(t *testing.T) (*Server, *httptest.Server, string) {
	t.Helper()
	srv, ts := testServer(t)

	if err := srv.Store.CreateUser("alice"); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := srv.Store.DB().Exec("UPDATE users SET email = ? WHERE id = ?", "alice@example.com", "alice"); err != nil {
		t.Fatalf("set email: %v", err)
	}
	if err := srv.Store.CreateSession("sess1", "alice", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("create session: %v", err)
	}

	tools := []*config.ToolConfig{
		{Name: "slide-db", Run: `echo "db:$1"`, Timeout: "5s"},
		{Name: "crm-lookup", Run: `echo "crm:$1"`, Timeout: "5s"},
		{Name: "whoami", Run: `echo "$WT_MCP_USER|$WT_MCP_EMAIL|$WT_MCP_ROLES"`, Timeout: "5s"},
	}
	policy := &mcp.Policy{
		DefaultAllowAll: false,
		Roles: map[string]*mcp.RolePolicy{
			"eng":   {Enabled: true, Deny: []string{"slide-db"}, Members: []string{"alice@example.com"}},
			"sales": {Enabled: false, Members: []string{"alice@example.com"}},
		},
	}
	srv.EnableMCP(egg.NewToolRunner(tools), policy)
	return srv, ts, "sess1"
}

func TestMCPOAuthFlowAndScoping(t *testing.T) {
	srv, ts, session := mcpTestServer(t)

	// 1. Unauthenticated /mcp must 401 with the OAuth challenge (RFC 9728).
	resp, err := http.Post(ts.URL+"/mcp", "application/json",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /mcp = %d, want 401", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("WWW-Authenticate"), "resource_metadata=") {
		t.Fatalf("missing WWW-Authenticate challenge: %q", resp.Header.Get("WWW-Authenticate"))
	}

	// 2. Dynamic client registration.
	clientID := oauthRegister(t, ts.URL, "http://localhost:9999/cb")

	// 3. Authorize with the user's session -> auth code (PKCE).
	verifier := "verifier-abcdefghijklmnopqrstuvwxyz-0123456789"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	code := oauthAuthorize(t, ts.URL, clientID, "http://localhost:9999/cb", challenge, session)

	// 4. Exchange code + verifier for an access token.
	token := oauthToken(t, ts.URL, clientID, "http://localhost:9999/cb", code, verifier)
	if token == "" {
		t.Fatal("empty access token")
	}

	// 5. Authenticated tools/list is scoped to alice's role (eng denies slide-db).
	names := mcpToolNames(t, ts.URL, token)
	if _, ok := names["slide-db"]; ok {
		t.Errorf("eng must NOT see slide-db; got %v", names)
	}
	if _, ok := names["crm-lookup"]; !ok {
		t.Errorf("eng should see crm-lookup; got %v", names)
	}
	if _, ok := names["whoami"]; !ok {
		t.Errorf("eng should see whoami; got %v", names)
	}

	// 6. Calling a permitted tool works; a denied tool is rejected (invisible).
	if out, isErr := mcpCall(t, ts.URL, token, "crm-lookup", []string{"Acme"}); isErr || !strings.Contains(out, "crm:Acme") {
		t.Errorf("crm-lookup call failed: out=%q isErr=%v", out, isErr)
	}
	if _, isErr := mcpCall(t, ts.URL, token, "slide-db", []string{"SELECT 1"}); !isErr {
		t.Error("slide-db call should be rejected for eng")
	}
	if out, isErr := mcpCall(t, ts.URL, token, "whoami", nil); isErr || !strings.Contains(out, "alice|alice@example.com|eng") {
		t.Errorf("MCP identity env missing: out=%q isErr=%v", out, isErr)
	}
	var auditDetail string
	if err := srv.Store.DB().QueryRow(
		"SELECT detail FROM audit_log WHERE event = 'mcp_tool_call' AND detail LIKE '%crm-lookup%' ORDER BY id DESC LIMIT 1",
	).Scan(&auditDetail); err != nil {
		t.Fatalf("missing per-call audit: %v", err)
	}
	if !strings.Contains(auditDetail, `"args":["Acme"]`) || !strings.Contains(auditDetail, `"client_id"`) {
		t.Fatalf("incomplete per-call audit: %s", auditDetail)
	}
}

func TestMCPReloadAtomicallyReplacesPolicyAndTools(t *testing.T) {
	srv, ts, session := mcpTestServer(t)
	clientID := oauthRegister(t, ts.URL, "http://localhost:9999/cb")
	verifier := "verifier-abcdefghijklmnopqrstuvwxyz-0123456789"
	sum := sha256.Sum256([]byte(verifier))
	code := oauthAuthorize(t, ts.URL, clientID, "http://localhost:9999/cb",
		base64.RawURLEncoding.EncodeToString(sum[:]), session)
	token := oauthToken(t, ts.URL, clientID, "http://localhost:9999/cb", code, verifier)

	policy := &mcp.Policy{Roles: map[string]*mcp.RolePolicy{
		"new-role": {Enabled: true, Allow: []string{"new-tool"}, Members: []string{"alice@example.com"}},
	}}
	srv.ReloadMCP(egg.NewToolRunner([]*config.ToolConfig{{
		Name: "new-tool", Run: `printf reloaded`, Timeout: "5s",
	}}), policy)

	names := mcpToolNames(t, ts.URL, token)
	if len(names) != 1 {
		t.Fatalf("reloaded tools = %v", names)
	}
	if _, ok := names["new-tool"]; !ok {
		t.Fatalf("new tool missing after reload: %v", names)
	}
	if out, isErr := mcpCall(t, ts.URL, token, "new-tool", nil); isErr || out != "reloaded" {
		t.Fatalf("reloaded tool call = %q, isError=%v", out, isErr)
	}
}

// TestMCPOAuthLoginBounce covers the unauthenticated path: /oauth/authorize must stash the
// request and bounce through login with a redirect that contains no "://" (else isSafeRedirect
// drops it and the client hangs after login), then resume via the opaque rid.
func TestMCPOAuthLoginBounce(t *testing.T) {
	_, ts, session := mcpTestServer(t)
	clientID := oauthRegister(t, ts.URL, "http://localhost:9999/cb")
	challenge := "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"

	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	q := url.Values{
		"client_id": {clientID}, "redirect_uri": {"http://localhost:9999/cb"}, "response_type": {"code"},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {"xyz"},
		"resource": {ts.URL + "/mcp"},
	}

	// Unauthenticated (no session cookie) -> bounce to login.
	req, _ := http.NewRequest("GET", ts.URL+"/oauth/authorize?"+q.Encode(), nil)
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("unauth authorize = %d, want 303 to login", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if strings.Contains(loc, "://") {
		t.Errorf("login redirect must not contain :// (would fail isSafeRedirect): %s", loc)
	}
	next := mustQuery(t, loc, "next")
	if !strings.HasPrefix(next, "/oauth/authorize?rid=") || strings.Contains(next, "://") {
		t.Fatalf("expected safe rid resume url, got %q", next)
	}
	for _, c := range resp.Cookies() {
		if c.Name == "oauth_next" && strings.Contains(c.Value, "://") {
			t.Errorf("oauth_next cookie must not contain ://: %s", c.Value)
		}
	}

	// Resume with a session (post-login) -> consent page, then approve -> code preserving state.
	req2, _ := http.NewRequest("GET", ts.URL+next, nil)
	req2.AddCookie(&http.Cookie{Name: "wt_session", Value: session})
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	buf, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("resume authorize = %d, want 200 consent", resp2.StatusCode)
	}
	m := ridRe.FindStringSubmatch(string(buf))
	if m == nil {
		t.Fatal("resume did not render the consent page")
	}
	codeLoc := oauthDecide(t, ts.URL, m[1], session, "approve")
	if mustQuery(t, codeLoc, "code") == "" {
		t.Fatalf("no code after approve: %s", codeLoc)
	}
	if got := mustQuery(t, codeLoc, "state"); got != "xyz" {
		t.Errorf("state not preserved through bounce: %q", got)
	}
}

// TestMCPOAuthConsentDeny verifies that denying consent returns access_denied and no code.
func TestMCPOAuthConsentDeny(t *testing.T) {
	_, ts, session := mcpTestServer(t)
	clientID := oauthRegister(t, ts.URL, "http://localhost:9999/cb")
	rid, _ := oauthConsentPage(t, ts.URL, clientID, "http://localhost:9999/cb",
		"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM", session)
	loc := oauthDecide(t, ts.URL, rid, session, "deny")
	u, _ := url.Parse(loc)
	if u.Query().Get("error") != "access_denied" {
		t.Fatalf("deny should redirect with error=access_denied, got: %s", loc)
	}
	if u.Query().Get("code") != "" {
		t.Errorf("deny must not issue a code: %s", loc)
	}
}

func TestMCPOAuthResourceBindingAndRefreshRotation(t *testing.T) {
	srv, ts, session := mcpTestServer(t)
	clientID := oauthRegister(t, ts.URL, "http://localhost:9999/cb")
	verifier := "verifier-abcdefghijklmnopqrstuvwxyz-0123456789"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	code := oauthAuthorize(t, ts.URL, clientID, "http://localhost:9999/cb", challenge, session)
	tokens := oauthTokenPair(t, ts.URL, clientID, "http://localhost:9999/cb", code, verifier)

	claims, err := ValidateMCPJWT(srv.JWTPubKey(), tokens.AccessToken, ts.URL, ts.URL+"/mcp")
	if err != nil {
		t.Fatalf("validate MCP token: %v", err)
	}
	if claims.Subject != "alice" || claims.ClientID != clientID || claims.TokenUse != "mcp" {
		t.Fatalf("unexpected claims: %+v", claims)
	}
	if _, err := ValidateMCPJWT(srv.JWTPubKey(), tokens.AccessToken, ts.URL, ts.URL+"/other"); err == nil {
		t.Fatal("token must not validate for another audience")
	}
	if _, err := ValidateWingJWT(srv.JWTPubKey(), tokens.AccessToken); err == nil {
		t.Fatal("MCP access token must not validate as a wing credential")
	}

	rotated := oauthRefresh(t, ts.URL, clientID, tokens.RefreshToken, http.StatusOK)
	if rotated.RefreshToken == tokens.RefreshToken {
		t.Fatal("refresh token was not rotated")
	}
	if got := oauthRefresh(t, ts.URL, clientID, tokens.RefreshToken, http.StatusBadRequest); got.AccessToken != "" {
		t.Fatal("replayed refresh token unexpectedly succeeded")
	}
	if names := mcpToolNames(t, ts.URL, rotated.AccessToken); !names["crm-lookup"] {
		t.Fatalf("refreshed access token did not reach MCP: %v", names)
	}
}

func TestMCPOAuthRejectsWrongResourceAndUnsafeRedirect(t *testing.T) {
	_, ts, session := mcpTestServer(t)

	body := `{"redirect_uris":["http://example.com/cb"],"client_name":"Unsafe"}`
	resp, err := http.Post(ts.URL+"/oauth/register", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unsafe redirect registration = %d, want 400", resp.StatusCode)
	}

	clientID := oauthRegister(t, ts.URL, "http://localhost:9999/cb")
	q := url.Values{
		"client_id": {clientID}, "redirect_uri": {"http://localhost:9999/cb"}, "response_type": {"code"},
		"code_challenge":        {"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"},
		"code_challenge_method": {"S256"}, "resource": {ts.URL + "/not-mcp"},
	}
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/oauth/authorize?"+q.Encode(), nil)
	req.AddCookie(&http.Cookie{Name: "wt_session", Value: session})
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("wrong resource authorize = %d, want 400", resp.StatusCode)
	}

	q.Set("resource", ts.URL+"/mcp")
	q.Set("code_challenge", "short-and-not-a-sha256-challenge")
	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/oauth/authorize?"+q.Encode(), nil)
	req.AddCookie(&http.Cookie{Name: "wt_session", Value: session})
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed PKCE challenge authorize = %d, want 400", resp.StatusCode)
	}
}

func TestMCPOAuthClientRegistrationSurvivesMemoryReset(t *testing.T) {
	srv, ts, session := mcpTestServer(t)
	redirectURI := "http://localhost:3118/callback"
	clientID := oauthRegister(t, ts.URL, redirectURI)

	// Model a process restart by dropping the in-memory cache. The durable registration
	// must be reloaded when Claude presents its cached client ID.
	srv.mcpOAuth.mu.Lock()
	srv.mcpOAuth.clients = map[string]oauthClient{}
	srv.mcpOAuth.mu.Unlock()

	q := url.Values{
		"client_id": {clientID}, "redirect_uri": {redirectURI}, "response_type": {"code"},
		"code_challenge":        {"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"},
		"code_challenge_method": {"S256"}, "resource": {ts.URL + "/mcp"},
	}
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/oauth/authorize?"+q.Encode(), nil)
	req.AddCookie(&http.Cookie{Name: "wt_session", Value: session})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("authorize after memory reset = %d: %s", resp.StatusCode, body)
	}
}

func TestMCPRedirectMatchingAllowsOnlyLoopbackPortVariation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		registered string
		requested  string
		want       bool
	}{
		{
			name: "localhost port", registered: "http://localhost:3118/callback",
			requested: "http://localhost:49152/callback", want: true,
		},
		{
			name: "IPv4 port", registered: "http://127.0.0.1:3118/callback",
			requested: "http://127.0.0.1:49152/callback", want: true,
		},
		{
			name: "path differs", registered: "http://localhost:3118/callback",
			requested: "http://localhost:49152/other", want: false,
		},
		{
			name: "host differs", registered: "http://localhost:3118/callback",
			requested: "http://127.0.0.1:49152/callback", want: false,
		},
		{
			name: "HTTPS remains exact", registered: "https://client.example:3118/callback",
			requested: "https://client.example:49152/callback", want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := redirectURIMatches(tc.registered, tc.requested); got != tc.want {
				t.Fatalf("redirectURIMatches() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMCPRateLimitCoverage(t *testing.T) {
	s := &Server{}
	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/oauth/register"},
		{http.MethodGet, "/oauth/authorize"},
		{http.MethodPost, "/oauth/token"},
		{http.MethodPost, "/mcp"},
	} {
		if !s.shouldRateLimit(tc.method, tc.path) {
			t.Errorf("%s %s is not rate limited", tc.method, tc.path)
		}
	}
	if s.shouldRateLimit(http.MethodGet, "/mcp") {
		t.Error("nonexistent GET /mcp should not consume the MCP call limit")
	}
}

func TestMCPRejectsGeneralWingJWT(t *testing.T) {
	srv, ts, _ := mcpTestServer(t)
	wingToken, _, err := IssueWingJWT(srv.jwtKey, "alice", "pub", "wing-1")
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`,
	))
	req.Header.Set("Authorization", "Bearer "+wingToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wing JWT at MCP endpoint = %d, want 401", resp.StatusCode)
	}
}

func TestMCPRejectsCrossOriginBeforeAuthentication(t *testing.T) {
	_, ts, _ := mcpTestServer(t)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"ping","params":{}}`,
	))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://attacker.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin unauthenticated MCP request = %d, want 403", resp.StatusCode)
	}
}

func TestJWTTokenUseSeparatesWingAndMCP(t *testing.T) {
	srv, ts, _ := mcpTestServer(t)
	wingToken, _, err := IssueWingJWT(srv.jwtKey, "alice", "pub", "wing-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateWingJWT(srv.JWTPubKey(), wingToken); err != nil {
		t.Fatalf("valid wing token rejected: %v", err)
	}
	if _, err := ValidateMCPJWT(srv.JWTPubKey(), wingToken, ts.URL, ts.URL+"/mcp"); err == nil {
		t.Fatal("wing token must not validate as an MCP access token")
	}
}

func TestMCPOAuthRejectsUserWithOnlyDisabledRoles(t *testing.T) {
	srv, ts, _ := mcpTestServer(t)
	if err := srv.Store.CreateUser("bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Store.DB().Exec("UPDATE users SET email = ? WHERE id = ?", "bob@example.com", "bob"); err != nil {
		t.Fatal(err)
	}
	if err := srv.Store.CreateSession("sess-bob", "bob", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	srv.mcpMu.Lock()
	srv.mcpPolicy.Roles["sales"].Members = append(srv.mcpPolicy.Roles["sales"].Members, "bob@example.com")
	srv.mcpMu.Unlock()

	clientID := oauthRegister(t, ts.URL, "http://localhost:9999/cb")
	q := url.Values{
		"client_id": {clientID}, "redirect_uri": {"http://localhost:9999/cb"}, "response_type": {"code"},
		"code_challenge":        {"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"},
		"code_challenge_method": {"S256"}, "resource": {ts.URL + "/mcp"},
	}
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/oauth/authorize?"+q.Encode(), nil)
	req.AddCookie(&http.Cookie{Name: "wt_session", Value: "sess-bob"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("disabled-only authorize = %d, want 403", resp.StatusCode)
	}
}

func mustQuery(t *testing.T, rawURL, key string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	return u.Query().Get(key)
}

// --- helpers ---

func oauthRegister(t *testing.T, base, redirect string) string {
	t.Helper()
	body := `{"redirect_uris":["` + redirect + `"],"client_name":"Test Client"}`
	resp, err := http.Post(base+"/oauth/register", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register = %d", resp.StatusCode)
	}
	var out struct {
		ClientID string `json:"client_id"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if out.ClientID == "" {
		t.Fatal("no client_id from registration")
	}
	return out.ClientID
}

var ridRe = regexp.MustCompile(`name="rid" value="([^"]+)"`)

// oauthAuthorize drives the consent flow: GET the consent page (asserting it names the
// client), then POST approve, returning the authorization code.
func oauthAuthorize(t *testing.T, base, clientID, redirect, challenge, session string) string {
	t.Helper()
	rid, body := oauthConsentPage(t, base, clientID, redirect, challenge, session)
	if !strings.Contains(body, "Test Client") {
		t.Errorf("consent page should name the requesting client")
	}
	loc := oauthDecide(t, base, rid, session, "approve")
	u, _ := url.Parse(loc)
	code := u.Query().Get("code")
	if code == "" {
		t.Fatalf("no code after approve: %s", loc)
	}
	if u.Query().Get("state") != "xyz" {
		t.Errorf("state not echoed: %s", u.Query().Get("state"))
	}
	return code
}

// oauthConsentPage GETs the authorize endpoint with a session and returns (rid, page body).
func oauthConsentPage(t *testing.T, base, clientID, redirect, challenge, session string) (string, string) {
	t.Helper()
	q := url.Values{
		"client_id": {clientID}, "redirect_uri": {redirect}, "response_type": {"code"},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {"xyz"},
		"resource": {base + "/mcp"},
	}
	req, _ := http.NewRequest("GET", base+"/oauth/authorize?"+q.Encode(), nil)
	req.AddCookie(&http.Cookie{Name: "wt_session", Value: session})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authorize consent = %d, want 200 (session not honored?)", resp.StatusCode)
	}
	buf, _ := io.ReadAll(resp.Body)
	m := ridRe.FindStringSubmatch(string(buf))
	if m == nil {
		t.Fatalf("no rid in consent page:\n%s", buf)
	}
	return m[1], string(buf)
}

// oauthDecide POSTs an approve/deny decision and returns the redirect Location.
func oauthDecide(t *testing.T, base, rid, session, action string) string {
	t.Helper()
	form := url.Values{"rid": {rid}, "action": {action}}
	req, _ := http.NewRequest("POST", base+"/oauth/authorize", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "wt_session", Value: session})
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("consent %s = %d, want 303", action, resp.StatusCode)
	}
	return resp.Header.Get("Location")
}

type oauthTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
}

func oauthToken(t *testing.T, base, clientID, redirect, code, verifier string) string {
	t.Helper()
	return oauthTokenPair(t, base, clientID, redirect, code, verifier).AccessToken
}

func oauthTokenPair(t *testing.T, base, clientID, redirect, code, verifier string) oauthTokens {
	t.Helper()
	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect},
		"client_id": {clientID}, "code_verifier": {verifier}, "resource": {base + "/mcp"},
	}
	resp, err := http.PostForm(base+"/oauth/token", form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token = %d", resp.StatusCode)
	}
	var out oauthTokens
	json.NewDecoder(resp.Body).Decode(&out)
	if out.TokenType != "Bearer" {
		t.Errorf("token_type = %q, want Bearer", out.TokenType)
	}
	if out.AccessToken == "" || out.RefreshToken == "" {
		t.Fatalf("token response missing access/refresh token")
	}
	return out
}

func oauthRefresh(t *testing.T, base, clientID, refreshToken string, wantStatus int) oauthTokens {
	t.Helper()
	form := url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {refreshToken},
		"client_id": {clientID}, "resource": {base + "/mcp"},
	}
	resp, err := http.PostForm(base+"/oauth/token", form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		buf, _ := io.ReadAll(resp.Body)
		t.Fatalf("refresh = %d, want %d: %s", resp.StatusCode, wantStatus, buf)
	}
	var out oauthTokens
	if wantStatus == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func mcpRPC(t *testing.T, base, token, body string) map[string]any {
	t.Helper()
	req, _ := http.NewRequest("POST", base+"/mcp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("MCP-Protocol-Version", "2025-11-25")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authenticated /mcp = %d", resp.StatusCode)
	}
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return out
}

func mcpToolNames(t *testing.T, base, token string) map[string]bool {
	t.Helper()
	out := mcpRPC(t, base, token, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	names := map[string]bool{}
	result, _ := out["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	for _, tv := range tools {
		if tm, ok := tv.(map[string]any); ok {
			if n, ok := tm["name"].(string); ok {
				names[n] = true
			}
		}
	}
	return names
}

func mcpCall(t *testing.T, base, token, name string, args []string) (string, bool) {
	t.Helper()
	argsJSON, _ := json.Marshal(args)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + name + `","arguments":{"args":` + string(argsJSON) + `}}}`
	out := mcpRPC(t, base, token, body)
	if _, isRPCErr := out["error"]; isRPCErr {
		return "", true
	}
	result, _ := out["result"].(map[string]any)
	isErr, _ := result["isError"].(bool)
	text := ""
	if content, ok := result["content"].([]any); ok && len(content) > 0 {
		if c0, ok := content[0].(map[string]any); ok {
			text, _ = c0["text"].(string)
		}
	}
	return text, isErr
}
