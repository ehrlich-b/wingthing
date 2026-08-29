package relay

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHostedNtfyConfigurationRejectsPrivateEndpoint(t *testing.T) {
	store := testStore(t)
	if err := store.CreateUser("hosted-ntfy-user"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSession("hosted-ntfy-session", "hosted-ntfy-user", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewServer(store, ServerConfig{RelayPolicy: RelayPolicyDirectFree}))
	defer server.Close()

	request, err := http.NewRequest(
		http.MethodPost,
		server.URL+"/api/app/ntfy",
		strings.NewReader(`{"topic":"http://127.0.0.1:8080/private","events":"attention"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "hosted-ntfy-session"})
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestBody(t, response.Body)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unsafe hosted ntfy status = %d, want 400", response.StatusCode)
	}
	config, err := store.GetNtfyConfig("hosted-ntfy-user")
	if err != nil {
		t.Fatal(err)
	}
	if config.Topic != "" {
		t.Fatalf("unsafe hosted ntfy endpoint persisted as %q", config.Topic)
	}
}

func TestSelfHostedNtfyRetainsPrivateEndpointCompatibility(t *testing.T) {
	server := NewServer(nil, ServerConfig{})
	server.RoostMode = true
	client, err := server.newNtfyClient("http://127.0.0.1:8080/topic", "", "attention")
	if err != nil || client == nil {
		t.Fatalf("self-hosted private ntfy client = %#v, %v", client, err)
	}
}
