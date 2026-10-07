package taskrun

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

func TestDirectAgentSandboxConfigProtectsControllerAncestors(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("WT_PROVIDER_BASE_URL", "")
	state := filepath.Join(home, "nested", ".wingthing")
	t.Setenv("WINGTHING_DIR", state)
	defaultState := filepath.Join(home, ".wingthing")
	if err := prepareDirectAgentState("codex", home); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{state, defaultState} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"wing_key", "device_token.yaml"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("controller canary"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	cfg, err := directAgentSandboxConfigForTask(egg.DefaultEggConfig(), "codex", "standard", home, home, []string{home}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "darwin" {
		if !slices.Contains(cfg.ControlDenyPaths, filepath.Join(state, "wing_key")) || !slices.Contains(cfg.ControlDenyPaths, filepath.Join(defaultState, "wing_key")) || !cfg.DenyOtherProcessInfo {
			t.Fatalf("headless policy omitted controller protection: %v", cfg.ControlDenyPaths)
		}
	} else if runtime.GOOS == "linux" {
		if !slices.Contains(cfg.Deny, "/") {
			t.Fatal("headless policy omitted the controller jail")
		}
		for _, mount := range cfg.Mounts {
			for _, name := range []string{"wing_key", "device_token.yaml", "eggs"} {
				path := filepath.Join(state, name)
				if path == mount.Source || strings.HasPrefix(path, strings.TrimSuffix(mount.Source, "/")+"/") {
					t.Fatalf("headless mount exposes controller path %s: %+v", path, mount)
				}
			}
		}
	} else {
		t.Skip("requires macOS or Linux sandbox")
	}
	if !hasSandboxMount(cfg.Mounts, filepath.Join(home, ".codex")) {
		t.Fatal("controller isolation removed writable agent state")
	}
	if ok, reason := sandbox.CheckCapability(); !ok {
		t.Skipf("sandbox unavailable: %s", reason)
	}
	if runtime.GOOS == "darwin" {
		if output, err := exec.Command("/usr/bin/sandbox-exec", "-p", "(version 1) (allow default)", "/usr/bin/true").CombinedOutput(); err != nil {
			t.Skipf("Seatbelt enforcement unavailable: %v: %s", err, output)
		}
	} else {
		// ro:/ enumerates temporary siblings of this disposable HOME. Other
		// package tests may remove them before the jail starts. The live probe
		// needs only its own mounts, system tools, and the wrapper executable.
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		executable, err = filepath.EvalSymlinks(executable)
		if err != nil {
			t.Fatal(err)
		}
		var mounts []sandbox.Mount
		for _, mount := range cfg.Mounts {
			if mount.Source == "/usr" || mount.Source == executable || strings.HasPrefix(mount.Source, home+"/") {
				mounts = append(mounts, mount)
			}
		}
		cfg.Mounts = mounts
	}
	sb, err := sandbox.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sb.Destroy() })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// Try moving each controller ancestor, as well as replacing the state
	// entry with a symlink. Ordinary agent state must stay writable.
	probe := `
for path in "$1" "$2" "$3" "$4"; do
    if mv "$path" "$path-moved" 2>/dev/null; then
        echo "controller ancestor moved: $path"; exit 1
    fi
done
if rmdir "$1" 2>/dev/null; then
    echo 'controller state removed'; exit 1
fi
ln -s "$HOME/.codex" "$1" 2>/dev/null || true
if [ -L "$1" ]; then
    echo 'controller state replaced'; exit 1
fi
for dir in "$1" "$4"; do
    for name in wing_key device_token.yaml; do
        value=$(cat "$dir/$name" 2>/dev/null) || value=
        if [ -n "$value" ]; then echo 'controller secret exposed'; exit 1; fi
    done
done
echo agent-state > "$HOME/.codex/state"
`
	cmd, err := sb.Exec(ctx, "/bin/sh", []string{"-c", probe, "probe", state, filepath.Dir(state), home, defaultState})
	if err != nil {
		t.Fatal(err)
	}
	cmd.Dir = home
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
	if output, err := cmd.CombinedOutput(); err != nil {
		diag, _ := os.ReadFile(sb.DiagLog())
		t.Fatalf("headless controller protection: %v\n%s\n%s", err, output, diag)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "state")); err != nil {
		t.Fatalf("agent state did not persist: %v", err)
	}
}

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
	cfg, err := directAgentSandboxConfigForTask(eggCfg, "codex", "standard", home, workDir, nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NetworkNeed != sandbox.NetworkHTTPS || len(cfg.Domains) != 1 || cfg.Domains[0] != "factory.example.test" {
		t.Fatalf("network policy = %v %q", cfg.NetworkNeed, cfg.Domains)
	}
	if !hasSandboxMount(cfg.Mounts, filepath.Join(workDir, "artifacts")) {
		t.Fatalf("relative config mount was not rooted at cwd: %#v", cfg.Mounts)
	}
	if !slices.Contains(cfg.Deny, filepath.Join(home, ".factory-secret")) {
		t.Fatalf("deny policy = %#v", cfg.Deny)
	}
	if !slices.Contains(cfg.DenyWrite, filepath.Join(workDir, "egg.yaml")) {
		t.Fatalf("deny-write policy = %#v", cfg.DenyWrite)
	}
	if cfg.CPULimit != 45*time.Second || cfg.MemLimit != 64*1024*1024 || cfg.MaxFDs != 128 || cfg.PidLimit != 32 || !cfg.Trace {
		t.Fatalf("resource policy was lost: %#v", cfg)
	}
}

