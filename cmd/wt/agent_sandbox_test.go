package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

func TestDirectAgentSandboxConfigAppliesOpenCodeProfile(t *testing.T) {
	t.Setenv("WT_PROVIDER_BASE_URL", "")
	home := t.TempDir()
	cfg, err := directAgentSandboxConfig("opencode", "standard", home, []string{"/work/project"})
	if err != nil {
		t.Fatal(err)
	}

	if cfg.NetworkNeed != sandbox.NetworkHTTPS {
		t.Fatalf("NetworkNeed = %v, want https", cfg.NetworkNeed)
	}
	if cfg.UserHome != home {
		t.Fatalf("UserHome = %q, want %q", cfg.UserHome, home)
	}
	for _, want := range []string{
		"/work/project",
		filepath.Join(home, ".config/opencode"),
		filepath.Join(home, ".local/share/opencode"),
		filepath.Join(home, ".local/state/opencode"),
		filepath.Join(home, ".cache/opencode"),
	} {
		if !hasSandboxMount(cfg.Mounts, want) {
			t.Errorf("missing writable mount %q in %#v", want, cfg.Mounts)
		}
	}
}

func TestDirectAgentSandboxConfigCapabilities(t *testing.T) {
	t.Setenv("WT_PROVIDER_BASE_URL", "")
	tests := []struct {
		name      string
		agent     string
		isolation string
		want      sandbox.NetworkNeed
	}{
		{name: "unknown stays offline", agent: "custom", isolation: "standard", want: sandbox.NetworkNone},
		{name: "ollama gets localhost", agent: "ollama", isolation: "strict", want: sandbox.NetworkLocal},
		{name: "agent profile drills https", agent: "gemini", isolation: "standard", want: sandbox.NetworkHTTPS},
		{name: "explicit network remains full", agent: "gemini", isolation: "network", want: sandbox.NetworkFull},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := directAgentSandboxConfig(tt.agent, tt.isolation, t.TempDir(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.NetworkNeed != tt.want {
				t.Fatalf("NetworkNeed = %v, want %v", cfg.NetworkNeed, tt.want)
			}
		})
	}
}

