package relay

import (
	"net/http"
	"strings"
	"testing"
)

func TestNormalizeBareEmail(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		want  string
		ok    bool
	}{
		{name: "ordinary", value: " user@example.com ", want: "user@example.com", ok: true},
		{name: "uppercase", value: "User@Example.COM", want: "User@Example.COM", ok: true},
		{name: "header injection", value: "user@example.com\r\nBcc: victim@example.com"},
		{name: "display name", value: "User <user@example.com>"},
		{name: "empty", value: "  "},
		{name: "invalid", value: "not-an-email"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeBareEmail(test.value)
			if (err == nil) != test.ok || got != test.want {
				t.Fatalf("normalizeBareEmail(%q) = %q, %v; want %q, ok=%v", test.value, got, err, test.want, test.ok)
			}
		})
	}
}

func TestSMTPHeaderValueRemovesControls(t *testing.T) {
	got := smtpHeaderValue("team\r\nBcc: victim@example.com\tend")
	if strings.ContainsAny(got, "\r\n\t") {
		t.Fatalf("sanitized header still contains controls: %q", got)
	}
	if got != "team  Bcc: victim@example.com end" {
		t.Fatalf("sanitized header = %q", got)
	}
}

func TestOrgInviteRejectsHeaderInjectionBeforeMutation(t *testing.T) {
	store, server, client, userID := planTestClient(t, ServerConfig{})
	mustTest(t, store.CreateOrg("safe-mail-org", "Safe Mail Org", "safe-mail-org", userID))

	response, err := client.Post(
		server.URL+"/api/orgs/safe-mail-org/invite",
		"application/json",
		strings.NewReader(`{"emails":["user@example.com\\r\\nBcc: victim@example.com"]}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestBody(t, response.Body)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("invite status = %d, want 400", response.StatusCode)
	}
	invites, err := store.ListPendingInvites("safe-mail-org")
	if err != nil {
		t.Fatal(err)
	}
	if len(invites) != 0 {
		t.Fatalf("invalid invite mutated store: %#v", invites)
	}
}
