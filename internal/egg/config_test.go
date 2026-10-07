package egg

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	wingconfig "github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
	"gopkg.in/yaml.v3"
)

func writeEggConfigTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func makeEggConfigTestDir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestResolveEggConfigNamedBaseLookup(t *testing.T) {
	for _, tc := range []struct {
		name, channel, current, wantShell string
		wantError                         bool
	}{
		{name: "stable legacy fallback", channel: "stable", wantShell: "/legacy/sh"},
		{name: "stable current preferred", channel: "stable", current: "base: none\nshell: /current/sh\n", wantShell: "/current/sh"},
		{name: "stable invalid current fails", channel: "stable", current: "[invalid yaml", wantError: true},
		{name: "preview no stable fallback", channel: "preview", wantError: true},
		{name: "preview current base", channel: "preview", current: "base: none\nshell: /preview/sh\n", wantShell: "/preview/sh"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := wingconfig.ReleaseChannel
			wingconfig.ReleaseChannel = tc.channel
			t.Cleanup(func() { wingconfig.ReleaseChannel = previous })
			home := t.TempDir()
			t.Setenv("HOME", home)
			state := filepath.Join(home, "alternate-state")
			t.Setenv("WINGTHING_DIR", state)
			t.Setenv("WINGTHING_PREVIEW_DIR", "")
			legacy := filepath.Join(home, ".wingthing", "bases")
			makeEggConfigTestDir(t, legacy)
			writeEggConfigTestFile(t, filepath.Join(legacy, "work.yaml"), "base: none\nshell: /legacy/sh\n")
			if tc.current != "" {
				makeEggConfigTestDir(t, filepath.Join(state, "bases"))
				writeEggConfigTestFile(t, filepath.Join(state, "bases", "work.yaml"), tc.current)
			}
			path := filepath.Join(home, "egg.yaml")
			writeEggConfigTestFile(t, path, "base: work\n")
			cfg, err := ResolveEggConfig(path)
			if tc.wantError {
				if err == nil {
					t.Fatal("expected base lookup to fail")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Shell != tc.wantShell {
				t.Fatalf("shell = %q, want %q", cfg.Shell, tc.wantShell)
			}
		})
	}
}

func TestDefaultEggConfig(t *testing.T) {
	cfg := DefaultEggConfig()
	if len(cfg.FS) == 0 {
		t.Fatal("default config should have FS rules")
	}
	// Should have ro:/ then rw:./ and deny paths
	if cfg.FS[0] != "ro:/" {
		t.Errorf("first FS rule = %q, want ro:/", cfg.FS[0])
	}
	if cfg.FS[1] != "rw:./" {
		t.Errorf("second FS rule = %q, want rw:./", cfg.FS[1])
	}
	// Should include deny-write:./egg.yaml
	found := false
	for _, entry := range cfg.FS {
		if entry == "deny-write:./egg.yaml" {
			found = true
		}
	}
	if !found {
		t.Error("default config missing deny-write:./egg.yaml")
	}
}

func TestMergeEggConfig_NoBase(t *testing.T) {
	// Child with no base merges on top of built-in default
	child := &EggConfig{
		FS:      []string{"ro:~/.ssh"},
		Network: NetworkField{Domains: []string{"api.anthropic.com"}},
	}
	parent := DefaultEggConfig()
	merged := MergeEggConfig(parent, child)

	// Parent deny paths should remain (minus overridden ones)
	hasDenySSH := false
	hasROSSH := false
	for _, entry := range merged.FS {
		if entry == "deny:~/.ssh" {
			hasDenySSH = true
		}
		if entry == "ro:~/.ssh" {
			hasROSSH = true
		}
	}
	if hasDenySSH {
		t.Error("parent deny:~/.ssh should be removed when child has ro:~/.ssh")
	}
	if !hasROSSH {
		t.Error("child ro:~/.ssh should be in merged config")
	}
	// Other deny paths should survive
	hasOtherDeny := false
	for _, entry := range merged.FS {
		if entry == "deny:~/.gnupg" {
			hasOtherDeny = true
		}
	}
	if !hasOtherDeny {
		t.Error("parent deny:~/.gnupg should survive merge")
	}
	// Network should include child's domains
	if len(merged.Network.Domains) != 1 || merged.Network.Domains[0] != "api.anthropic.com" {
		t.Errorf("network = %v, want [api.anthropic.com]", merged.Network)
	}
}

func TestMergeEggConfig_BaseNone(t *testing.T) {
	// base: none means empty slate
	dir := t.TempDir()
	path := filepath.Join(dir, "egg.yaml")
	writeEggConfigTestFile(t, path, `base: none
fs:
  - rw:./
  - ro:~/.ssh
`)

	cfg, err := ResolveEggConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	// Should only have what's in the file — no default deny paths
	for _, entry := range cfg.FS {
		if strings.HasPrefix(entry, "deny:") {
			t.Errorf("base:none should not have deny entries, got %s", entry)
		}
	}
	if mounts, _, denied := ParseFSRules(cfg.FS, ""); len(mounts) != 2 || len(denied) != 1 {
		t.Errorf("expected 2 FS rules plus policy protection, got %d: %v", len(cfg.FS), cfg.FS)
	}
}

func TestMergeEggConfig_NamedBase(t *testing.T) {
	// Create a named base in a temp dir
	home := t.TempDir()
	t.Setenv("HOME", home)

	basesDir := filepath.Join(home, ".wingthing", "bases")
	makeEggConfigTestDir(t, basesDir)
	writeEggConfigTestFile(t, filepath.Join(basesDir, "strict.yaml"), `base: none
fs:
  - rw:./
  - deny:~/.ssh
  - deny:~/.aws
network: none
`)

	// Project config references the named base
	dir := t.TempDir()
	path := filepath.Join(dir, "egg.yaml")
	writeEggConfigTestFile(t, path, `base: strict
fs:
  - ro:~/.ssh
network:
  - api.anthropic.com
`)

	cfg, err := ResolveEggConfig(path)
	if err != nil {
		t.Fatal(err)
	}

	// deny:~/.ssh should be removed because child has ro:~/.ssh
	for _, entry := range cfg.FS {
		if entry == "deny:~/.ssh" {
			t.Error("child ro:~/.ssh should override parent deny:~/.ssh")
		}
	}
	// deny:~/.aws should survive
	hasAWS := false
	for _, entry := range cfg.FS {
		if entry == "deny:~/.aws" {
			hasAWS = true
		}
	}
	if !hasAWS {
		t.Error("parent deny:~/.aws should survive merge")
	}
	// Network should be union
	if len(cfg.Network.Domains) != 1 || cfg.Network.Domains[0] != "api.anthropic.com" {
		t.Errorf("network = %v, want [api.anthropic.com]", cfg.Network)
	}
}

func TestMergeEggConfig_RelativePath(t *testing.T) {
	dir := t.TempDir()

	// Create a base config in a subdirectory
	basesDir := filepath.Join(dir, "bases")
	makeEggConfigTestDir(t, basesDir)
	writeEggConfigTestFile(t, filepath.Join(basesDir, "ci.yaml"), `base: none
fs:
  - rw:./
`)

	// Project config with relative base path
	path := filepath.Join(dir, "egg.yaml")
	writeEggConfigTestFile(t, path, `base: ./bases/ci.yaml
fs:
  - deny:~/.ssh
`)

	cfg, err := ResolveEggConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if mounts, _, denied := ParseFSRules(cfg.FS, ""); len(mounts) != 1 || len(denied) != 2 {
		t.Errorf("expected filesystem grants plus 2 protected policies, got %d: %v", len(cfg.FS), cfg.FS)
	}
}

func TestResolveEggConfig_CircularReference(t *testing.T) {
	dir := t.TempDir()

	// a.yaml -> b.yaml -> a.yaml (circular)
	writeEggConfigTestFile(t, filepath.Join(dir, "a.yaml"), "base: ./b.yaml\n")
	writeEggConfigTestFile(t, filepath.Join(dir, "b.yaml"), "base: ./a.yaml\n")

	_, err := ResolveEggConfig(filepath.Join(dir, "a.yaml"))
	if err == nil {
		t.Fatal("expected circular reference error")
	}
	if !strings.Contains(err.Error(), "circular") {
		t.Errorf("error = %q, want circular reference error", err)
	}
}

func TestResolveEggConfig_MaxDepth(t *testing.T) {
	dir := t.TempDir()

	// Create a chain deeper than maxBaseDepth
	for i := 0; i <= maxBaseDepth+1; i++ {
		name := filepath.Join(dir, "level"+string(rune('a'+i))+".yaml")
		if i <= maxBaseDepth {
			next := filepath.Join(dir, "level"+string(rune('a'+i+1))+".yaml")
			writeEggConfigTestFile(t, name, "base: "+next+"\n")
		} else {
			writeEggConfigTestFile(t, name, "base: none\n")
		}
	}

	_, err := ResolveEggConfig(filepath.Join(dir, "levela.yaml"))
	if err == nil {
		t.Fatal("expected max depth error")
	}
	if !strings.Contains(err.Error(), "too deep") {
		t.Errorf("error = %q, want too deep error", err)
	}
}

func TestMergeEggConfig_NetworkUnion(t *testing.T) {
	parent := &EggConfig{Network: NetworkField{Domains: []string{"api.anthropic.com"}, AgentDomains: "merge"}}
	child := &EggConfig{Network: NetworkField{Domains: []string{"api.openai.com"}, AgentDomains: "none"}}
	merged := MergeEggConfig(parent, child)
	if len(merged.Network.Domains) != 2 {
		t.Errorf("network = %v, want 2 domains", merged.Network)
	}
	if merged.Network.AgentDomains != "none" {
		t.Errorf("agent_domains = %q, want child value none", merged.Network.AgentDomains)
	}

	// Wildcard in either -> wildcard
	parent2 := &EggConfig{Network: NetworkField{Domains: []string{"*"}}}
	child2 := &EggConfig{Network: NetworkField{Domains: []string{"api.openai.com"}}}
	merged2 := MergeEggConfig(parent2, child2)
	if len(merged2.Network.Domains) != 1 || merged2.Network.Domains[0] != "*" {
		t.Errorf("network = %v, want [*]", merged2.Network)
	}
}

func TestMergeEggConfig_EnvUnion(t *testing.T) {
	parent := &EggConfig{Env: EnvField{"ANTHROPIC_API_KEY"}}
	child := &EggConfig{Env: EnvField{"OPENAI_API_KEY"}}
	merged := MergeEggConfig(parent, child)
	if len(merged.Env) != 2 {
		t.Errorf("env = %v, want 2 vars", merged.Env)
	}

	// Wildcard
	parent2 := &EggConfig{Env: EnvField{"ANTHROPIC_API_KEY"}}
	child2 := &EggConfig{Env: EnvField{"*"}}
	merged2 := MergeEggConfig(parent2, child2)
	if len(merged2.Env) != 1 || merged2.Env[0] != "*" {
		t.Errorf("env = %v, want [*]", merged2.Env)
	}
}

func TestMergeEggConfig_ResourcesOverride(t *testing.T) {
	parent := &EggConfig{Resources: EggResources{CPU: "300s", Memory: "2GB", MaxFDs: 1024}}
	child := &EggConfig{Resources: EggResources{Memory: "4GB"}}
	merged := MergeEggConfig(parent, child)
	if merged.Resources.CPU != "300s" {
		t.Errorf("CPU = %q, want 300s (from parent)", merged.Resources.CPU)
	}
	if merged.Resources.Memory != "4GB" {
		t.Errorf("Memory = %q, want 4GB (from child)", merged.Resources.Memory)
	}
	if merged.Resources.MaxFDs != 1024 {
		t.Errorf("MaxFDs = %d, want 1024 (from parent)", merged.Resources.MaxFDs)
	}
}

func TestMergeEggConfig_ShellOverride(t *testing.T) {
	parent := &EggConfig{Shell: "/bin/bash"}
	child := &EggConfig{}
	merged := MergeEggConfig(parent, child)
	if merged.Shell != "/bin/bash" {
		t.Errorf("Shell = %q, want /bin/bash (from parent)", merged.Shell)
	}

	child2 := &EggConfig{Shell: "/bin/zsh"}
	merged2 := MergeEggConfig(parent, child2)
	if merged2.Shell != "/bin/zsh" {
		t.Errorf("Shell = %q, want /bin/zsh (from child)", merged2.Shell)
	}
}

func TestMergeEggConfig_DangerouslySkipPermissionsOR(t *testing.T) {
	parent := &EggConfig{DangerouslySkipPermissions: true}
	child := &EggConfig{DangerouslySkipPermissions: false}
	merged := MergeEggConfig(parent, child)
	if !merged.DangerouslySkipPermissions {
		t.Error("DangerouslySkipPermissions should be OR (parent=true)")
	}
}

func TestParseFSRules_DenyWrite(t *testing.T) {
	home := "/Users/test"
	fs := []string{"rw:./", "deny:~/.ssh", "deny-write:./egg.yaml"}
	mounts, deny, denyWrite := ParseFSRules(fs, home)
	if len(mounts) != 1 {
		t.Errorf("mounts = %d, want 1", len(mounts))
	}
	if len(deny) != 1 || deny[0] != home+"/.ssh" {
		t.Errorf("deny = %v, want [%s/.ssh]", deny, home)
	}
	if len(denyWrite) != 1 || denyWrite[0] != "./egg.yaml" {
		t.Errorf("denyWrite = %v, want [./egg.yaml]", denyWrite)
	}
}

func TestResolveEggConfig_NoBase_MergesDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "egg.yaml")
	writeEggConfigTestFile(t, path, `fs:
  - ro:~/.ssh
`)

	cfg, err := ResolveEggConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	// Should have default deny paths preserved (except ~/.ssh which is overridden)
	hasGnupg := false
	for _, entry := range cfg.FS {
		if entry == "deny:~/.gnupg" {
			hasGnupg = true
		}
	}
	if !hasGnupg {
		t.Error("no base: should merge with default, preserving deny:~/.gnupg")
	}
}