func TestDirectAgentSandboxConfigUsesExplicitLocalProvider(t *testing.T) {
	t.Setenv("WT_PROVIDER_BASE_URL", "http://127.0.0.1:4000/v1")
	cfg, err := directAgentSandboxConfig("codex", "standard", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NetworkNeed != sandbox.NetworkLocal {
		t.Fatalf("NetworkNeed = %v, want local", cfg.NetworkNeed)
	}
	if len(cfg.Domains) != 1 || cfg.Domains[0] != "127.0.0.1" {
		t.Fatalf("Domains = %q, want loopback provider only", cfg.Domains)
	}
}

func TestDirectAgentSandboxConfigRejectsUnsafeProviderURL(t *testing.T) {
	t.Setenv("WT_PROVIDER_BASE_URL", "http://api.example.com/v1")
	if _, err := directAgentSandboxConfig("opencode", "standard", t.TempDir(), nil); err == nil {
		t.Fatal("unsafe non-loopback http provider URL was accepted")
	}
}

func TestDirectAgentSandboxConfigAppliesTaskEggPolicy(t *testing.T) {
	t.Setenv("WT_PROVIDER_BASE_URL", "")
	home := t.TempDir()
	workDir := t.TempDir()
	eggCfg := &egg.EggConfig{
		FS:      []string{"rw:artifacts", "deny:~/.factory-secret", "deny-write:egg.yaml"},
		Network: egg.NetworkField{Domains: []string{"factory.example.test"}, AgentDomains: "none"},
		Resources: egg.EggResources{
			CPU:     "45s",
			Memory:  "64MB",
			MaxFDs:  128,
			MaxPids: 32,
		},
		Trace: true,
	}
	cfg, err := directAgentSandboxConfigForTask(eggCfg, "codex", "standard", home, workDir, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NetworkNeed != sandbox.NetworkHTTPS || len(cfg.Domains) != 1 || cfg.Domains[0] != "factory.example.test" {
		t.Fatalf("network policy = %v %q", cfg.NetworkNeed, cfg.Domains)
	}
	if !hasSandboxMount(cfg.Mounts, filepath.Join(workDir, "artifacts")) {
		t.Fatalf("relative config mount was not rooted at cwd: %#v", cfg.Mounts)
	}
	if len(cfg.Deny) != 1 || cfg.Deny[0] != filepath.Join(home, ".factory-secret") {
		t.Fatalf("deny policy = %#v", cfg.Deny)
	}
	if len(cfg.DenyWrite) != 1 || cfg.DenyWrite[0] != filepath.Join(workDir, "egg.yaml") {
		t.Fatalf("deny-write policy = %#v", cfg.DenyWrite)
	}
	if cfg.CPULimit != 45*time.Second || cfg.MemLimit != 64*1024*1024 || cfg.MaxFDs != 128 || cfg.PidLimit != 32 || !cfg.Trace {
		t.Fatalf("resource policy was lost: %#v", cfg)
	}
}

func TestRunEggConfigDiscoveryPersistsDomainWithoutActivatingEnvFiltering(t *testing.T) {
	workDir := t.TempDir()
	configYAML := "base: none\nfs:\n  - rw:./\nnetwork:\n  domains:\n    - api.arliai.com\nenv:\n  - ARLIAI_API_KEY\n"
	if err := os.WriteFile(filepath.Join(workDir, "egg.yaml"), []byte(configYAML), 0600); err != nil {
		t.Fatal(err)
	}

	rendered, err := resolveRunEggConfigYAML("", workDir, false)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := egg.LoadEggConfigFromYAML(rendered)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.Network.Domains) != 1 || resolved.Network.Domains[0] != "api.arliai.com" {
		t.Fatalf("persisted domains = %q", resolved.Network.Domains)
	}
	if len(resolved.Env) != 0 {
		t.Fatalf("persisted env policy = %q, want direct-run compatibility mode", resolved.Env)
	}
	t.Setenv("ARLIAI_API_KEY", "canary-secret")
	t.Setenv("UNDECLARED_COMPAT_CANARY", "also-preserved")
	env := directAgentEnvWithPolicy("opencode", t.TempDir(), 0, true)
	joined := "\n" + strings.Join(env, "\n") + "\n"
	for _, want := range []string{"\nARLIAI_API_KEY=canary-secret\n", "\nUNDECLARED_COMPAT_CANARY=also-preserved\n"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("local discovered-config run dropped ambient value %q", want)
		}
	}
}

func TestMergeAgentFailureOutputKeepsStreamAndDiagnostics(t *testing.T) {
	diagnostics := mergeAgentFailureDiagnostics(errors.New("exit status 1: authentication failed"), "")
	got := mergeAgentFailureOutput("partial agent answer", diagnostics)
	want := "partial agent answer\nexit status 1: authentication failed"
	if got != want {
		t.Fatalf("failure output = %q, want %q", got, want)
	}
}

func TestMergeAgentFailureDiagnosticsIncludesSandboxLog(t *testing.T) {
	got := mergeAgentFailureDiagnostics(errors.New("exit status 1"), "filesystem enforcement failed: make writable mount")
	want := "exit status 1\nfilesystem enforcement failed: make writable mount"
	if got != want {
		t.Fatalf("failure diagnostics = %q, want %q", got, want)
	}
}

func TestReadSandboxDiagnosticsKeepsTailWithinLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deny_init.log")
	prefix := strings.Repeat("x", maxSandboxDiagnostics)
	want := "actionable failure"
	if err := os.WriteFile(path, []byte(prefix+want), 0o600); err != nil {
		t.Fatal(err)
	}
	got := readSandboxDiagnostics(path)
	if !strings.HasSuffix(got, want) {
		t.Fatal("readSandboxDiagnostics() lost the trailing diagnostic")
	}
	if len(got) > maxSandboxDiagnostics {
		t.Fatalf("readSandboxDiagnostics() length = %d, want <= %d", len(got), maxSandboxDiagnostics)
	}
}

