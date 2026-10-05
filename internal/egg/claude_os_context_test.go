package egg

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

func TestPreviewClaudeLifecycleSettingsAndHooksKeepDataHome(t *testing.T) {
	dir := t.TempDir()
	dataHome := config.CanonicalProviderPath(t.TempDir())
	cwd := t.TempDir()
	args, err := prepareClaudeLifecycleArgs(nil, dataHome, dir, "ours", cwd)
	if err != nil {
		t.Fatal(err)
	}
	settingsPath := args[len(args)-1]
	env := map[string]string{"HOME": dataHome, "PATH": "/usr/bin:/bin"}
	osHome, err := ApplyPreviewClaudeOSContext(env, dataHome)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct{ Command string }
		}
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	command := settings.Hooks["SessionStart"][0].Hooks[0].Command
	spool := lifecycleHookDir(dataHome, filepath.Base(dir))
	if !strings.Contains(command, spool) || strings.Contains(command, filepath.Join(osHome, ".claude")) {
		t.Fatalf("native hooks switched to OS home: %s", command)
	}
	// Read the owner-only settings and run the generated hook with the final
	// OS-account environment, without starting a nested Seatbelt or an egg.
	cmd := exec.Command("/bin/sh", "-c", `cat "$1" >/dev/null && `+command, "fixture", settingsPath)
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	cmd.Dir = cwd
	cmd.Stdin = strings.NewReader(`{"session_id":"ours","hook_event_name":"SessionStart"}`)
	if output, err := cmd.CombinedOutput(); err != nil || len(output) != 0 {
		t.Fatalf("OS context could not read settings or publish hook: %v %s", err, output)
	}
	view, err := ReadSessionLifecycle(dir, "claude", cwd, dataHome, "ours", true, 0, 10)
	if err != nil || !view.Ready || view.StateSource != "claude_hook" || view.State != "idle" {
		t.Fatalf("hook writer and importer disagree about data home: %+v %v", view, err)
	}
}

func TestPreviewClaudeOSContextRejectsOuterBoundary(t *testing.T) {
	previous := config.ReleaseChannel
	config.ReleaseChannel = "preview"
	t.Cleanup(func() { config.ReleaseChannel = previous })
	err := ValidatePreviewClaudeBoundary("claude", nil, true)
	if runtime.GOOS == "darwin" && err == nil {
		t.Fatal("preview Mac Claude accepted an unguarded OS-home route")
	}
	if runtime.GOOS != "darwin" && err != nil {
		t.Fatalf("changed non-Mac route: %v", err)
	}
	if err := ValidatePreviewClaudeBoundary("claude", nil, false); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePreviewClaudeBoundary("codex", nil, true); err != nil {
		t.Fatal(err)
	}
}

func TestPreviewClaudeMacTmpAliasSelectsOneCredentialService(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS /tmp lexical alias")
	}
	root, err := os.MkdirTemp("/tmp", "wt-profile-alias-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if canonical == root {
		t.Skip("system /tmp is not an alias")
	}
	_, left, err := PreviewClaudeOSContext(config.PreviewProviderHome(root))
	if err != nil {
		t.Fatal(err)
	}
	_, right, err := PreviewClaudeOSContext(config.PreviewProviderHome(canonical))
	if err != nil || left != right {
		t.Fatalf("/tmp alias selected different Keychain service inputs: %q != %q (%v)", left, right, err)
	}
}

func TestPreviewClaudeContextCanonicalAliasAndMissingHome(t *testing.T) {
	root := t.TempDir()
	actual := filepath.Join(root, "state")
	if err := os.Mkdir(actual, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(actual, alias); err != nil {
		t.Fatal(err)
	}
	left := config.PreviewProviderHome(actual)
	right := config.PreviewProviderHome(alias)
	if left != right {
		t.Fatalf("provider aliases select different identities: %q != %q", left, right)
	}
	osHome, cfg, err := PreviewClaudeOSContext(left)
	if err != nil {
		t.Fatal(err)
	}
	otherOS, otherConfig, err := PreviewClaudeOSContext(right)
	if err != nil || otherConfig != cfg || otherOS != osHome {
		t.Fatalf("context aliases differ: %q %q %v", otherOS, otherConfig, err)
	}
	if !filepath.IsAbs(osHome) || osHome == left || cfg != filepath.Join(left, ".claude") {
		t.Fatalf("mixed OS/provider context: %q %q", osHome, cfg)
	}
	if _, err := os.Stat(left); !os.IsNotExist(err) {
		t.Fatalf("read-only context initialized provider home: %v", err)
	}
}

func TestPreviewClaudeContextKeepsNativeDataAndRejectsOverrides(t *testing.T) {
	dataHome := config.PreviewProviderHome(filepath.Join(t.TempDir(), "state"))
	env := map[string]string{"HOME": dataHome, "CLAUDE_CONFIG_DIR": filepath.Join(dataHome, ".claude"), "CFFIXED_USER_HOME": "/foreign", "UNRELATED": "preserved", "USER": "fixture-ambient-account"}
	osHome, err := ApplyPreviewClaudeOSContext(env, dataHome)
	if err != nil {
		t.Fatal(err)
	}
	if env["HOME"] != osHome || env["CLAUDE_CONFIG_DIR"] != filepath.Join(dataHome, ".claude") || env["UNRELATED"] != "preserved" || env["CFFIXED_USER_HOME"] != "" {
		t.Fatalf("unexpected environment: %#v", env)
	}
	if _, present := env["USER"]; runtime.GOOS == "darwin" && present {
		t.Fatal("Mac runtime retained a different credential account selector")
	}
	if runtime.GOOS != "darwin" && env["USER"] != "fixture-ambient-account" {
		t.Fatal("non-Mac runtime changed USER")
	}
	env["CLAUDE_SECURESTORAGE_CONFIG_DIR"] = "/foreign"
	if _, err := ApplyPreviewClaudeOSContext(env, dataHome); err == nil {
		t.Fatal("accepted secure-storage override")
	}
	delete(env, "CLAUDE_SECURESTORAGE_CONFIG_DIR")
	env["CLAUDE_CONFIG_DIR"] = filepath.Join(osHome, ".claude")
	if _, err := ApplyPreviewClaudeOSContext(env, dataHome); err == nil {
		t.Fatal("accepted host config selection")
	}
	if _, _, err := PreviewClaudeOSContext("relative"); err == nil {
		t.Fatal("accepted relative data home")
	}
	if _, _, err := PreviewClaudeOSContext(osHome); err == nil {
		t.Fatal("accepted host home as preview data")
	}
}

func TestPreviewClaudeGuardRejectsMoreSpecificHostConfigMounts(t *testing.T) {
	osHome := filepath.Join(t.TempDir(), "OS-user")
	protected, err := GuardPreviewClaudeMounts([]sandbox.Mount{{Source: osHome, Target: osHome}}, osHome)
	if err != nil || len(protected) != 2 {
		t.Fatalf("broader mount guard: %v %v", protected, err)
	}
	for _, path := range []string{filepath.Join(osHome, ".claude"), filepath.Join(osHome, ".claude", "ide"), filepath.Join(osHome, ".claude.json")} {
		if _, err := GuardPreviewClaudeMounts([]sandbox.Mount{{Source: path, Target: path, ReadOnly: true}}, osHome); err == nil {
			t.Fatalf("accepted specific host config mount %q", path)
		}
	}
	if !strings.HasSuffix(protected[0], "/.claude") || !strings.HasSuffix(protected[1], "/.claude.json") {
		t.Fatalf("unexpected protected paths: %v", protected)
	}
}
