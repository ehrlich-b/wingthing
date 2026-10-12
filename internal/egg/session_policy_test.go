package egg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

func TestSessionPolicyReceiptRetainsAdmissionAuthority(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "original-session")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	rc := RunConfig{CWD: "/workspace", AgentArgs: []string{"PRIVATE_PROMPT"}, Env: map[string]string{"TOKEN": "PRIVATE_TOKEN"}, LaunchPolicy: &LaunchPolicySelection{Path: "/policy/egg.yaml"}}
	prepared := PreparedLaunch{Sandboxed: true, Sandbox: sandbox.Config{UserHome: "/provider", Mounts: []sandbox.Mount{{Source: "/workspace", Target: "/workspace"}}, Deny: []string{"/provider/.ssh"}, NetworkNeed: sandbox.NetworkHTTPS, Domains: []string{"provider.example"}}}
	if err := SaveSessionPolicy(dir, rc, prepared); err != nil {
		t.Fatal(err)
	}
	p := ReadSessionPolicy(dir)
	if p == nil || p.Workspace != rc.CWD || p.ProviderHome != "/provider" || p.Source != "/policy/egg.yaml" || !p.Sandboxed || len(p.Denied) != 1 || p.Domains[0] != "provider.example" {
		t.Fatalf("receipt: %+v", p)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "session-policy.json"))
	if strings.Contains(string(data), "PRIVATE_") {
		t.Fatal("policy exported launch content")
	}
	info, _ := os.Stat(filepath.Join(dir, "session-policy.json"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("policy receipt is not private")
	}
	// The receipt remains the launch snapshot even if the caller mutates its config.
	prepared.Sandbox.Domains[0] = "changed.example"
	if ReadSessionPolicy(dir).Domains[0] != "provider.example" {
		t.Fatal("receipt was not captured at admission")
	}
	p.SessionID = "replacement"
	data, _ = json.Marshal(p)
	if err := os.WriteFile(filepath.Join(dir, "session-policy.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if ReadSessionPolicy(dir) != nil {
		t.Fatal("foreign session receipt accepted")
	}
}

func TestSessionPolicyUnavailableIsUnknown(t *testing.T) {
	dir := t.TempDir()
	if ReadSessionPolicy(dir) != nil {
		t.Fatal("invented a policy")
	}
	path := filepath.Join(dir, "session-policy.json")
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if ReadSessionPolicy(dir) != nil {
		t.Fatal("corrupt receipt accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/dev/zero", path); err != nil {
		t.Fatal(err)
	}
	if ReadSessionPolicy(dir) != nil {
		t.Fatal("symlink receipt accepted")
	}
}