func TestDirectAgentEnvPreservesHomeAndAddsProxy(t *testing.T) {
	t.Setenv("WT_PROVIDER_BASE_URL", "")
	home := t.TempDir()
	env := directAgentEnv("opencode", home, 43210)
	joined := "\n" + strings.Join(env, "\n") + "\n"
	for _, want := range []string{
		"\nHOME=" + home + "\n",
		"\nHTTPS_PROXY=http://localhost:43210\n",
		"\nHTTP_PROXY=http://localhost:43210\n",
		"\nNODE_USE_ENV_PROXY=1\n",
		"\nGIT_TERMINAL_PROMPT=0\n",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("environment missing %q", strings.TrimSpace(want))
		}
	}
	if !strings.Contains(joined, filepath.Join(home, ".local", "bin")) {
		t.Error("PATH does not include the user's local bin directory")
	}
}

func TestDirectAgentEnvBypassesProxyForExplicitLocalProvider(t *testing.T) {
	t.Setenv("WT_PROVIDER_BASE_URL", "http://localhost:4000/v1")
	t.Setenv("NO_PROXY", "example.test")
	t.Setenv("no_proxy", "")
	env := directAgentEnv("codex", t.TempDir(), 0)
	joined := "\n" + strings.Join(env, "\n") + "\n"
	for _, want := range []string{"\nNO_PROXY=example.test,localhost\n", "\nno_proxy=localhost\n"} {
		if !strings.Contains(joined, want) {
			t.Errorf("environment missing %q", strings.TrimSpace(want))
		}
	}
}

func TestSharedHostDirectAgentEnvDropsAmbientProviderCredentials(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "host-secret")
	t.Setenv("OPENAI_API_KEY", "another-host-secret")
	env := directAgentEnvWithPolicy("claude", t.TempDir(), 0, false)
	joined := "\n" + strings.Join(env, "\n") + "\n"
	for _, forbidden := range []string{"ANTHROPIC_API_KEY=", "OPENAI_API_KEY="} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("shared-host environment contains %s", forbidden)
		}
	}
	if !strings.Contains(joined, "\nHOME=") || !strings.Contains(joined, "\nPATH=") {
		t.Fatalf("shared-host environment lost essentials: %s", joined)
	}
}

func TestSharedHostDirectAgentUsesAllowlistJail(t *testing.T) {
	t.Setenv("WT_PROVIDER_BASE_URL", "")
	home := t.TempDir()
	workspace := t.TempDir()
	cfg, err := directAgentSandboxConfigWithPolicy("codex", "standard", home, []string{workspace}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Deny) != 1 || cfg.Deny[0] != "/" {
		t.Fatalf("shared-host deny policy = %#v", cfg.Deny)
	}
	if !hasSandboxMount(cfg.Mounts, workspace) {
		t.Fatalf("workspace is not writable in %#v", cfg.Mounts)
	}
	for _, mount := range cfg.Mounts {
		if mount.Source == "/" {
			t.Fatalf("host root was mounted into the jail: %#v", cfg.Mounts)
		}
	}
}

func TestSharedHostTaskMountsValidatedWorkspaceRoots(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "mutable", "checkout")
	options := taskRunOptions{SharedHost: true, AllowedPaths: []string{root}}
	mounts := taskSandboxMountPaths([]string{workDir, "/caller/widening"}, workDir, options)
	if len(mounts) != 1 || mounts[0] != root {
		t.Fatalf("shared-host task mounts = %#v, want only %q", mounts, root)
	}

	personal := taskSandboxMountPaths([]string{"/prompt/mount"}, workDir, taskRunOptions{})
	if len(personal) != 2 || personal[0] != "/prompt/mount" || personal[1] != workDir {
		t.Fatalf("personal task mounts = %#v", personal)
	}
}

func hasSandboxMount(mounts []sandbox.Mount, source string) bool {
	for _, mount := range mounts {
		if mount.Source == source && !mount.ReadOnly {
			return true
		}
	}
	return false
}
