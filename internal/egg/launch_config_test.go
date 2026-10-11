package egg

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func launchPolicyFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestLaunchConfigProductionPrecedenceAndLegacy(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("WINGTHING_DIR", filepath.Join(dir, "ambient-wing"))
	for _, tc := range []struct{ name, body string }{
		{"legacy", "network: none\nfs: [rw:./]\n"},
		{"mapping", "network: {domains: [api.example], agent_domains: none}\nfs: [deny:~/private]\n"},
		{"empty-base", "base: none\nnetwork: '*'\nenv: '*'\n"},
		{"override", "fs: [rw:~/.ssh]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := launchPolicyFile(t, dir, tc.name+".yaml", tc.body)
			got, _, err := ResolveLaunchConfig(path, "", dir)
			if err != nil {
				t.Fatal(err)
			}
			want, err := ResolveEggConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("planner/runtime mismatch\ngot: %#v\nwant: %#v", got, want)
			}
		})
	}
}
func TestLaunchConfigExplicitHomeAndNamedBase(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "selected-home")
	t.Setenv("HOME", filepath.Join(dir, "foreign-home"))
	bases := filepath.Join(dir, "bases")
	if err := os.Mkdir(bases, 0700); err != nil {
		t.Fatal(err)
	}
	parent := launchPolicyFile(t, bases, "parent.yaml", "base: none\nfs: [deny:~/private]\nnetwork: {agent_domains: none}\n")
	child := launchPolicyFile(t, dir, "egg.yaml", "base: parent\nfs: [rw:"+filepath.Join(home, "private")+"]\n")
	cfg, rules, err := ResolveLaunchConfig(child, bases, home)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.FS) != 1 || strings.HasPrefix(cfg.FS[0], "deny:") {
		t.Fatalf("explicit-home deny override failed: %v", cfg.FS)
	}
	for _, rule := range rules {
		if rule.Rule == "deny:~/private" {
			t.Fatal("removed parent denial has stale provenance")
		}
	}
	if _, _, err := ResolveLaunchConfig(child, "", home); err == nil {
		t.Fatal("named base used ambient fallback")
	}
	_ = parent
}
func TestLaunchConfigProvenanceMasksDuplicatesMissingAndCycles(t *testing.T) {
	dir := t.TempDir()
	parent := launchPolicyFile(t, dir, "parent.yaml", "base: none\nfs: [deny:./blocked]\nnetwork: {domains: [one.example], agent_domains: none}\n")
	child := launchPolicyFile(t, dir, "child.yaml", "base: {name: ./parent.yaml, network: ./parent.yaml}\nfs: [deny:./blocked]\n")
	cfg, rules, err := ResolveLaunchConfig(child, "", dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.FS) != 2 {
		t.Fatalf("duplicate parent declaration lost: %v", cfg.FS)
	}
	sources := map[string]bool{}
	for _, rule := range rules {
		if rule.Rule == "deny:./blocked" {
			sources[rule.Source] = true
		}
	}
	if !sources[parent] || !sources[child] || len(sources) != 2 {
		t.Fatalf("denial sources: %v", sources)
	}
	masked := launchPolicyFile(t, dir, "masked.yaml", "base: {name: ./parent.yaml, fs: none}\nfs: [rw:./]\n")
	_, rules, err = ResolveLaunchConfig(masked, "", dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range rules {
		if rule.Rule == "deny:./blocked" {
			t.Fatal("masked denial still attributed")
		}
	}
	for _, tc := range []struct{ name, body string }{{"missing", "base: ./absent.yaml\n"}, {"cycle", "base: ./cycle.yaml\n"}, {"malformed", "network: {typo: true}\n"}} {
		path := launchPolicyFile(t, dir, tc.name+".yaml", tc.body)
		if _, _, err := ResolveLaunchConfig(path, "", dir); err == nil {
			t.Fatalf("%s accepted", tc.name)
		}
	}
}
func TestLaunchCommandPreviewDoesNotPrepareState(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "absent-home")
	args, native, err := LaunchCommandPreview("codex", dir, home, "codex-cli 0.159.3", true)
	if err != nil || !native {
		t.Fatalf("%v %v", native, err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("preview created provider state")
	}
	if strings.Contains(strings.Join(args, " "), "--full-auto") {
		t.Fatal("obsolete flag")
	}
	original := []string{"--dangerously-bypass-approvals-and-sandbox"}
	live, native, err := CodexRunArgs(original, home, "launch-plan")
	if err != nil || !native {
		t.Fatal(err)
	}
	live = codexStartupArgs(live, dir)
	if !reflect.DeepEqual(args[1:], live) {
		t.Fatalf("preview/runtime argv differ\n%v\n%v", args[1:], live)
	}
}
