package egg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

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
		Network: NetworkField{"api.anthropic.com"},
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
	if len(merged.Network) != 1 || merged.Network[0] != "api.anthropic.com" {
		t.Errorf("network = %v, want [api.anthropic.com]", merged.Network)
	}
}

func TestMergeEggConfig_BaseNone(t *testing.T) {
	// base: none means empty slate
	dir := t.TempDir()
	path := filepath.Join(dir, "egg.yaml")
	os.WriteFile(path, []byte(`base: none
fs:
  - rw:./
  - ro:~/.ssh
`), 0644)

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
	if len(cfg.FS) != 2 {
		t.Errorf("expected 2 FS rules, got %d: %v", len(cfg.FS), cfg.FS)
	}
}

func TestMergeEggConfig_NamedBase(t *testing.T) {
	// Create a named base in a temp dir
	home := t.TempDir()
	old := os.Getenv("HOME")
	os.Setenv("HOME", home)
	defer os.Setenv("HOME", old)

	basesDir := filepath.Join(home, ".wingthing", "bases")
	os.MkdirAll(basesDir, 0755)
	os.WriteFile(filepath.Join(basesDir, "strict.yaml"), []byte(`base: none
fs:
  - rw:./
  - deny:~/.ssh
  - deny:~/.aws
network: none
`), 0644)

	// Project config references the named base
	dir := t.TempDir()
	path := filepath.Join(dir, "egg.yaml")
	os.WriteFile(path, []byte(`base: strict
fs:
  - ro:~/.ssh
network:
  - api.anthropic.com
`), 0644)

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
	if len(cfg.Network) != 1 || cfg.Network[0] != "api.anthropic.com" {
		t.Errorf("network = %v, want [api.anthropic.com]", cfg.Network)
	}
}

func TestMergeEggConfig_RelativePath(t *testing.T) {
	dir := t.TempDir()

	// Create a base config in a subdirectory
	basesDir := filepath.Join(dir, "bases")
	os.MkdirAll(basesDir, 0755)
	os.WriteFile(filepath.Join(basesDir, "ci.yaml"), []byte(`base: none
fs:
  - rw:./
`), 0644)

	// Project config with relative base path
	path := filepath.Join(dir, "egg.yaml")
	os.WriteFile(path, []byte(`base: ./bases/ci.yaml
fs:
  - deny:~/.ssh
`), 0644)

	cfg, err := ResolveEggConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.FS) != 2 {
		t.Errorf("expected 2 FS rules, got %d: %v", len(cfg.FS), cfg.FS)
	}
}

func TestResolveEggConfig_CircularReference(t *testing.T) {
	dir := t.TempDir()

	// a.yaml -> b.yaml -> a.yaml (circular)
	os.WriteFile(filepath.Join(dir, "a.yaml"), []byte("base: ./b.yaml\n"), 0644)
	os.WriteFile(filepath.Join(dir, "b.yaml"), []byte("base: ./a.yaml\n"), 0644)

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
			os.WriteFile(name, []byte("base: "+next+"\n"), 0644)
		} else {
			os.WriteFile(name, []byte("base: none\n"), 0644)
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
	parent := &EggConfig{Network: NetworkField{"api.anthropic.com"}}
	child := &EggConfig{Network: NetworkField{"api.openai.com"}}
	merged := MergeEggConfig(parent, child)
	if len(merged.Network) != 2 {
		t.Errorf("network = %v, want 2 domains", merged.Network)
	}

	// Wildcard in either -> wildcard
	parent2 := &EggConfig{Network: NetworkField{"*"}}
	child2 := &EggConfig{Network: NetworkField{"api.openai.com"}}
	merged2 := MergeEggConfig(parent2, child2)
	if len(merged2.Network) != 1 || merged2.Network[0] != "*" {
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
	os.WriteFile(path, []byte(`fs:
  - ro:~/.ssh
`), 0644)

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

func TestDiscoverEggConfig_FallsBackToDefault(t *testing.T) {
	cfg := DiscoverEggConfig("/nonexistent", nil)
	if len(cfg.FS) == 0 {
		t.Error("should fall back to default config")
	}
}

func TestDiscoverEggConfig_ProjectConfig(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "egg.yaml"), []byte(`base: none
fs:
  - rw:./
`), 0644)

	cfg := DiscoverEggConfig(dir, nil)
	if len(cfg.FS) != 1 {
		t.Errorf("expected 1 FS rule from project config, got %d: %v", len(cfg.FS), cfg.FS)
	}
}

