package relay

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMCPAnonymousRegistrationsUseOnlyTemporaryCapacity(t *testing.T) {
	server, ts, session := mcpTestServer(t)
	server.Config.ResourceLimits.MCPRegistrations = 1
	server.Config.ResourceLimits.MCPClients = 1
	redirect := "http://localhost:9999/cb"
	clientID := oauthRegister(t, ts.URL, redirect)
	reg, err := server.Store.GetMCPClientRegistration(clientID, time.Now())
	mustTest(t, err)
	if reg == nil || reg.Authorized || time.Until(reg.ExpiresAt) > time.Hour {
		t.Fatalf("anonymous registration got durable reservation: %#v", reg)
	}
	rr := httptest.NewRecorder()
	server.handleOAuthRegister(rr, httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(`{"redirect_uris":["http://localhost:9999/cb"]}`)))
	if rr.Code != 429 || !strings.Contains(rr.Body.String(), "capacity") {
		t.Fatalf("temporary capacity error: %d %s", rr.Code, rr.Body.String())
	}
	verifier := "verifier-abcdefghijklmnopqrstuvwxyz-0123456789"
	code := oauthAuthorize(t, ts.URL, clientID, redirect, pkceChallenge(verifier), session)
	reg, err = server.Store.GetMCPClientRegistration(clientID, time.Now())
	mustTest(t, err)
	if reg.Authorized {
		t.Fatal("unredeemed authorization consumed a durable slot")
	}
	_ = oauthToken(t, ts.URL, clientID, redirect, code, verifier)
	reg, err = server.Store.GetMCPClientRegistration(clientID, time.Now())
	mustTest(t, err)
	if !reg.Authorized || time.Until(reg.ExpiresAt) < 365*24*time.Hour {
		t.Fatalf("authorized client did not retain its durable ID: %#v", reg)
	}
	second := oauthRegister(t, ts.URL, redirect)
	code = oauthAuthorize(t, ts.URL, second, redirect, pkceChallenge(verifier), session)
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {second}, "redirect_uri": {redirect}, "code": {code}, "code_verifier": {verifier}, "resource": {ts.URL + "/mcp"}}
	resp, err := http.PostForm(ts.URL+"/oauth/token", form)
	mustTest(t, err)
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("durable client capacity status = %d", resp.StatusCode)
	}
	code = oauthAuthorize(t, ts.URL, clientID, redirect, pkceChallenge(verifier), session)
	_ = oauthToken(t, ts.URL, clientID, redirect, code, verifier)
}

func TestMCPUnusedRegistrationExpiresAndIsReclaimed(t *testing.T) {
	server, ts, _ := mcpTestServer(t)
	server.Config.ResourceLimits.MCPRegistrations = 1
	clientID := oauthRegister(t, ts.URL, "http://localhost:9999/cb")
	mustTestExec(t, server.Store.DB(), "UPDATE mcp_oauth_clients SET expires_at = datetime('now', '-1 second') WHERE client_id = ?", clientID)
	server.mcpOAuth.mu.Lock()
	server.mcpOAuth.clients = map[string]oauthClient{}
	server.mcpOAuth.mu.Unlock()
	if server.oauthClientRegistered(clientID) {
		t.Fatal("expired anonymous client still accepted")
	}
	_ = oauthRegister(t, ts.URL, "http://localhost:9999/cb")
	var count int
	mustTest(t, server.Store.DB().QueryRow("SELECT COUNT(*) FROM mcp_oauth_clients WHERE client_id = ?", clientID).Scan(&count))
	if count != 0 {
		t.Fatal("on-insert cleanup retained expired registration")
	}
	mustTest(t, server.Store.purgeExpiredGrants(time.Now().Add(2*time.Hour)))
	mustTest(t, server.Store.DB().QueryRow("SELECT COUNT(*) FROM mcp_oauth_clients").Scan(&count))
	if count != 0 {
		t.Fatal("grant sweep retained expired MCP registration")
	}
}

func TestMCPRegistrationMigrationPreservesAuthorizedClients(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.db")
	db, err := sql.Open("sqlite", path)
	mustTest(t, err)
	mustTestExec(t, db, "CREATE TABLE schema_migrations (version TEXT PRIMARY KEY, applied_at DATETIME DEFAULT CURRENT_TIMESTAMP)")
	entries, err := migrationsFS.ReadDir("migrations")
	mustTest(t, err)
	for _, entry := range entries {
		if entry.Name() >= "009" {
			break
		}
		body, err := migrationsFS.ReadFile("migrations/" + entry.Name())
		mustTest(t, err)
		mustTestExec(t, db, string(body))
		mustTestExec(t, db, "INSERT INTO schema_migrations(version) VALUES (?)", entry.Name())
	}
	for _, id := range []string{"used", "unused"} {
		mustTestExec(t, db, `INSERT INTO mcp_oauth_clients(client_id, redirect_uris, expires_at) VALUES (?, '["http://localhost/cb"]', '2030-01-01 00:00:00')`, id)
	}
	mustTestExec(t, db, "INSERT INTO audit_log(event, detail) VALUES ('mcp_authorized', 'client=used')")
	mustTest(t, db.Close())
	store, err := OpenRelay(path)
	mustTest(t, err)
	t.Cleanup(func() { _ = store.Close() })
	used, err := store.GetMCPClientRegistration("used", time.Now())
	mustTest(t, err)
	unused, err := store.GetMCPClientRegistration("unused", time.Now())
	mustTest(t, err)
	if !used.Authorized || time.Until(used.ExpiresAt) < 24*time.Hour || unused.Authorized || time.Until(unused.ExpiresAt) > time.Hour {
		t.Fatalf("migration registrations: used=%#v unused=%#v", used, unused)
	}
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
