package egg

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

func TestSessionAuthorityCapturesRuntimePolicyWithoutContent(t *testing.T) {
	dir := t.TempDir()
	p := PreparedLaunch{Sandboxed: true, Environment: map[string]string{"TOKEN": "SECRET_TOKEN"},
		Args: []string{"PRIVATE_PROMPT"}, ProfileDirs: []string{"/home/provider/.claude"},
		Sandbox: sandbox.Config{NetworkNeed: sandbox.NetworkHTTPS, Domains: []string{"custom.provider.example"},
			Mounts: []sandbox.Mount{{Source: "/work", Target: "/work"}, {Source: "/home/provider/.claude", UseRegex: true}},
			Deny:   []string{"/home/provider/.ssh"}, DenyRename: []string{"/home/provider"},
			ProtectedWriteTargets: []string{"/state"}, LocalPorts: []int{1234}}}
	a := sessionAuthority(RunConfig{CWD: "/work"}, p, "/home/provider")
	if err := writeSessionAuthority(dir, a); err != nil {
		t.Fatal(err)
	}
	if got := ReadSessionAuthority(dir); got == nil || !reflect.DeepEqual(*got, a) {
		t.Fatalf("runtime policy lost: %+v", got)
	}
	data, _ := os.ReadFile(filepath.Join(dir, SessionAuthorityFile))
	for _, secret := range []string{"SECRET_TOKEN", "PRIVATE_PROMPT", "TOKEN"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("authority projection exposed %s", secret)
		}
	}
	info, _ := os.Stat(filepath.Join(dir, SessionAuthorityFile))
	if info.Mode().Perm() != 0600 {
		t.Fatal("policy metadata is not private")
	}
	// An outer-boundary launch must not advertise unenforced mount/deny rules.
	p.Sandboxed = false
	outer := sessionAuthority(RunConfig{CWD: "/work"}, p, "/home/provider")
	if outer.Network != "unrestricted" || len(outer.Mounts)+len(outer.Denied)+len(outer.ProtectedWrites) != 0 {
		t.Fatalf("outer boundary claims confinement: %+v", outer)
	}
}

func TestSessionAuthorityRefusesMissingMalformedAndLinkedEvidence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, SessionAuthorityFile)
	if ReadSessionAuthority(dir) != nil {
		t.Fatal("missing policy was reconstructed")
	}
	for _, content := range []string{"{", `{"version":2,"platform":"linux","workspace":"/work","network":"none"}`, strings.Repeat("x", (64<<10)+1)} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if ReadSessionAuthority(dir) != nil {
			t.Fatal("invalid evidence accepted")
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(target, []byte(`{"version":1,"platform":"linux","workspace":"/work","network":"none"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if ReadSessionAuthority(dir) != nil {
		t.Fatal("linked evidence accepted")
	}
}
