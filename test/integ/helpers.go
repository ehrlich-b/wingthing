//go:build e2e

package integ

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/relay"
)

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
