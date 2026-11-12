package relay

import "testing"

func TestNotificationSessionLink(t *testing.T) {
	if got, want := NewServer(nil, ServerConfig{AppHost: "app.wingthing.ai", BaseURL: "https://wingthing.ai"}).ntfyClickURL("machine/one", "session?two"), "https://app.wingthing.ai/#session/machine%2Fone/session%3Ftwo"; got != want {
		t.Fatalf("notification route = %q, want %q", got, want)
	}
}