func TestResolveEggConfig_FileNotFound(t *testing.T) {
	_, err := ResolveEggConfig("/nonexistent/egg.yaml")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestResolvedPoliciesDenyWriteEveryDependency(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("WINGTHING_DIR", filepath.Join(home, "state"))
	bases := filepath.Join(home, "state", "bases")
	makeEggConfigTestDir(t, bases)
	project := t.TempDir()
	base := filepath.Join(project, "base.yaml")
	named := filepath.Join(bases, "team.yaml")
	env := filepath.Join(project, "env.yaml")
	network := filepath.Join(project, "network.yaml")
	policy := filepath.Join(project, "egg.yaml")
	writeEggConfigTestFile(t, base, "base: none\nfs: [rw:./]\n")
	writeEggConfigTestFile(t, named, "base: "+base+"\n")
	writeEggConfigTestFile(t, env, "base: none\nenv: [HOME]\n")
	writeEggConfigTestFile(t, network, "base: none\nnetwork: [example.com]\n")
	writeEggConfigTestFile(t, policy, "base:\n  name: team\n  fs: none\n  env: ./env.yaml\n  network: ./network.yaml\nfs: [rw:"+base+"]\n")
	cfg, err := ResolveEggConfig(policy)
	if err != nil {
		t.Fatal(err)
	}
	_, _, denied := ParseFSRules(cfg.FS, home)
	for _, dependency := range []string{policy, base, named, env, network} {
		found := false
		for _, path := range denied {
			if path == canonicalPolicyTestPath(t, dependency) {
				found = true
			}
		}
		if !found {
			t.Errorf("dependency %s can be overwritten: %v", dependency, denied)
		}
	}
}

func TestDiscoveredGlobalPolicyRefusesReplaceableSymlink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	state := filepath.Join(home, "state")
	t.Setenv("WINGTHING_DIR", state)
	makeEggConfigTestDir(t, state)
	t.Chdir(t.TempDir())
	base := filepath.Join(home, "base.yaml")
	alias := filepath.Join(state, "alias.yaml")
	policy := filepath.Join(state, "egg.yaml")
	writeEggConfigTestFile(t, base, "fs: [rw:./]\n")
	if err := os.Symlink(base, alias); err != nil {
		t.Fatal(err)
	}
	writeEggConfigTestFile(t, policy, "base: ./alias.yaml\n")
	cfg := DiscoverEggConfig(home, nil)
	if !isUnsafePolicyPath(cfg.ResolutionError()) {
		t.Fatalf("replaceable global policy alias admitted: %v", cfg.ResolutionError())
	}
	if _, err := cfg.YAML(); !isUnsafePolicyPath(err) {
		t.Fatalf("security refusal lost during serialization: %v", err)
	}
	if _, err := ResolvePolicyWithProvider(cfg, "claude", home, ""); !isUnsafePolicyPath(err) {
		t.Fatalf("security refusal lost during policy resolution: %v", err)
	}
}

