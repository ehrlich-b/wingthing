package egg

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/testsocket"
)

func TestLaunchPreparationDryAndNativeSpecification(t *testing.T) {
	for _, agent := range []string{"codex", "gemini", ""} {
		t.Run(agent, func(t *testing.T) {
			home := testsocket.Dir(t)
			work := filepath.Join(home, "work")
			if err := os.Mkdir(work, 0700); err != nil {
				t.Fatal(err)
			}
			rc := RunConfig{Agent: agent, CWD: work, UserHome: home, FS: []string{"rw:" + work, "deny:" + filepath.Join(home, "private")}, Network: []string{"*"}, DangerouslySkipPermissions: true, MaxFDs: 123, PidLimit: 45, MemLimit: 6789, ToolSocketPath: filepath.Join(home, "tools", "tool.sock"), ToolNames: []string{"fixture"}}
			if agent == "" {
				rc.Command = []string{"/bin/sh", "-l"}
			}
			opts := LaunchPreparationOptions{Binary: filepath.Join(home, "bin", "provider"), Home: home, HostHome: home, EggDir: filepath.Join(home, "ours"), BrowserRequestsPath: filepath.Join(home, "browser-requests"), ToolBinary: filepath.Join(home, "wt-bin", "wt"), CodexCapability: SavedCodexCapability("codex-cli 0.159.3")}
			state, err := NewDryLaunchState(testsocket.Dir(t))
			if err != nil {
				t.Fatal(err)
			}
			dry, err := PrepareLaunch(rc, opts, state)
			if err != nil {
				t.Fatal(err)
			}
			for _, dir := range dry.ProfileDirs {
				if _, err := os.Stat(dir); !os.IsNotExist(err) {
					t.Fatalf("dry preparation created original directory %s", dir)
				}
			}
			native, err := PrepareLaunch(rc, opts, NativeLaunchState())
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(dry, native) {
				t.Fatalf("dry/native preparation differ:\ndry: %#v\nnative: %#v", dry, native)
			}
			if native.Sandbox.MaxFDs != 123 || native.Sandbox.PidLimit != 45 || native.Sandbox.MemLimit != 6789 {
				t.Fatal("resource specification lost")
			}
			for _, path := range []string{opts.BrowserRequestsPath, filepath.Dir(opts.ToolBinary), filepath.Dir(rc.ToolSocketPath)} {
				found := false
				for _, m := range native.Sandbox.Mounts {
					if m.Source == path {
						found = true
					}
				}
				if !found {
					t.Fatalf("runtime mount lost: %s", path)
				}
			}
		})
	}
}

