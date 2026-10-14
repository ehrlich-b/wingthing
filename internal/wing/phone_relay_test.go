package wing

import "testing"

func TestPhoneRelayInvitationRequiresRegistrationAndTrustedTransport(t *testing.T) {
	for _, tc := range []struct {
		url                 string
		registered, allowed bool
	}{
		{"https://relay.example", true, true}, {"http://127.0.0.1:18241", true, true}, {"http://[::1]:18241", true, true}, {"http://localhost:18241", true, true},
		{"http://192.168.1.2:8080", true, false}, {"https://relay.example", false, false}, {"http://127.0.0.1:18241", false, false},
		{"https://user:password@relay.example", true, false}, {"https://relay.example#secret", true, false},
	} {
		got := phoneRelayURL(tc.url, tc.registered)
		if (got != "") != tc.allowed {
			t.Fatalf("URL %q registered %v: got %q", tc.url, tc.registered, got)
		}
	}
}