func TestDefaultPolicyRefusesReplaceableToolLoaderPaths(t *testing.T) {
	for _, kind := range []string{"directory", "ancestor", "missing", "dotdot", "definition", "chain", "wing config"} {
		t.Run(kind, func(t *testing.T) {
			home := canonicalPolicyTestPath(t, t.TempDir())
			t.Setenv("HOME", home)
			state := filepath.Join(home, ".wingthing")
			t.Setenv("WINGTHING_DIR", state)
			makeEggConfigTestDir(t, state)
			work := filepath.Join(home, "work")
			makeEggConfigTestDir(t, work)
			t.Chdir(work)
			real := filepath.Join(home, "host-tools")
			makeEggConfigTestDir(t, real)
			definition := filepath.Join(real, "host.yaml")
			writeEggConfigTestFile(t, definition, "name: host\nrun: true\n")
			toolsDir := real
			link := filepath.Join(work, "tools")
			switch kind {
			case "directory", "ancestor", "missing", "dotdot":
				if err := os.Symlink(real, link); err != nil {
					t.Fatal(err)
				}
				toolsDir = link
				if kind == "dotdot" {
					toolsDir = link + "/../host-tools"
				} else if kind != "directory" {
					toolsDir = filepath.Join(link, "nested")
					if kind == "ancestor" {
						makeEggConfigTestDir(t, filepath.Join(real, "nested"))
					}
				}
			case "definition", "chain":
				toolsDir = filepath.Join(work, "definitions")
				makeEggConfigTestDir(t, toolsDir)
				if kind == "chain" {
					link = filepath.Join(real, "chain.yaml")
					if err := os.Symlink(definition, link); err != nil {
						t.Fatal(err)
					}
					definition = link
				}
				if err := os.Symlink(definition, filepath.Join(toolsDir, "host.yaml")); err != nil {
					t.Fatal(err)
				}
			}
			wingPath := filepath.Join(state, "wing.yaml")
			if kind == "wing config" {
				wingSource := filepath.Join(work, "wing.yaml")
				writeEggConfigTestFile(t, wingSource, "tools_dir: "+toolsDir+"\n")
				if err := os.Symlink(wingSource, wingPath); err != nil {
					t.Fatal(err)
				}
			} else {
				writeEggConfigTestFile(t, wingPath, "tools_dir: "+toolsDir+"\n")
			}
			cfg := DiscoverEggConfig(work, nil)
			if !isUnsafePolicyPath(cfg.ResolutionError()) {
				t.Fatalf("replaceable tool %s admitted: %v", kind, cfg.ResolutionError())
			}
			if _, err := cfg.YAML(); !isUnsafePolicyPath(err) {
				t.Fatalf("tool loader refusal lost during serialization: %v", err)
			}
			policy := filepath.Join(work, "egg.yaml")
			writeEggConfigTestFile(t, policy, "fs: [rw:./]\n")
			if _, err := ResolveEggConfig(policy, work); !isUnsafePolicyPath(err) {
				t.Fatalf("inherited tool loader refusal lost: %v", err)
			}
		})
	}
}

func TestLoaderPathsRejectHardLinkAliases(t *testing.T) {
	for _, kind := range []string{"tool yaml", "tool yml", "tool symlink", "wing config", "clients config", "remotes config", "legacy config", "global policy", "project policy", "base policy", "section policy"} {
		t.Run(kind, func(t *testing.T) {
			home := canonicalPolicyTestPath(t, t.TempDir())
			t.Setenv("HOME", home)
			state := filepath.Join(home, ".wingthing")
			t.Setenv("WINGTHING_DIR", state)
			work := filepath.Join(home, "workspace")
			tools := filepath.Join(state, "tools")
			for _, dir := range []string{work, tools} {
				makeEggConfigTestDir(t, dir)
			}
			t.Chdir(work)
			loader := filepath.Join(tools, "host.yaml")
			body := "name: host\nrun: /bin/true\n"
			switch kind {
			case "tool yml":
				loader = filepath.Join(tools, "host.yml")
			case "tool symlink":
				loader = filepath.Join(home, "host.yaml")
				if err := os.Symlink(loader, filepath.Join(tools, "host.yaml")); err != nil {
					t.Fatal(err)
				}
			case "wing config":
				loader, body = filepath.Join(state, "wing.yaml"), "tools_dir: tools\n"
			case "clients config":
				loader, body = filepath.Join(state, "clients.yaml"), "require_client: true\n"
			case "remotes config":
				loader, body = filepath.Join(state, "remotes.yaml"), "{}\n"
			case "legacy config":
				loader, body = filepath.Join(state, "config.yaml"), "{}\n"
			case "global policy":
				loader, body = filepath.Join(state, "egg.yaml"), "fs: [rw:./]\n"
			case "project policy":
				loader, body = filepath.Join(work, "egg.yaml"), "base: none\nfs: [rw:./]\n"
			case "base policy", "section policy":
				loader, body = filepath.Join(home, "base.yaml"), "base: none\nfs: [rw:./]\n"
				base := "base: " + loader + "\n"
				if kind == "section policy" {
					base = "base:\n  fs: " + loader + "\n"
				}
				writeEggConfigTestFile(t, filepath.Join(work, "egg.yaml"), base)
			}
			writeEggConfigTestFile(t, loader, body)
			alias := filepath.Join(work, "writable-alias.yaml")
			if err := os.Link(loader, alias); err != nil {
				t.Fatal(err)
			}
			// This is a regular-file alias, not a symlink. Path masks cannot
			// stop writes through the workspace's second name for the inode.
			original, err := os.Stat(loader)
			if err != nil {
				t.Fatal(err)
			}
			linked, err := os.Lstat(alias)
			if err != nil || !linked.Mode().IsRegular() || !os.SameFile(original, linked) {
				t.Fatalf("invalid hard-link fixture: %v", err)
			}
			cfg := DiscoverEggConfig(work, nil)
			if err := cfg.ResolutionError(); !isUnsafePolicyPath(err) || !strings.Contains(err.Error(), "hard links") {
				t.Fatalf("writable hard-link alias admitted: %v", err)
			}
			if _, err := cfg.YAML(); !isUnsafePolicyPath(err) {
				t.Fatalf("hard-link refusal lost during serialization: %v", err)
			}
			if _, err := ResolvePolicyWithProvider(cfg, "codex", home, ""); !isUnsafePolicyPath(err) {
				t.Fatalf("hard-link refusal lost during policy resolution: %v", err)
			}
			// Removing the alias restores the supported one-link layout.
			if err := os.Remove(alias); err != nil {
				t.Fatal(err)
			}
			if err := DiscoverEggConfig(work, nil).ResolutionError(); err != nil {
				t.Fatalf("one-link loader refused: %v", err)
			}
		})
	}
}

