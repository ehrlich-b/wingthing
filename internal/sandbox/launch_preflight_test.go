package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLaunchPlanDomainPolicyMatcher(t *testing.T) {
	for _, tc := range []struct {
		domains []string
		host    string
		allowed bool
	}{
		{[]string{"API.OPENAI.COM."}, "api.openai.com:443", true},
		{[]string{"*.openai.com"}, "api.openai.com:443", true},
		{[]string{"*.openai.com"}, "openai.com:443", false},
		{[]string{"*.openai.com"}, "api.openai.com.evil.example:443", false},
		{[]string{"unrelated.example"}, "api.openai.com:443", false},
		{[]string{"*"}, "api.openai.com:443", true},
	} {
		if got := DomainAllowed(tc.domains, tc.host); got != tc.allowed {
			t.Errorf("domains %v, host %s: allowed=%v, want %v", tc.domains, tc.host, got, tc.allowed)
		}
	}
}

func TestLaunchPlanCanonicalPathMissingProviderState(t *testing.T) {
	root := t.TempDir()
	actual := filepath.Join(root, "actual")
	alias := filepath.Join(root, "alias")
	if err := os.Mkdir(actual, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(actual, alias); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(alias, ".codex", "wingthing-events", "launch-plan")
	got, err := CanonicalPath(path)
	if err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(actual)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(real, ".codex", "wingthing-events", "launch-plan"); got != want {
		t.Fatalf("canonical state = %s, want %s", got, want)
	}
	if _, err := os.Stat(filepath.Join(actual, ".codex")); !os.IsNotExist(err) {
		t.Fatal("path canonicalization created state")
	}
}
