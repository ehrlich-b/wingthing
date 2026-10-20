package egg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Captured from the v1 production PrepareLaunch before introducing Driver.
// Only disposable path identities are normalized; order and policy stay exact.
func TestLegacyDriverV1LaunchGolden(t *testing.T) {
	for _, agent := range []string{"claude", "codex", "gemini", "command"} {
		t.Run(agent, func(t *testing.T) {
			home := t.TempDir()
			cfg, err := LoadEggConfigFromYAML(`fs: ["rw:~/workspace", "deny:~/.ssh", "deny-write:~/workspace/egg.yaml"]
network: ["*"]
env: ["LANG"]
resources: {cpu: 30s, memory: 2GB, max_fds: 128, max_pids: 64}
dangerously_skip_permissions: true
`)
			if err != nil {
				t.Fatal(err)
			}
			rc := RunConfig{Agent: agent, Kind: "agent", CWD: filepath.Join(home, "workspace"), UserHome: home,
				FS: cfg.FS, Network: cfg.Network.Domains, CPULimit: cfg.Resources.CPUDuration(), MemLimit: cfg.Resources.MemBytes(),
				MaxFDs: cfg.Resources.MaxFDs, PidLimit: cfg.Resources.MaxPids, DangerouslySkipPermissions: cfg.DangerouslySkipPermissions,
				AgentArgs: []string{"--model", "fixture-model"}}
			if agent == "command" {
				rc.Agent, rc.Kind, rc.Command = "", "command", []string{"/bin/sh", "-l"}
			}
			if err := os.Mkdir(rc.CWD, 0700); err != nil {
				t.Fatal(err)
			}
			state, err := NewDryLaunchState(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := PrepareLaunch(rc, LaunchPreparationOptions{Binary: "/usr/bin/provider", Home: home, HostHome: home,
				EggDir: filepath.Join(home, "session"), EnvironmentConfig: cfg,
				InheritedEnvironment: []string{"HOME=" + home, "PATH=/usr/bin:/bin", "LANG=C.UTF-8", "UNSELECTED=absent"},
				CodexCapability:      SavedCodexCapability("codex-cli 0.159.3")}, state)
			if err != nil {
				t.Fatal(err)
			}
			// The driver report is additive metadata, outside the v1 launch contract.
			data, err := json.MarshalIndent(struct {
				Args                           []string
				Environment                    map[string]string
				Sandbox                        any
				Policy                         EffectivePolicy
				NativeRun, CodexRun, Sandboxed bool
			}{
				prepared.Args, prepared.Environment, prepared.Sandbox, prepared.Policy, prepared.NativeRun, prepared.CodexRun, prepared.Sandboxed}, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got := strings.ReplaceAll(string(data), home, "$HOME") + "\n"
			// Hook trust hashes include the spool's absolute path. Verify and
			// translate those exact hashes to the same canonical HOME identity;
			// arbitrary hashes and every other argument remain untouched.
			if prepared.CodexRun {
				command := lifecycleHookCommand(filepath.Join(home, ".codex", "wingthing-events", "session"))
				for _, event := range codexLifecycleEvents {
					original, err := codexLifecycleHookHash(event.key, command)
					if err != nil {
						t.Fatal(err)
					}
					canonical, err := codexLifecycleHookHash(event.key, strings.ReplaceAll(command, home, "$HOME"))
					if err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(got, original) {
						t.Fatalf("missing exact trust hash for %s", event.key)
					}
					got = strings.ReplaceAll(got, original, canonical)
				}
			}
			path := filepath.Join("testdata", "driver-v1", agent+".golden.json")
			if os.Getenv("UPDATE_DRIVER_GOLDEN") == "1" {
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(got), 0644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Fatalf("v1 launch changed; compare %s\ngot:\n%s", path, got)
			}
		})
	}
}