func TestLaunchStateMirrorContainsSymlinkTargets(t *testing.T) {
	for _, relative := range []bool{false, true} {
		t.Run(map[bool]string{false: "absolute", true: "relative"}[relative], func(t *testing.T) {
			root := testsocket.Dir(t)
			home, target := filepath.Join(root, "home"), filepath.Join(root, "foreign")
			for _, path := range []string{home, target} {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			credential := filepath.Join(target, "auth.json")
			if err := os.WriteFile(credential, []byte("SENTINEL_CREDENTIAL"), 0600); err != nil {
				t.Fatal(err)
			}
			link := target
			if relative {
				link = "../foreign"
			}
			if err := os.Symlink(link, filepath.Join(home, ".codex")); err != nil {
				t.Fatal(err)
			}
			mirror := testsocket.Dir(t)
			state, err := NewDryLaunchState(mirror)
			if err != nil {
				t.Fatal(err)
			}
			if err := state.ProviderSpool(home, ".codex/wingthing-events/ours"); err == nil {
				t.Fatal("dry pinned reader accepted symlink")
			}
			entries, err := os.ReadDir(target)
			if err != nil || len(entries) != 1 || entries[0].Name() != "auth.json" {
				t.Fatalf("preparation escaped mirror: %v / %v", entries, err)
			}
			if err := filepath.WalkDir(mirror, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.Type()&os.ModeSymlink != 0 {
					destination, err := os.Readlink(path)
					if err != nil {
						return err
					}
					if !strings.HasPrefix(destination, mirror+string(filepath.Separator)) {
						t.Fatalf("mirror link escapes: %s", destination)
					}
				}
				if d.Name() == "auth.json" {
					t.Fatal("mirror copied credential file")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLaunchPreparationDefersClaudeSettingsOnly(t *testing.T) {
	home := testsocket.Dir(t)
	state, err := NewDryLaunchState(testsocket.Dir(t))
	if err != nil {
		t.Fatal(err)
	}
	// This missing settings path proves that the dry effect does not open it.
	rc := RunConfig{Agent: "claude", CWD: home, UserHome: home, FS: []string{"rw:" + home}, Network: []string{"*"}, ProviderSessionID: "ours", AgentArgs: []string{"--settings", filepath.Join(home, "must-not-read.json")}}
	prepared, err := PrepareLaunch(rc, LaunchPreparationOptions{Home: home, HostHome: home, EggDir: filepath.Join(home, "ours"), CodexCapability: SavedCodexCapability("")}, state)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.LifecycleSettingsPath != "" || !containsExact(prepared.Deferred, "Claude lifecycle settings merge and file generation") {
		t.Fatalf("settings effect not deferred: %#v", prepared)
	}
	if len(prepared.Sandbox.Mounts) == 0 || !prepared.NativeRun {
		t.Fatal("unrelated real preparation did not execute")
	}
	if _, err := os.Stat(filepath.Join(home, ".claude")); !os.IsNotExist(err) {
		t.Fatal("dry preparation changed original HOME")
	}
}

func TestLaunchPreparationEnvironmentSelectionParity(t *testing.T) {
	for _, tc := range []struct {
		name     string
		skip     bool
		selected map[string]string
	}{
		{"profile reinjection", false, map[string]string{"OPENAI_API_KEY": "CALLER_KEY"}},
		{"isolated home", true, map[string]string{}},
		{"empty caller value wins", false, map[string]string{"CODEX_HOME": ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := testsocket.Dir(t)
			rc := RunConfig{Agent: "codex", Kind: "agent", UserHome: home, SkipHostAgentEnv: tc.skip}
			inherited := []string{"OPENAI_API_KEY=HOST_KEY", "CODEX_HOME=" + filepath.Join(home, "alternate"), "ELAN_HOME=" + filepath.Join(home, ".elan"), "HOME=/host/home", "PATH=/host/bin", "TERM="}
			env := LaunchEnvironment(rc, tc.selected, inherited)
			if env["HOME"] != home || env["PATH"] != "/host/bin" || env["TERM"] != "xterm-256color" || env["GIT_TERMINAL_PROMPT"] != "0" {
				t.Fatalf("runtime essentials changed: %#v", env)
			}
			if env["ELAN_HOME"] != "" {
				t.Fatal("unselected non-profile environment was reinjected")
			}
			if tc.skip {
				if env["OPENAI_API_KEY"] != "" || env["CODEX_HOME"] != "" {
					t.Fatal("shared-host launch inherited host credentials/state")
				}
			} else if tc.name == "empty caller value wins" {
				if env["CODEX_HOME"] != "" || env["OPENAI_API_KEY"] != "HOST_KEY" {
					t.Fatal("caller precedence changed")
				}
			} else if env["OPENAI_API_KEY"] != "CALLER_KEY" || env["CODEX_HOME"] != filepath.Join(home, "alternate") {
				t.Fatal("profile reinjection changed")
			}
			rc.CWD, rc.OuterBoundary = home, true
			state, err := NewDryLaunchState(testsocket.Dir(t))
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := PrepareLaunch(rc, LaunchPreparationOptions{Home: home, HostHome: home, Environment: tc.selected, InheritedEnvironment: inherited, CodexCapability: SavedCodexCapability("")}, state)
			if err != nil || !reflect.DeepEqual(prepared.Environment, env) {
				t.Fatalf("preparation/runtime environment mismatch: %v / %#v / %#v", err, prepared.Environment, env)
			}
		})
	}
}

func TestLaunchPreparationSupplementalLayoutDoesNotCreateState(t *testing.T) {
	home := testsocket.Dir(t)
	override := filepath.Join(home, "override")
	elan := filepath.Join(home, ".elan")
	rc := RunConfig{Agent: "codex", CWD: home, UserHome: home, OuterBoundary: true}
	opts := LaunchPreparationOptions{
		Home: home, HostHome: home, Environment: map[string]string{"ELAN_HOME": elan},
		CodexCapability: SavedCodexCapability(""),
		Requirements:    &LaunchRequirements{ProviderStateDir: override},
	}
	prepared, err := PrepareLaunch(rc, opts, NativeLaunchState())
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Environment["CODEX_HOME"] != override {
		t.Fatal("provider override not forwarded")
	}
	for _, path := range []string{override, elan} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("supplemental diagnosis created original state: %s / %v", path, err)
		}
	}
}