func TestToolLoaderSealsResolvedDirectory(t *testing.T) {
	home := t.TempDir() // includes the immutable /var alias on macOS
	t.Setenv("HOME", home)
	state := filepath.Join(home, ".wingthing")
	t.Setenv("WINGTHING_DIR", state)
	tools := filepath.Join(home, "workspace", "tools")
	makeEggConfigTestDir(t, state)
	makeEggConfigTestDir(t, tools)
	writeEggConfigTestFile(t, filepath.Join(state, "wing.yaml"), "tools_dir: "+tools+"\n")
	cfg := DefaultEggConfig()
	if err := cfg.ResolutionError(); err != nil {
		t.Fatal(err)
	}
	_, deny, _ := ParseFSRules(cfg.FS, home)
	resolved := canonicalPolicyTestPath(t, tools)
	if !containsString(deny, resolved) {
		t.Fatalf("resolved tools directory remains accessible: %v", deny)
	}
	mounts, err := isolateLinuxEggControl([]sandbox.Mount{{Source: filepath.Dir(tools)}}, deny, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, mount := range mounts {
		if controlPathWithin(resolved, mount.Source) {
			t.Fatalf("resolved tools directory remains mounted: %+v", mount)
		}
	}
}

func TestUserManagedLoaderAliasesFollowSandboxWriteGrants(t *testing.T) {
	for _, loaderName := range []string{"wing.yaml", "egg.yaml"} {
		for _, writable := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/writable=%t", loaderName, writable), func(t *testing.T) {
				home := canonicalPolicyTestPath(t, t.TempDir())
				t.Setenv("HOME", home)
				state, dotfiles, work := filepath.Join(home, ".wingthing"), filepath.Join(home, "dotfiles"), filepath.Join(home, "work")
				for _, dir := range []string{state, dotfiles, work} {
					makeEggConfigTestDir(t, dir)
				}
				t.Setenv("WINGTHING_DIR", state)
				t.Chdir(work)
				target, loader := filepath.Join(dotfiles, loaderName), filepath.Join(state, loaderName)
				writeEggConfigTestFile(t, target, "fs: [rw:./]\n")
				if err := os.Symlink(target, loader); err != nil {
					t.Fatal(err)
				}
				if writable {
					t.Chdir(dotfiles)
				}
				cwd, err := os.Getwd()
				if err != nil {
					t.Fatal(err)
				}
				cfg := DiscoverEggConfig(cwd, DefaultEggConfig())
				if writable {
					if !isUnsafePolicyPath(cfg.ResolutionError()) {
						t.Fatalf("agent-writable loader target admitted: %v", cfg.ResolutionError())
					}
					return
				}
				if err := cfg.ResolutionError(); err != nil {
					t.Fatalf("read-only dotfiles layout refused: %v", err)
				}
				_, deny, _ := ParseFSRules(cfg.FS, home)
				if !containsString(deny, loader) || !containsString(deny, target) {
					t.Fatalf("controller loader alias was not sealed: %v", deny)
				}
				if loaderName == "egg.yaml" {
					loaded, err := ResolveEggConfig(loader)
					if err != nil {
						t.Fatal(err)
					}
					_, _, denied := ParseFSRules(loaded.FS, home)
					if !containsString(denied, target) {
						t.Fatalf("resolved policy file remains writable: %v", denied)
					}
				}
				// Later grants must not make an already admitted alias unsafe.
				mounts := []sandbox.Mount{{Source: work}, {Source: dotfiles}}
				if _, err := eggControlDenyPaths("", mounts); !isUnsafePolicyPath(err) {
					t.Fatalf("runtime write grant bypassed alias guard: %v", err)
				}
			})
		}
	}
}

func TestLoaderAliasRejectsAgentProfileWriteGrant(t *testing.T) {
	home := canonicalPolicyTestPath(t, t.TempDir())
	t.Setenv("HOME", home)
	state, codex, work := filepath.Join(home, ".wingthing"), filepath.Join(home, ".codex"), filepath.Join(home, "work")
	for _, dir := range []string{state, codex, work} {
		makeEggConfigTestDir(t, dir)
	}
	t.Setenv("WINGTHING_DIR", state)
	t.Chdir(work)
	target := filepath.Join(codex, "wing.yaml")
	writeEggConfigTestFile(t, target, "{}\n")
	if err := os.Symlink(target, filepath.Join(state, "wing.yaml")); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultEggConfig()
	if err := cfg.ResolutionError(); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolvePolicyWithProvider(cfg, "codex", home, ""); !isUnsafePolicyPath(err) {
		t.Fatalf("agent profile reopened a loader alias: %v", err)
	}
}

func TestResolvedPoliciesRefuseReplaceableSymlinkComponents(t *testing.T) {
	for _, kind := range []string{"file", "directory", "chain", "section", "root"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			outside := t.TempDir()
			base := filepath.Join(outside, "base.yaml")
			writeEggConfigTestFile(t, base, "base: none\nfs: [rw:./]\n")
			alias := filepath.Join(root, "alias.yaml")
			target := base
			if kind == "directory" {
				target = outside
			}
			if kind == "chain" {
				link := filepath.Join(outside, "link.yaml")
				if err := os.Symlink(base, link); err != nil {
					t.Fatal(err)
				}
				target = link
			}
			if err := os.Symlink(target, alias); err != nil {
				t.Fatal(err)
			}
			ref := "./alias.yaml"
			if kind == "directory" {
				ref += "/base.yaml"
			}
			policy := filepath.Join(root, "egg.yaml")
			body := "base: " + ref + "\n"
			if kind == "section" {
				body = "base:\n  fs: " + ref + "\n"
			}
			writeEggConfigTestFile(t, policy, body)
			if kind == "root" {
				policy = alias
			}
			if _, err := ResolveEggConfig(policy, root); !isUnsafePolicyPath(err) {
				t.Fatalf("replaceable %s alias admitted: %v", kind, err)
			}
		})
	}
}

