package dashboard

import (
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
)

func TestDetailsDistinguishMissingSavedAndPrivilegedAuthority(t *testing.T) {
	r := row{LocalSession: eggclient.LocalSession{ID: "one", CWD: "/remote/work", Isolation: "fixture"}, machine: "forge"}
	text := strings.Join(authorityLines(r), "\n")
	if !strings.Contains(text, "access not reported") || !strings.Contains(text, "Network: not reported") || strings.Contains(text, "read/write") {
		t.Fatalf("invented authority for legacy/fixture session: %s", text)
	}
	r.Authority = &egg.SessionAuthority{Version: 1, Platform: "linux", Workspace: r.CWD, ProviderHome: "/remote/home", Sandboxed: true,
		Network: "https", Domains: []string{"provider.example"}, Denied: []string{"/remote/home/.ssh"},
		Mounts: []egg.AuthorityMount{{Path: r.CWD}, {Path: "/remote/data", ReadOnly: true}}}
	text = strings.Join(authorityLines(r), "\n")
	for _, want := range []string{"read/write /remote/work", "read-only /remote/data", "Provider state: /remote/home", "provider.example", "read/write: /remote/home/.ssh", "does not verify native confinement"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s in %s", want, text)
		}
	}
	r.Authority.Workspace = "/other"
	if text = strings.Join(authorityLines(r), "\n"); !strings.Contains(text, "unavailable") || strings.Contains(text, "provider.example") {
		t.Fatal("mismatched policy attributed to selected session")
	}
	r.Authority.Workspace, r.Authority.Sandboxed = r.CWD, false
	if text = strings.Join(authorityLines(r), "\n"); !strings.Contains(text, "full OS-user authority") || strings.Contains(text, "provider.example") {
		t.Fatal("outer boundary displayed sandbox restrictions")
	}
}

func TestAuthorityMetadataCannotInjectTerminalControls(t *testing.T) {
	s := fixture()
	s.mode, s.target = details, s.rows()[0]
	s.target.Authority = &egg.SessionAuthority{Version: 1, Workspace: s.target.CWD, Sandboxed: true,
		ProviderHome: "host\x1b]52;clipboard\a", Network: "none", Denied: []string{"secret\n\x1b[2J"}}
	renderedLines(t, render(s, 80, 40, time.Now()), 80, 40)
}
