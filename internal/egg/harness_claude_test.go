package egg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

func harnessFixture() HarnessConfig {
	return HarnessConfig{Driver: "claude-code", Version: "2.1.285", PermissionMode: "dontAsk"}
}

// Conformance here exercises the policy compiler and production preparation,
// offline. It deliberately makes no installed sandbox enforcement claim.
func TestClaudeHarnessConformancePolicy(t *testing.T) {
	h := harnessFixture()
	p := EffectivePolicy{Agent: "claude", Mounts: []sandbox.Mount{{Source: "/", ReadOnly: true}, {Source: "./project"}, {Source: "/state"}}, Deny: []string{"/keys", "/keys"}, DenyWrite: []string{"./egg.yaml"}, Domains: []string{"api.example.test", "api.example.test"}}
	rendered, err := RenderClaudeHarness(h, p, "/workspace")
	if err != nil {
		t.Fatal(err)
	}
	var actual map[string]any
	if err := json.Unmarshal(rendered.Settings, &actual); err != nil {
		t.Fatal(err)
	}
	expected := `{"permissions":{"defaultMode":"dontAsk","deny":["Edit(//keys)","Edit(//keys/**)","Edit(//workspace/egg.yaml)","Edit(//workspace/egg.yaml/**)","Read(//keys)","Read(//keys/**)"]},"sandbox":{"enabled":true,"failIfUnavailable":true,"autoAllowBashIfSandboxed":false,"allowUnsandboxedCommands":false,"excludedCommands":[],"enableWeakerNestedSandbox":false,"enableWeakerNetworkIsolation":false,"filesystem":{"allowWrite":["/state","/workspace/project"],"denyRead":["/keys"],"denyWrite":["/keys","/workspace/egg.yaml"]},"network":{"allowedDomains":["api.example.test"],"allowAllUnixSockets":false,"allowUnixSockets":[],"allowLocalBinding":false}}}`
	var want map[string]any
	if err := json.Unmarshal([]byte(expected), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, want) {
		t.Fatalf("policy mismatch:\n%s", rendered.Settings)
	}
	// Reordering and duplication must not change the reviewable artifact.
	p.Mounts[1], p.Mounts[2] = p.Mounts[2], p.Mounts[1]
	p.Deny = append(p.Deny, "/keys")
	again, err := RenderClaudeHarness(h, p, "/workspace")
	if err != nil || string(again.Settings) != string(rendered.Settings) || again.SHA256 != rendered.SHA256 {
		t.Fatalf("nondeterministic: %v", err)
	}
	for _, scope := range []string{"startup", "shell", "file_tools", "mcp", "lsp", "hooks", "setup", "git_helpers", "subagents", "resources", "network"} {
		if rendered.Coverage[scope] == "" {
			t.Errorf("missing owner for %s", scope)
		}
	}
	for _, mode := range []string{"", "default", "acceptEdits", "plan", "dontAsk"} {
		h.PermissionMode = mode
		r, err := RenderClaudeHarness(h, p, "/workspace")
		if err != nil || r.PermissionMode == "" {
			t.Fatalf("mode %q: %v", mode, err)
		}
	}
	for _, root := range []string{"/a*b", "/a?b", "/a[b]", "/a(b)", "/a\nb"} {
		p.Deny = []string{root}
		if _, err := RenderClaudeHarness(h, p, "/workspace"); err == nil {
			t.Errorf("accepted ambiguous root %q", root)
		}
	}
}