func TestDiscoverEggConfig_FallsBackToDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := DiscoverEggConfig("/nonexistent", nil)
	if len(cfg.FS) == 0 {
		t.Error("should fall back to default config")
	}
}

func TestDiscoverEggConfig_GlobalDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	globalDir := filepath.Join(home, ".wingthing")
	if err := os.MkdirAll(globalDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(globalDir, "egg.yaml"), []byte("base: none\nnetwork: '*'\nenv: '*'\n"), 0600); err != nil {
		t.Fatal(err)
	}

	cfg := DiscoverEggConfig("/nonexistent", nil)
	if RequiresSandbox(cfg, "claude") {
		t.Fatal("global trusted-host config was not discovered")
	}
}

func TestDiscoverEggConfigCustomStateLegacyFallback(t *testing.T) {
	for _, tc := range []struct {
		name, channel, current, wantDeny string
	}{
		{name: "stable legacy policy", channel: "stable", wantDeny: "/legacy/secrets"},
		{name: "stable current policy", channel: "stable", current: "fs: [deny:/current/secrets]\n", wantDeny: "/current/secrets"},
		{name: "stable invalid current", channel: "stable", current: "[invalid yaml"},
		{name: "preview isolated", channel: "preview"},
		{name: "preview current policy", channel: "preview", current: "fs: [deny:/preview/secrets]\n", wantDeny: "/preview/secrets"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := wingconfig.ReleaseChannel
			wingconfig.ReleaseChannel = tc.channel
			t.Cleanup(func() { wingconfig.ReleaseChannel = previous })
			home := t.TempDir()
			t.Setenv("HOME", home)
			state := filepath.Join(home, "alternate-state")
			t.Setenv("WINGTHING_DIR", state)
			t.Setenv("WINGTHING_PREVIEW_DIR", "")
			legacy := filepath.Join(home, ".wingthing")
			makeEggConfigTestDir(t, legacy)
			writeEggConfigTestFile(t, filepath.Join(legacy, "egg.yaml"), "fs: [deny:/legacy/secrets]\n")
			if tc.current != "" {
				makeEggConfigTestDir(t, state)
				writeEggConfigTestFile(t, filepath.Join(state, "egg.yaml"), tc.current)
			}
			cfg := DiscoverEggConfig(t.TempDir(), nil)
			resolved := cfg.ToSandboxConfig(home)
			found := false
			for _, path := range resolved.Deny {
				if path == tc.wantDeny {
					found = true
				}
				if path == "/legacy/secrets" && tc.wantDeny != path {
					t.Fatal("legacy sandbox policy overrode the selected channel policy")
				}
			}
			if tc.wantDeny != "" && !found {
				t.Fatalf("deny rules = %q, want %q", resolved.Deny, tc.wantDeny)
			}
		})
	}
}

func TestDiscoverEggConfig_ProjectConfig(t *testing.T) {
	dir := t.TempDir()
	writeEggConfigTestFile(t, filepath.Join(dir, "egg.yaml"), `base: none
fs:
  - rw:./
`)

	cfg := DiscoverEggConfig(dir, nil)
	if mounts, _, denied := ParseFSRules(cfg.FS, ""); len(mounts) != 1 || len(denied) != 1 {
		t.Errorf("expected 1 FS rule plus policy protection, got %d: %v", len(cfg.FS), cfg.FS)
	}
}

func TestDiscoverEggConfig_WingDefault(t *testing.T) {
	wingCfg := &EggConfig{
		FS:      []string{"rw:./", "deny:~/.ssh"},
		Network: NetworkField{Domains: []string{"*"}},
	}
	cfg := DiscoverEggConfig("/nonexistent", wingCfg)
	if len(cfg.Network.Domains) != 1 || cfg.Network.Domains[0] != "*" {
		t.Error("should use wing default when no project config")
	}
}

func TestUnsandboxedEggConfig(t *testing.T) {
	cfg := UnsandboxedEggConfig()
	if RequiresSandbox(cfg, "claude") {
		t.Fatal("trusted VM policy unexpectedly requires a nested sandbox")
	}
	if !cfg.IsAllEnv() {
		t.Fatal("trusted VM policy does not pass the host environment")
	}
	if got := sandbox.NetworkNeedFromDomains(cfg.Network.Domains); got != sandbox.NetworkFull {
		t.Fatalf("network need = %s, want full", got)
	}
	if cfg.Network.AgentDomains != "none" {
		t.Fatalf("agent domains = %q, want none for the explicit outer-boundary policy", cfg.Network.AgentDomains)
	}

	withLimit := *cfg
	withLimit.Resources.Memory = "1GB"
	if !RequiresSandbox(&withLimit, "claude") {
		t.Fatal("resource policy must require the sandbox backend")
	}
}

func TestWildcardNetworkAloneDoesNotDisableSandbox(t *testing.T) {
	tests := []struct {
		name string
		cfg  *EggConfig
	}{
		{"filtered environment", &EggConfig{Network: NetworkField{Domains: []string{"*"}}, Env: EnvField{"PATH"}}},
		{"forwarded local port", &EggConfig{Network: NetworkField{Domains: []string{"*"}, LocalPorts: []int{11434}}, Env: EnvField{"*"}}},
		{"network mode", &EggConfig{Network: NetworkField{Domains: []string{"*"}, Mode: "observe"}, Env: EnvField{"*"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !RequiresSandbox(tt.cfg, "claude") {
				t.Fatal("partial policy silently selected the trusted outer boundary")
			}
		})
	}

	// This exact legacy config remains the configuration-file spelling of an
	// unrestricted trusted host. New command-line callers use --unsandboxed.
	legacy := &EggConfig{Network: NetworkField{Domains: []string{"*"}}, Env: EnvField{"*"}}
	if RequiresSandbox(legacy, "claude") {
		t.Fatal("fully unrestricted legacy trusted-host policy lost compatibility")
	}
}

func TestBaseField_UnmarshalYAML(t *testing.T) {
	tests := []struct {
		yaml string
		want BaseField
	}{
		{`base: none`, BaseField{Name: "none"}},
		{`base: strict`, BaseField{Name: "strict"}},
		{`base: ""`, BaseField{}},
		{
			"base:\n  fs: none\n  env: team-env",
			BaseField{FS: "none", Env: "team-env"},
		},
		{
			"base:\n  name: strict\n  fs: none\n  network: none",
			BaseField{Name: "strict", FS: "none", Network: "none"},
		},
	}
	for _, tt := range tests {
		var cfg EggConfig
		if err := yaml.Unmarshal([]byte(tt.yaml), &cfg); err != nil {
			t.Fatalf("unmarshal %q: %v", tt.yaml, err)
		}
		if cfg.Base != tt.want {
			t.Errorf("unmarshal %q:\n  got  %+v\n  want %+v", tt.yaml, cfg.Base, tt.want)
		}
	}
}

func TestBaseField_MarshalRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		base BaseField
		want string // expected YAML substring
	}{
		{"scalar", BaseField{Name: "strict"}, "base: strict"},
		{"object", BaseField{Name: "strict", FS: "none"}, "name: strict"},
	}
	for _, tt := range tests {
		cfg := EggConfig{Base: tt.base}
		data, err := yaml.Marshal(&cfg)
		if err != nil {
			t.Fatalf("%s: marshal: %v", tt.name, err)
		}
		if !strings.Contains(string(data), tt.want) {
			t.Errorf("%s: yaml = %q, want substring %q", tt.name, string(data), tt.want)
		}
		// Round-trip
		var cfg2 EggConfig
		if err := yaml.Unmarshal(data, &cfg2); err != nil {
			t.Fatalf("%s: unmarshal: %v", tt.name, err)
		}
		if cfg2.Base != tt.base {
			t.Errorf("%s: round-trip got %+v, want %+v", tt.name, cfg2.Base, tt.base)
		}
	}
}

