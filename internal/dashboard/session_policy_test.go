package dashboard

import (
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

func TestPolicyDetailsDistinguishUnknownSandboxAndPrivileged(t *testing.T) {
	r := row{LocalSession: eggclient.LocalSession{CWD: "/workspace"}}
	unknown := strings.Join(sessionPolicyLines(r), "\n")
	if !strings.Contains(unknown, "permissions unverified") || !strings.Contains(unknown, "Denied: not reported") {
		t.Fatal(unknown)
	}
	r.Policy = &egg.SessionPolicy{Version: 1, Workspace: "/workspace", ProviderHome: "/provider", Sandboxed: true, Mounts: []sandbox.Mount{{Source: "/tool", ReadOnly: true}}, Denied: []string{"/private"}, Network: "https", NetworkMode: "observe"}
	text := strings.Join(sessionPolicyLines(r), "\n")
	for _, want := range []string{"Provider state: /provider", "read-only /tool", "Denied: /private", "domain decisions: observe", "requires host verification"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
	r.Policy.Sandboxed = false
	text = strings.Join(sessionPolicyLines(r), "\n")
	if !strings.Contains(text, "full OS-user authority") || strings.Contains(text, "Denied: /private") {
		t.Fatal(text)
	}
	r.Policy.SessionID = "replacement"
	if text := strings.Join(sessionPolicyLines(r), "\n"); !strings.Contains(text, "unavailable") || strings.Contains(text, "full OS-user authority") {
		t.Fatal("foreign receipt was presented as this session's authority", text)
	}
}