func TestClaudeHarnessConformancePreparation(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "workspace")
	dir := filepath.Join(home, "session")
	for _, p := range []string{work, dir} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	h := harnessFixture()
	rc := RunConfig{Harness: &h, Agent: "claude", CWD: work, UserHome: home, FS: []string{"ro:/", "rw:" + work, "deny:" + filepath.Join(home, "keys"), "deny-write:" + filepath.Join(work, "egg.yaml")}, Network: []string{"*"}, ProviderSessionID: "provider-id", AgentArgs: []string{"--model", "fixture-model", "--settings", `{"effortLevel":"high","env":{"DISABLE_AUTOUPDATER":"1"}}`}}
	opts := LaunchPreparationOptions{Home: home, HostHome: home, EggDir: dir, ClaudeVersion: "2.1.285 (Claude Code)", CodexCapability: SavedCodexCapability("")}
	dryState, err := NewDryLaunchState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dry, err := PrepareLaunch(rc, opts, dryState)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, claudeLifecycleSettingsFile)); !os.IsNotExist(err) {
		t.Fatal("dry preparation wrote live settings")
	}
	native, err := PrepareLaunch(rc, opts, NativeLaunchState())
	if err != nil {
		t.Fatal(err)
	}
	if !native.Sandboxed || native.Harness == nil || dry.Harness.SHA256 != native.Harness.SHA256 {
		t.Fatal("driver lost the outer boundary or diverged during native preparation")
	}
	if native.Environment["DISABLE_AUTOUPDATER"] != "1" {
		t.Fatal("auto updater enabled")
	}
	data, err := os.ReadFile(native.LifecycleSettingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	if settings["hooks"] == nil || settings["sandbox"] == nil || settings["permissions"] == nil || settings["effortLevel"] != "high" {
		t.Fatalf("lifecycle merge lost policy or preferences: %s", data)
	}
	if info, err := os.Stat(native.LifecycleSettingsPath); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("settings not private")
	}
	joined := strings.Join(native.Args, "|")
	if !strings.Contains(joined, "--setting-sources||--permission-mode|dontAsk") || strings.Contains(joined, "\"sandbox\"") || strings.Contains(joined, "dangerously-skip") {
		t.Fatalf("unsafe final argv: %s", joined)
	}
	if !reflect.DeepEqual(native.Sandbox.Mounts[len(native.Sandbox.Mounts)-1], sandbox.Mount{Source: native.LifecycleSettingsPath, Target: native.LifecycleSettingsPath, ReadOnly: true}) {
		t.Fatal("settings are not mounted read-only")
	}
	report, err := os.ReadFile(filepath.Join(dir, "harness-policy.json"))
	if err != nil || !strings.Contains(string(report), native.Harness.SHA256) {
		t.Fatal("policy receipt missing")
	}
	// Opt-out keeps the existing provider lifecycle path without native settings.
	rc.Harness = nil
	rc.AgentArgs = nil
	legacy, err := PrepareLaunch(rc, opts, NativeLaunchState())
	if err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(legacy.LifecycleSettingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Harness != nil || strings.Contains(string(data), `"sandbox"`) || strings.Contains(strings.Join(legacy.Args, "|"), "--setting-sources") {
		t.Fatal("v1 behavior changed")
	}
}