func TestSectionMask_None(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "egg.yaml")
	writeEggConfigTestFile(t, path, "base:\n  fs: none\n")

	cfg, err := ResolveEggConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	// Masking defaults must leave the policy itself write-protected.
	if len(cfg.FS) != 2 || cfg.FS[0] != "deny-write:"+canonicalPolicyTestPath(t, path) || cfg.FS[1] != "deny-rename:"+canonicalPolicyTestPath(t, dir) {
		t.Errorf("fs should contain only policy protection, got %v", cfg.FS)
	}
	// Env should still come from defaults
	hasHome := false
	for _, v := range cfg.Env {
		if v == "HOME" {
			hasHome = true
		}
	}
	if !hasHome {
		t.Error("env should still have HOME from defaults when only fs is masked")
	}
}

func TestSectionMask_FileRef(t *testing.T) {
	dir := t.TempDir()
	// Create a base config with specific env
	writeEggConfigTestFile(t, filepath.Join(dir, "prod-env.yaml"), "base: none\nenv:\n  - PROD_KEY\n  - DB_URL\n")
	// Project references it for env only
	path := filepath.Join(dir, "egg.yaml")
	writeEggConfigTestFile(t, path, "base:\n  env: ./prod-env.yaml\n")

	cfg, err := ResolveEggConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	// FS should come from defaults (not masked)
	if len(cfg.FS) == 0 {
		t.Error("fs should come from defaults")
	}
	// Env should come from prod-env.yaml (resolved)
	hasProd := false
	for _, v := range cfg.Env {
		if v == "PROD_KEY" {
			hasProd = true
		}
	}
	if !hasProd {
		t.Error("env should include PROD_KEY from prod-env.yaml")
	}
	// Default env essentials should NOT be present (prod-env has base: none)
	hasHome := false
	for _, v := range cfg.Env {
		if v == "HOME" {
			hasHome = true
		}
	}
	if hasHome {
		t.Error("env should not have HOME — prod-env.yaml has base: none")
	}
}

func TestSectionMask_FallThrough(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	basesDir := filepath.Join(home, ".wingthing", "bases")
	makeEggConfigTestDir(t, basesDir)
	// team-env has no fs of its own, just env. Its base resolves defaults, so fs comes from defaults.
	writeEggConfigTestFile(t, filepath.Join(basesDir, "team-env.yaml"), "env:\n  - TEAM_KEY\n")

	dir := t.TempDir()
	path := filepath.Join(dir, "egg.yaml")
	writeEggConfigTestFile(t, path, "base:\n  env: team-env\n")

	cfg, err := ResolveEggConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	// Env: team-env resolves against defaults, so gets HOME,PATH,etc + TEAM_KEY
	hasTeam := false
	hasHome := false
	for _, v := range cfg.Env {
		if v == "TEAM_KEY" {
			hasTeam = true
		}
		if v == "HOME" {
			hasHome = true
		}
	}
	if !hasTeam {
		t.Error("env should include TEAM_KEY from team-env.yaml")
	}
	if !hasHome {
		t.Error("env should include HOME — team-env.yaml inherits defaults")
	}
}

func TestSectionMask_Combo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	basesDir := filepath.Join(home, ".wingthing", "bases")
	makeEggConfigTestDir(t, basesDir)
	writeEggConfigTestFile(t, filepath.Join(basesDir, "strict.yaml"), "base: none\nfs:\n  - rw:./\nnetwork:\n  - api.internal.corp\n")

	dir := t.TempDir()
	writeEggConfigTestFile(t, filepath.Join(dir, "prod-env.yaml"), "base: none\nenv:\n  - PROD_KEY\n")
	path := filepath.Join(dir, "egg.yaml")
	writeEggConfigTestFile(t, path, "base:\n  name: strict\n  fs: none\n  env: ./prod-env.yaml\n")

	cfg, err := ResolveEggConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	// FS: masked to none, except protection for all three dependencies.
	if _, _, protected := ParseFSRules(cfg.FS, home); len(protected) != 3 {
		t.Errorf("fs should contain 3 protected policies and their ancestors, got %v", cfg.FS)
	}
	// Network: from strict
	if len(cfg.Network.Domains) != 1 || cfg.Network.Domains[0] != "api.internal.corp" {
		t.Errorf("network should come from strict, got %v", cfg.Network)
	}
	// Env: from prod-env
	if len(cfg.Env) != 1 || cfg.Env[0] != "PROD_KEY" {
		t.Errorf("env should come from prod-env, got %v", cfg.Env)
	}
}

func TestSectionMask_CycleDetection(t *testing.T) {
	dir := t.TempDir()
	// a.yaml references b.yaml for env, b.yaml references a.yaml
	writeEggConfigTestFile(t, filepath.Join(dir, "a.yaml"), "base:\n  env: ./b.yaml\n")
	writeEggConfigTestFile(t, filepath.Join(dir, "b.yaml"), "base: ./a.yaml\n")

	_, err := ResolveEggConfig(filepath.Join(dir, "a.yaml"))
	if err == nil {
		t.Fatal("expected cycle error")
	}
	if !strings.Contains(err.Error(), "circular") {
		t.Errorf("error = %q, want circular reference error", err)
	}
}

func TestSectionMask_WithBaseNone_Error(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "egg.yaml")
	writeEggConfigTestFile(t, path, "base:\n  name: none\n  fs: none\n")

	_, err := ResolveEggConfig(path)
	if err == nil {
		t.Fatal("expected error: can't mask with base: none")
	}
	if !strings.Contains(err.Error(), "nothing to mask") {
		t.Errorf("error = %q, want 'nothing to mask'", err)
	}
}

func TestBaseField_BackwardCompat(t *testing.T) {
	dir := t.TempDir()

	// String "none" should work exactly as before
	pathNone := filepath.Join(dir, "none.yaml")
	writeEggConfigTestFile(t, pathNone, "base: none\nfs:\n  - rw:./\n")
	cfg, err := ResolveEggConfig(pathNone)
	if err != nil {
		t.Fatal(err)
	}
	if mounts, _, denied := ParseFSRules(cfg.FS, ""); len(mounts) != 1 || len(denied) != 1 || cfg.FS[0] != "rw:./" {
		t.Errorf("base:none backward compat failed, fs = %v", cfg.FS)
	}

	// String base with named ref should work
	home := t.TempDir()
	t.Setenv("HOME", home)
	basesDir := filepath.Join(home, ".wingthing", "bases")
	makeEggConfigTestDir(t, basesDir)
	writeEggConfigTestFile(t, filepath.Join(basesDir, "strict.yaml"), "base: none\nfs:\n  - rw:./\n")

	pathStrict := filepath.Join(dir, "strict.yaml")
	writeEggConfigTestFile(t, pathStrict, "base: strict\nenv:\n  - CUSTOM_VAR\n")
	cfg2, err := ResolveEggConfig(pathStrict)
	if err != nil {
		t.Fatal(err)
	}
	if mounts, _, denied := ParseFSRules(cfg2.FS, ""); len(mounts) != 1 || len(denied) != 2 || cfg2.FS[0] != "rw:./" {
		t.Errorf("base:strict backward compat failed, fs = %v", cfg2.FS)
	}
	hasCustom := false
	for _, v := range cfg2.Env {
		if v == "CUSTOM_VAR" {
			hasCustom = true
		}
	}
	if !hasCustom {
		t.Error("base:strict should merge child env")
	}
}