func TestDirectAgentSandboxConfigPreservesPolicyAncestorPins(t *testing.T) {
	t.Setenv("WT_PROVIDER_BASE_URL", "")
	workspace := t.TempDir()
	policyDir := filepath.Join(workspace, "policy")
	if err := os.Mkdir(policyDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(policyDir, "base.yaml"), []byte("base: none\nfs: [rw:./]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	policy := filepath.Join(workspace, "egg.yaml")
	if err := os.WriteFile(policy, []byte("base: ./policy/base.yaml\n"), 0600); err != nil {
		t.Fatal(err)
	}
	eggCfg, err := egg.ResolveEggConfig(policy)
	if err != nil {
		t.Fatal(err)
	}
	for _, sharedHost := range []bool{false, true} {
		t.Run(boolKey(sharedHost), func(t *testing.T) {
			cfg := *eggCfg
			cfg.FS = append([]string(nil), eggCfg.FS...)
			if sharedHost {
				cfg.FS = append(cfg.FS, "deny:/")
			}
			home := t.TempDir()
			want := cfg.ToSandboxConfig(home).DenyRename
			if len(want) == 0 {
				t.Fatal("resolved inherited policy has no ancestor pins")
			}
			got, err := directAgentSandboxConfigForTask(&cfg, "codex", "standard", home, workspace, nil, sharedHost, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range want {
				if !slices.Contains(got.DenyRename, path) {
					t.Fatalf("headless pins = %v, missing declared pin %s", got.DenyRename, path)
				}
			}
		})
	}
}

func TestRunEggConfigDiscoveryPersistsDomainWithoutActivatingEnvFiltering(t *testing.T) {
	workDir := t.TempDir()
	configYAML := "base: none\nfs:\n  - rw:./\nnetwork:\n  domains:\n    - api.arliai.com\nenv:\n  - ARLIAI_API_KEY\n"
	if err := os.WriteFile(filepath.Join(workDir, "egg.yaml"), []byte(configYAML), 0600); err != nil {
		t.Fatal(err)
	}

	rendered, err := ResolveRunEggConfigYAML("", workDir, false)
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
		"\nHTTPS_PROXY=http://127.0.0.1:43210\n",
		"\nHTTP_PROXY=http://127.0.0.1:43210\n",
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

func TestSandboxAgentExecutableUsesOwnerScopedSharedRuntime(t *testing.T) {
	home := t.TempDir()
	command := filepath.Join(home, ".local", "bin", "claude")
	if err := os.MkdirAll(filepath.Dir(command), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(command, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := sandboxAgentExecutable("claude", home, true)
	if err != nil {
		t.Fatal(err)
	}
	if got != command {
		t.Fatalf("shared executable = %q, want %q", got, command)
	}
	if _, err := sandboxAgentExecutable("missing", home, true); err == nil {
		t.Fatal("missing shared runtime was accepted")
	}
}

func TestSandboxAgentExecutableResolvesHostCommandForPersonalTask(t *testing.T) {
	got, err := sandboxAgentExecutable("sh", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("personal executable = %q, want absolute path", got)
	}
}

func TestSharedHostDirectAgentPreservesAdministratorFilesystemPolicy(t *testing.T) {
	t.Setenv("WT_PROVIDER_BASE_URL", "")
	home := t.TempDir()
	workspace := t.TempDir()
	readOnlySource := t.TempDir()
	deniedSecret := t.TempDir()
	eggCfg := &egg.EggConfig{FS: []string{
		"deny:/",
		"rw:" + workspace,
		"ro:" + readOnlySource,
		"deny:" + deniedSecret,
	}}
	cfg, err := directAgentSandboxConfigForTask(eggCfg, "codex", "standard", home, workspace, nil, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cfg.Deny, "/") {
		t.Fatalf("shared-host deny policy = %#v", cfg.Deny)
	}
	if !hasSandboxMount(cfg.Mounts, workspace) {
		t.Fatalf("workspace is not writable in %#v", cfg.Mounts)
	}
	if !hasReadOnlySandboxMount(cfg.Mounts, readOnlySource) {
		t.Fatalf("administrator read-only mount is absent from %#v", cfg.Mounts)
	}
	// Controller protection adds mandatory denies to the administrator policy.
	if !slices.Contains(cfg.Deny, deniedSecret) {
		t.Fatalf("administrator deny policy = %#v", cfg.Deny)
	}
}

func TestSharedHostDirectAgentMountsOnlyOwnerRuntimeAndHelperReadonly(t *testing.T) {
	for _, agentName := range []string{"claude", "codex"} {
		t.Run(agentName, func(t *testing.T) {
			home := t.TempDir()
			key := filepath.Join(home, ".anthropic_key")
			if err := os.WriteFile(key, []byte("owner-key"), 0400); err != nil {
				t.Fatal(err)
			}
			cfg, err := directAgentSandboxConfigForTask(&egg.EggConfig{FS: []string{"deny:/"}}, agentName, "standard", home, "", nil, true, nil)
			if err != nil {
				t.Fatal(err)
			}
			binary := filepath.Join(home, ".local", "bin", agentName)
			if !hasReadOnlySandboxMount(cfg.Mounts, binary) {
				t.Fatalf("owner runtime is not mounted read-only: %+v", cfg.Mounts)
			}
			if hasReadOnlySandboxMount(cfg.Mounts, key) != (agentName == "claude") {
				t.Fatalf("wrong credential helper exposure: %+v", cfg.Mounts)
			}
			for _, mount := range cfg.Mounts {
				if mount.Source == home || mount.Source == filepath.Dir(binary) || (mount.Source == key && !mount.ReadOnly) {
					t.Fatalf("shared-host runtime widened HOME access: %+v", mount)
				}
			}
		})
	}
}

func TestSharedHostTaskMountsValidatedWorkspaceRoots(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "mutable", "checkout")
	options := TaskRunOptions{SharedHost: true, AllowedPaths: []string{root}}
	mounts := taskSandboxMountPaths([]string{workDir, "/caller/widening"}, workDir, options)
	if len(mounts) != 0 {
		t.Fatalf("shared-host task mounts widened administrator policy: %#v", mounts)
	}

	personal := taskSandboxMountPaths([]string{"/prompt/mount"}, workDir, TaskRunOptions{})
	if len(personal) != 2 || personal[0] != "/prompt/mount" || personal[1] != workDir {
		t.Fatalf("personal task mounts = %#v", personal)
	}
}

func TestSharedHostDirectAgentIgnoresCallerMounts(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	callerMount := t.TempDir()
	cfg, err := directAgentSandboxConfigForTask(&egg.EggConfig{FS: []string{
		"deny:/",
		"rw:" + workspace,
	}}, "codex", "standard", home, workspace, []string{callerMount}, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if hasSandboxMount(cfg.Mounts, callerMount) {
		t.Fatalf("caller widened shared-host mounts: %#v", cfg.Mounts)
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

func hasReadOnlySandboxMount(mounts []sandbox.Mount, source string) bool {
	for _, mount := range mounts {
		if mount.Source == source && mount.ReadOnly {
			return true
		}
	}
	return false
}
