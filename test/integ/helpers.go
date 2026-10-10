//go:build e2e

package integ

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/relay"
)

// Keep real egg socket paths short on macOS and fixture state inside the clone.
func shortIntegrationRoot(t *testing.T) string {
	t.Helper()
	scratch, err := filepath.Abs("../../.scratch")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(scratch, "i")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			logs, _ := filepath.Glob(filepath.Join(root, "*", "eggs", "*", "egg.log"))
			for _, path := range logs {
				data, err := os.ReadFile(path)
				t.Logf("fixture egg log %s: %s (read error: %v)", path, data, err)
			}
		}
		_ = os.RemoveAll(root)
	})
	return root
}

// testRelayAndWS creates an in-memory relay server with an httptest server.
// Returns the server, httptest server, and store. Cleanup is registered on t.
func testRelayAndWS(t *testing.T) (*relay.Server, *httptest.Server, *relay.RelayStore) {
	t.Helper()
	store, err := relay.OpenRelay(":memory:")
	if err != nil {
		t.Fatalf("open relay store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	key, _, err := relay.GenerateECKey()
	if err != nil {
		t.Fatalf("generate jwt key: %v", err)
	}
	srv := relay.NewServer(store, relay.ServerConfig{})
	srv.SetJWTKey(key)
	srv.DevMode = true

	ts := httptest.NewServer(srv)
	t.Cleanup(func() { ts.Close() })

	return srv, ts, store
}

// createTestUser creates a user and device token in the relay store.
// Returns the token string and user ID.
func createTestUser(t *testing.T, store *relay.RelayStore, id string) (token, userID string) {
	t.Helper()
	userID = "user-" + id
	if err := store.CreateUser(userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	token = "tok-" + id
	if err := store.CreateDeviceToken(token, userID, "device-"+id, nil); err != nil {
		t.Fatalf("create token: %v", err)
	}
	return token, userID
}

// wsURL converts an httptest server URL from http:// to ws://.
func wsURL(ts *httptest.Server) string {
	return strings.Replace(ts.URL, "http://", "ws://", 1)
}