func TestDefaultEggConfig_EnvEssentials(t *testing.T) {
	cfg := DefaultEggConfig()
	essentials := []string{"HOME", "PATH", "TERM", "LANG", "USER"}
	for _, k := range essentials {
		found := false
		for _, v := range cfg.Env {
			if v == k {
				found = true
			}
		}
		if !found {
			t.Errorf("default config missing env essential: %s", k)
		}
	}
}

func TestBuildEnv_SSHAuthSockStrippedWhenSSHDenied(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "/tmp/fake-ssh-agent.sock")

	home, _ := os.UserHomeDir()

	// deny:~/.ssh should strip SSH_AUTH_SOCK
	cfg := &EggConfig{
		FS:  []string{"rw:./", "deny:~/.ssh"},
		Env: []string{"*"},
	}
	env := cfg.BuildEnv("")
	for _, e := range env {
		if strings.HasPrefix(e, "SSH_AUTH_SOCK=") {
			t.Errorf("SSH_AUTH_SOCK should be stripped when ~/.ssh is denied, got %s", e)
		}
	}

	// deny:~/.ssh/<subpath> should also strip it (parent is denied)
	cfg2 := &EggConfig{
		FS:  []string{"deny:" + home + "/.ssh"},
		Env: []string{"*"},
	}
	env2 := cfg2.BuildEnv("")
	for _, e := range env2 {
		if strings.HasPrefix(e, "SSH_AUTH_SOCK=") {
			t.Errorf("SSH_AUTH_SOCK should be stripped when ~/.ssh is denied (absolute path), got %s", e)
		}
	}

	// no deny:~/.ssh — SSH_AUTH_SOCK should pass through
	cfg3 := &EggConfig{
		FS:  []string{"rw:./", "deny:~/.gnupg"},
		Env: []string{"*"},
	}
	env3 := cfg3.BuildEnv("")
	found := false
	for _, e := range env3 {
		if strings.HasPrefix(e, "SSH_AUTH_SOCK=") {
			found = true
		}
	}
	if !found {
		t.Error("SSH_AUTH_SOCK should not be stripped when ~/.ssh is not denied")
	}
}

func TestBuildEnv_ExplicitSSHAuthSockOptsIntoAgentOnlyAccess(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "/tmp/fake-ssh-agent.sock")
	cfg := &EggConfig{
		FS:  []string{"deny:~/.ssh"},
		Env: []string{"HOME", "SSH_AUTH_SOCK"},
	}
	found := false
	for _, entry := range cfg.BuildEnv("") {
		if entry == "SSH_AUTH_SOCK=/tmp/fake-ssh-agent.sock" {
			found = true
		}
	}
	if !found {
		t.Fatal("explicit SSH_AUTH_SOCK opt-in was stripped")
	}
}

func TestSSHAgentSocketDenyPathsMasksDiscoverableSocket(t *testing.T) {
	socketDir, err := os.MkdirTemp("/tmp", "wt-ssh-sock-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(socketDir); err != nil {
			t.Errorf("remove socket directory: %v", err)
		}
	})
	socketPath := filepath.Join(socketDir, "agent.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := listener.Close(); err != nil {
			t.Errorf("close SSH agent socket: %v", err)
		}
	})
	t.Setenv("SSH_AUTH_SOCK", socketPath)
	resolvedSocketPath, err := filepath.EvalSymlinks(socketPath)
	if err != nil {
		t.Fatal(err)
	}

	implicit := &EggConfig{FS: []string{"deny:~/.ssh"}, Env: []string{"*"}}
	if got := implicit.SSHAgentSocketDenyPaths("", false); len(got) != 1 || got[0] != resolvedSocketPath {
		t.Fatalf("implicit socket deny paths = %v, want [%s]", got, resolvedSocketPath)
	}
	if got := implicit.ToSandboxConfig("").Deny; len(got) < 2 || got[len(got)-1] != resolvedSocketPath {
		t.Fatalf("sandbox deny paths = %v, want live socket last", got)
	}

	explicit := &EggConfig{FS: []string{"deny:~/.ssh"}, Env: []string{"SSH_AUTH_SOCK"}}
	if got := explicit.SSHAgentSocketDenyPaths("", false); len(got) != 0 {
		t.Fatalf("explicit opt-in socket deny paths = %v", got)
	}
	if got := explicit.SSHAgentSocketDenyPaths("", true); len(got) != 1 || got[0] != resolvedSocketPath {
		t.Fatalf("shared-host forced socket deny paths = %v, want [%s]", got, resolvedSocketPath)
	}
}

func TestSandboxAllowedSocketsIncludesOnlyFilteredLiveSSHAgent(t *testing.T) {
	socketDir, err := os.MkdirTemp("/tmp", "wt-allowed-ssh-sock-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socketPath := filepath.Join(socketDir, "agent.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	resolved, err := filepath.EvalSymlinks(socketPath)
	if err != nil {
		t.Fatal(err)
	}

	toolSocket := filepath.Join(t.TempDir(), "tool.sock")
	got := sandboxAllowedSockets(toolSocket, map[string]string{"SSH_AUTH_SOCK": socketPath})
	if len(got) != 2 || got[0] != toolSocket || got[1] != resolved {
		t.Fatalf("allowed sockets = %v, want [%s %s]", got, toolSocket, resolved)
	}
	if got := sandboxAllowedSockets(resolved, map[string]string{"SSH_AUTH_SOCK": socketPath}); len(got) != 1 || got[0] != resolved {
		t.Fatalf("deduplicated allowed sockets = %v, want [%s]", got, resolved)
	}
	for name, value := range map[string]string{
		"relative": "agent.sock",
		"missing":  filepath.Join(socketDir, "missing.sock"),
	} {
		t.Run(name, func(t *testing.T) {
			if got := sandboxAllowedSockets("", map[string]string{"SSH_AUTH_SOCK": value}); len(got) != 0 {
				t.Fatalf("allowed sockets = %v", got)
			}
		})
	}
}

func TestSSHAgentSocketDenyPathsIgnoresNonSocketAndRelativeValues(t *testing.T) {
	cfg := &EggConfig{FS: []string{"deny:~/.ssh"}, Env: []string{"*"}}
	for name, value := range map[string]string{
		"relative": "agent.sock",
		"regular file": func() string {
			path := filepath.Join(t.TempDir(), "not-a-socket")
			if err := os.WriteFile(path, []byte("not a socket"), 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("SSH_AUTH_SOCK", value)
			if got := cfg.SSHAgentSocketDenyPaths("", false); len(got) != 0 {
				t.Fatalf("deny paths = %v", got)
			}
		})
	}
}

func TestBuildEnv_ClaudeCodeVarsNeverLeak(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "cli")
	t.Setenv("CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS", "1")
	t.Setenv("HOME", os.Getenv("HOME"))

	// env: ["*"] should still strip CLAUDECODE and CLAUDE_CODE_* vars
	cfg := &EggConfig{
		FS:  []string{"rw:./"},
		Env: []string{"*"},
	}
	for _, e := range cfg.BuildEnv("") {
		k, _, _ := strings.Cut(e, "=")
		if k == "CLAUDECODE" || strings.HasPrefix(k, "CLAUDE_CODE_") {
			t.Errorf("agent env should never contain %s (causes nested session error)", k)
		}
	}

	// Explicit allowlist should also strip them
	cfg2 := &EggConfig{
		FS:  []string{"rw:./"},
		Env: []string{"HOME", "PATH", "CLAUDECODE"},
	}
	for _, e := range cfg2.BuildEnv("") {
		k, _, _ := strings.Cut(e, "=")
		if k == "CLAUDECODE" {
			t.Errorf("CLAUDECODE should be stripped even if explicitly listed in env config")
		}
	}
}