func TestDiscoverEggConfig_WingDefault(t *testing.T) {
	wingCfg := &EggConfig{
		FS:      []string{"rw:./", "deny:~/.ssh"},
		Network: NetworkField{"*"},
	}
	cfg := DiscoverEggConfig("/nonexistent", wingCfg)
	if len(cfg.Network) != 1 || cfg.Network[0] != "*" {
		t.Error("should use wing default when no project config")
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
	os.WriteFile(path, []byte("base:\n  fs: none\n"), 0644)

	cfg, err := ResolveEggConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	// FS should be empty (cut from defaults)
	if len(cfg.FS) != 0 {
		t.Errorf("fs should be empty with base.fs: none, got %v", cfg.FS)
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
	os.WriteFile(filepath.Join(dir, "prod-env.yaml"), []byte("base: none\nenv:\n  - PROD_KEY\n  - DB_URL\n"), 0644)
	// Project references it for env only
	path := filepath.Join(dir, "egg.yaml")
	os.WriteFile(path, []byte("base:\n  env: ./prod-env.yaml\n"), 0644)

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
	old := os.Getenv("HOME")
	os.Setenv("HOME", home)
	defer os.Setenv("HOME", old)

	basesDir := filepath.Join(home, ".wingthing", "bases")
	os.MkdirAll(basesDir, 0755)
	// team-env has no fs of its own, just env. Its base resolves defaults, so fs comes from defaults.
	os.WriteFile(filepath.Join(basesDir, "team-env.yaml"), []byte("env:\n  - TEAM_KEY\n"), 0644)

	dir := t.TempDir()
	path := filepath.Join(dir, "egg.yaml")
	os.WriteFile(path, []byte("base:\n  env: team-env\n"), 0644)

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
	old := os.Getenv("HOME")
	os.Setenv("HOME", home)
	defer os.Setenv("HOME", old)

	basesDir := filepath.Join(home, ".wingthing", "bases")
	os.MkdirAll(basesDir, 0755)
	os.WriteFile(filepath.Join(basesDir, "strict.yaml"), []byte("base: none\nfs:\n  - rw:./\nnetwork:\n  - api.internal.corp\n"), 0644)

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "prod-env.yaml"), []byte("base: none\nenv:\n  - PROD_KEY\n"), 0644)
	path := filepath.Join(dir, "egg.yaml")
	os.WriteFile(path, []byte("base:\n  name: strict\n  fs: none\n  env: ./prod-env.yaml\n"), 0644)

	cfg, err := ResolveEggConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	// FS: masked to none, so empty
	if len(cfg.FS) != 0 {
		t.Errorf("fs should be empty (masked none), got %v", cfg.FS)
	}
	// Network: from strict
	if len(cfg.Network) != 1 || cfg.Network[0] != "api.internal.corp" {
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
	os.WriteFile(filepath.Join(dir, "a.yaml"), []byte("base:\n  env: ./b.yaml\n"), 0644)
	os.WriteFile(filepath.Join(dir, "b.yaml"), []byte("base: ./a.yaml\n"), 0644)

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
	os.WriteFile(path, []byte("base:\n  name: none\n  fs: none\n"), 0644)

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
	os.WriteFile(pathNone, []byte("base: none\nfs:\n  - rw:./\n"), 0644)
	cfg, err := ResolveEggConfig(pathNone)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.FS) != 1 || cfg.FS[0] != "rw:./" {
		t.Errorf("base:none backward compat failed, fs = %v", cfg.FS)
	}

	// String base with named ref should work
	home := t.TempDir()
	old := os.Getenv("HOME")
	os.Setenv("HOME", home)
	defer os.Setenv("HOME", old)
	basesDir := filepath.Join(home, ".wingthing", "bases")
	os.MkdirAll(basesDir, 0755)
	os.WriteFile(filepath.Join(basesDir, "strict.yaml"), []byte("base: none\nfs:\n  - rw:./\n"), 0644)

	pathStrict := filepath.Join(dir, "strict.yaml")
	os.WriteFile(pathStrict, []byte("base: strict\nenv:\n  - CUSTOM_VAR\n"), 0644)
	cfg2, err := ResolveEggConfig(pathStrict)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg2.FS) != 1 || cfg2.FS[0] != "rw:./" {
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

func TestSectionMask_EnvNone_RemovesEssentials(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "egg.yaml")
	os.WriteFile(path, []byte("base:\n  env: none\nenv:\n  - CUSTOM_ONLY\n"), 0644)

	cfg, err := ResolveEggConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	// Env should only have CUSTOM_ONLY — no HOME, PATH, etc.
	if len(cfg.Env) != 1 || cfg.Env[0] != "CUSTOM_ONLY" {
		t.Errorf("env should be [CUSTOM_ONLY], got %v", cfg.Env)
	}
	env := cfg.BuildEnv()
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		if k == "HOME" || k == "PATH" {
			t.Errorf("BuildEnv should not include %s with env: none mask", k)
		}
	}
}