func TestClaudeHarnessConformanceAdmission(t *testing.T) {
	h := harnessFixture()
	base := RunConfig{Harness: &h, Agent: "claude", CWD: "/workspace", ProviderSessionID: "exact"}
	opts := LaunchPreparationOptions{ClaudeVersion: "2.1.285 (Claude Code)"}
	tests := map[string]func(*RunConfig, *LaunchPreparationOptions){
		"privileged-socket": func(r *RunConfig, o *LaunchPreparationOptions) { r.ToolSocketPath = "/host/tools.sock" },
		"privileged-tool":   func(r *RunConfig, o *LaunchPreparationOptions) { r.ToolNames = []string{"host-command"} },
		"wrong-agent":       func(r *RunConfig, o *LaunchPreparationOptions) { r.Agent = "codex" },
		"command":           func(r *RunConfig, o *LaunchPreparationOptions) { r.Command = []string{"sh"} },
		"outer-only":        func(r *RunConfig, o *LaunchPreparationOptions) { r.OuterBoundary = true },
		"skip":              func(r *RunConfig, o *LaunchPreparationOptions) { r.DangerouslySkipPermissions = true },
		"no-identity":       func(r *RunConfig, o *LaunchPreparationOptions) { r.ProviderSessionID = "" },
		"no-version":        func(r *RunConfig, o *LaunchPreparationOptions) { o.ClaudeVersion = "" },
		"wrong-version":     func(r *RunConfig, o *LaunchPreparationOptions) { o.ClaudeVersion = "2.1.286 (Claude Code)" },
		"platform":          func(r *RunConfig, o *LaunchPreparationOptions) { o.Platform = "windows" },
	}
	for name, alter := range tests {
		t.Run(name, func(t *testing.T) {
			r, o := base, opts
			alter(&r, &o)
			state, _ := NewDryLaunchState(t.TempDir())
			if err := validateClaudeHarness(r, o, state); err == nil {
				t.Fatal("unsafe admission")
			}
		})
	}
	for _, args := range [][]string{{"--permission-mode=bypassPermissions"}, {"--dangerously-skip-permissions"}, {"--setting-sources", "project"}, {"--add-dir", "/"}, {"--settings", `{"sandbox":{"enabled":false}}`}, {"--settings", `{"env":{"NODE_OPTIONS":"injection"}}`}, {"--settings", "/host/settings.json"}, {"--settings", `{"hooks":{}}`}, {"--model"}, {"--", "--settings=ignored"}, {"--plugin-dir=./plugin"}} {
		r := base
		r.AgentArgs = args
		state, _ := NewDryLaunchState(t.TempDir())
		if err := validateClaudeHarness(r, opts, state); err == nil {
			t.Errorf("accepted unsafe arguments %q", args)
		}
	}
}

func TestClaudeHarnessConformanceConfig(t *testing.T) {
	cfg, err := LoadEggConfigFromYAML("harness:\n  driver: claude-code\n  version: 2.1.285\n  permission_mode: plan\n")
	if err != nil {
		t.Fatal(err)
	}
	inherited := MergeEggConfig(cfg, &EggConfig{})
	if inherited.Harness == nil || inherited.Harness.PermissionMode != "plan" {
		t.Fatal("base lost native intent")
	}
	for _, text := range []string{"harness: false", "harness: {driver: unknown, version: 2.1.285}", "harness: {driver: claude-code, version: 2.1.284}", "harness: {driver: claude-code, version: 3.0.0}", "harness: {driver: claude-code, version: 2.1.285, permission_mode: bypassPermissions}", "harness: {driver: claude-code, version: 2.1.285, sandbox: false}"} {
		if _, err := LoadEggConfigFromYAML(text); err == nil {
			t.Errorf("accepted invalid config %s", text)
		}
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "egg.yaml"), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		if cfg := DiscoverEggConfig(dir, nil); cfg.Harness == nil {
			t.Fatal("discovery silently dropped invalid native intent")
		}
	}
}

func TestClaudeHarnessConformanceInstalledProbe(t *testing.T) {
	h := harnessFixture()
	rc := RunConfig{Harness: &h, Agent: "claude", CWD: "/workspace", ProviderSessionID: "exact"}
	binary := filepath.Join(t.TempDir(), "claude")
	t.Setenv("ANTHROPIC_API_KEY", "DO_NOT_INHERIT")
	for _, version := range []string{"2.1.285", "2.1.284"} {
		script := "#!/bin/sh\n[ \"$1\" = --version ] || exit 9\n[ -z \"$ANTHROPIC_API_KEY\" ] || exit 8\nprintf '%s (Claude Code)\\n' '" + version + "'\n"
		if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		err := validateClaudeHarness(rc, LaunchPreparationOptions{Binary: binary}, NativeLaunchState())
		if (err == nil) != (version == h.Version) {
			t.Fatalf("probe %s: %v", version, err)
		}
	}
}