func TestParseFSRules_DenyRoot(t *testing.T) {
	home := "/home/test"
	fs := []string{
		"deny:/",
		"ro:/usr",
		"ro:/etc",
		"rw:/opt/work",
		"deny:/opt/secret",
		"deny-write:./egg.yaml",
	}
	mounts, deny, denyWrite := ParseFSRules(fs, home)
	// Mounts: /usr (ro), /etc (ro), /opt/work (rw)
	if len(mounts) != 3 {
		t.Errorf("mounts = %d, want 3", len(mounts))
	}
	if mounts[0].Source != "/usr" || !mounts[0].ReadOnly {
		t.Errorf("mounts[0] = %+v, want ro /usr", mounts[0])
	}
	if mounts[2].Source != "/opt/work" || mounts[2].ReadOnly {
		t.Errorf("mounts[2] = %+v, want rw /opt/work", mounts[2])
	}
	// Deny: / and /opt/secret
	if len(deny) != 2 {
		t.Errorf("deny = %v, want 2 entries", deny)
	}
	if deny[0] != "/" {
		t.Errorf("deny[0] = %q, want /", deny[0])
	}
	if deny[1] != "/opt/secret" {
		t.Errorf("deny[1] = %q, want /opt/secret", deny[1])
	}
	// DenyWrite: ./egg.yaml
	if len(denyWrite) != 1 || denyWrite[0] != "./egg.yaml" {
		t.Errorf("denyWrite = %v, want [./egg.yaml]", denyWrite)
	}
}

func TestParseFSRules_TildeExpandsToUserHome(t *testing.T) {
	userHome := "/custom/user-home"
	fs := []string{
		"rw:~/.cache",
		"deny:~/.ssh",
		"ro:~/projects",
	}
	mounts, deny, _ := ParseFSRules(fs, userHome)

	// rw:~/.cache should expand to /custom/user-home/.cache
	if len(mounts) != 2 {
		t.Fatalf("mounts = %d, want 2", len(mounts))
	}
	if mounts[0].Source != "/custom/user-home/.cache" {
		t.Errorf("rw mount = %q, want /custom/user-home/.cache", mounts[0].Source)
	}
	if mounts[0].ReadOnly {
		t.Error("~/.cache should be rw")
	}

	// ro:~/projects should expand to /custom/user-home/projects
	if mounts[1].Source != "/custom/user-home/projects" || !mounts[1].ReadOnly {
		t.Errorf("ro mount = %+v, want ro /custom/user-home/projects", mounts[1])
	}

	// deny:~/.ssh should expand to /custom/user-home/.ssh
	if len(deny) != 1 || deny[0] != "/custom/user-home/.ssh" {
		t.Errorf("deny = %v, want [/custom/user-home/.ssh]", deny)
	}
}

func TestToSandboxConfig_RespectsCustomHome(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	cfg := &EggConfig{
		FS: []string{"deny:~/.ssh", "rw:~/.cache"},
	}
	userHome := "/custom/user-home"
	sbCfg := cfg.ToSandboxConfig(userHome)

	// Deny should use custom home
	if len(sbCfg.Deny) != 1 || sbCfg.Deny[0] != "/custom/user-home/.ssh" {
		t.Errorf("deny = %v, want [/custom/user-home/.ssh]", sbCfg.Deny)
	}
	// Mount should use custom home
	if len(sbCfg.Mounts) != 1 || sbCfg.Mounts[0].Source != "/custom/user-home/.cache" {
		t.Errorf("mounts = %v, want rw /custom/user-home/.cache", sbCfg.Mounts)
	}
}

func TestSshDirDenied_RespectsCustomHome(t *testing.T) {
	userHome := "/custom/user-home"

	// deny:~/.ssh with custom home should match custom home's .ssh
	cfg := &EggConfig{
		FS: []string{"deny:~/.ssh"},
	}
	if !cfg.sshDirDenied(userHome) {
		t.Error("sshDirDenied should be true when deny:~/.ssh is set with custom home")
	}

	// deny using a different absolute path should not match
	cfg2 := &EggConfig{
		FS: []string{"deny:/other/home/.ssh"},
	}
	if cfg2.sshDirDenied(userHome) {
		t.Error("sshDirDenied should be false when deny path doesn't match custom home")
	}
}

func TestBuildEnv_RespectsCustomHome(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "/tmp/fake-ssh-agent.sock")
	userHome := "/custom/user-home"

	// deny:~/.ssh with custom home should strip SSH_AUTH_SOCK
	cfg := &EggConfig{
		FS:  []string{"rw:./", "deny:~/.ssh"},
		Env: []string{"*"},
	}
	for _, e := range cfg.BuildEnv(userHome) {
		if strings.HasPrefix(e, "SSH_AUTH_SOCK=") {
			t.Error("SSH_AUTH_SOCK should be stripped when deny:~/.ssh with custom home")
		}
	}
}

func TestSectionMask_EnvNone_RemovesEssentials(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "egg.yaml")
	writeEggConfigTestFile(t, path, "base:\n  env: none\nenv:\n  - CUSTOM_ONLY\n")

	cfg, err := ResolveEggConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	// Env should only have CUSTOM_ONLY — no HOME, PATH, etc.
	if len(cfg.Env) != 1 || cfg.Env[0] != "CUSTOM_ONLY" {
		t.Errorf("env should be [CUSTOM_ONLY], got %v", cfg.Env)
	}
	env := cfg.BuildEnv("")
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		if k == "HOME" || k == "PATH" {
			t.Errorf("BuildEnv should not include %s with env: none mask", k)
		}
	}
}

func canonicalPolicyTestPath(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

func TestResolvedPoliciesPinAncestorsToWorkspace(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "policy", "nested", "base.yaml")
	makeEggConfigTestDir(t, filepath.Dir(base))
	writeEggConfigTestFile(t, base, "base: none\nfs: [rw:./]\n")
	policy := filepath.Join(root, "egg.yaml")
	writeEggConfigTestFile(t, policy, "base: ./policy/nested/base.yaml\n")
	cfg, err := ResolveEggConfig(policy)
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.ToSandboxConfig(root)
	for _, dir := range []string{root, filepath.Dir(base), filepath.Dir(filepath.Dir(base))} {
		found := false
		for _, pinned := range got.DenyRename {
			found = found || pinned == canonicalPolicyTestPath(t, dir)
		}
		if !found {
			t.Fatalf("replaceable policy ancestor %s: %v", dir, got.DenyRename)
		}
	}
}
