package egg

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCodexSandboxConformance(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "work with spaces")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name           string
		fs             []string
		network        NetworkField
		unattended     bool
		mode, approval string
		net            bool
		wantErr        string
	}{
		{"read-only", []string{"ro:/", "deny:~/.ssh"}, NetworkField{}, false, "read-only", "on-request", false, ""},
		{"closed", []string{"rw:./", "deny-write:./egg.yaml"}, NetworkField{}, true, "workspace-write", "never", false, ""},
		{"open", []string{"rw:./"}, NetworkField{Domains: []string{"*"}}, false, "workspace-write", "on-request", true, ""},
		{"destinations-use-outer", []string{"rw:./"}, NetworkField{Domains: []string{"example.com"}}, true, "workspace-write", "never", true, ""},
		{"loopback-uses-outer", []string{"rw:./"}, NetworkField{LocalPorts: []int{8080}}, true, "workspace-write", "never", true, ""},
		{"implicit-cwd", []string{"rw:" + filepath.Join(root, "other")}, NetworkField{}, false, "", "", false, "implicitly grants cwd"},
		{"readonly-network", []string{"ro:/"}, NetworkField{Domains: []string{"*"}}, false, "", "", false, "cannot represent"},
		{"unknown-mode", []string{"write:./"}, NetworkField{}, false, "", "", false, "cannot translate"},
		{"empty-root", []string{"rw:"}, NetworkField{}, false, "", "", false, "must not be empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &EggConfig{FS: tc.fs, Network: tc.network}
			got, err := RenderCodexSandbox(cfg, work, root, tc.unattended)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want %s, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.SandboxMode != tc.mode || got.ApprovalPolicy != tc.approval || got.NetworkAccess != tc.net {
				t.Fatalf("unexpected policy: %+v", got)
			}
			if len(got.Args) != 12 || got.Args[9] != "sandbox_workspace_write.exclude_slash_tmp=true" || got.Args[11] != "sandbox_workspace_write.exclude_tmpdir_env_var=true" {
				t.Fatalf("implicit temp grant: %v", got.Args)
			}
			if tc.mode == "workspace-write" && !reflect.DeepEqual(got.WritableRoots, []string{work}) {
				t.Fatalf("roots: %v", got.WritableRoots)
			}
			again, err := RenderCodexSandbox(cfg, work, root, tc.unattended)
			if err != nil || !reflect.DeepEqual(got, again) {
				t.Fatal("non-deterministic rendering")
			}
			wire, _ := json.Marshal(got)
			t.Log(string(wire))
		})
	}
}

func TestCodexSandboxCanonicalRoots(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "work")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(work, filepath.Join(home, "alias")); err != nil {
		t.Fatal(err)
	}
	got, err := RenderCodexSandbox(&EggConfig{FS: []string{"rw:~/alias", "rw:./", "rw:../work", "ro:/", "deny-rename:./egg.yaml"}}, work, home, true)
	if err != nil || !reflect.DeepEqual(got.WritableRoots, []string{work}) {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestCodexSandboxOverrideConformance(t *testing.T) {
	for _, args := range [][]string{{"--dangerously-bypass-approvals-and-sandbox"}, {"-s", "danger-full-access"}, {"-awhatever"}, {"--full-auto"}, {"--config=sandbox_mode=\"danger-full-access\""}, {"-c", "sandbox_workspace_write={network_access=true}"}, {"--profile", "unsafe"}, {"-C", "/"}, {"--enable", "anything"}, {"--model"}, {"resume", "--last"}} {
		if err := validateCodexSandboxOverrides(args); err == nil {
			t.Fatalf("accepted override: %v", args)
		}
	}
	for _, args := range [][]string{nil, {"-m", "example-model"}, {"--model=example-model"}, {"--", "--dangerously-bypass-approvals-and-sandbox"}} {
		if err := validateCodexSandboxOverrides(args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

func TestCodexSandboxLaunchConformance(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "work")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	for _, resume := range []string{"", "exact-thread"} {
		rc := RunConfig{Agent: "codex", CodexNativeSandbox: true, CWD: work, UserHome: home, FS: []string{"ro:/", "rw:" + work, "deny:" + filepath.Join(home, "private")}, DangerouslySkipPermissions: true, ResumeSessionID: resume}
		opts := LaunchPreparationOptions{Binary: "/usr/bin/codex", Home: home, HostHome: home, EggDir: filepath.Join(home, "egg"), CodexCapability: SavedCodexCapability("codex-cli 0.159.3")}
		state, err := NewDryLaunchState(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		got, err := PrepareLaunch(rc, opts, state)
		if err == nil || !strings.Contains(err.Error(), "nesting") {
			t.Fatalf("unverified nesting admitted: %v", err)
		}
		if got.CodexSandbox == nil || !got.Sandboxed || containsExact(got.Args, "--dangerously-bypass-approvals-and-sandbox") || len(got.Sandbox.Deny) == 0 {
			t.Fatalf("lost enforcement: %+v", got)
		}
		if got.CodexSandbox.NetworkAccess {
			t.Fatal("control provider network became tool network")
		}
		if resume != "" && !containsExact(got.Args, resume) {
			t.Fatal("lost exact resume identity")
		}
		rc.CodexNativeSandbox = false
		legacy, err := PrepareLaunch(rc, opts, state)
		if err != nil || legacy.CodexSandbox != nil || !containsExact(legacy.Args, "--dangerously-bypass-approvals-and-sandbox") {
			t.Fatalf("legacy changed: %v / %v", legacy.Args, err)
		}
		rc.CodexNativeSandbox = true
		for _, mutate := range []func(*RunConfig, *LaunchPreparationOptions){
			func(r *RunConfig, _ *LaunchPreparationOptions) { r.OuterBoundary = true },
			func(_ *RunConfig, o *LaunchPreparationOptions) { o.CodexCapability = SavedCodexCapability("unknown") },
			func(r *RunConfig, _ *LaunchPreparationOptions) { r.AgentArgs = []string{"--full-auto"} },
		} {
			r, o := rc, opts
			mutate(&r, &o)
			if _, err := PrepareLaunch(r, o, state); err == nil {
				t.Fatal("unsupported launch did not fail closed")
			}
		}
	}
}

func TestCodexSandboxNestingAdmission(t *testing.T) {
	for _, platform := range []string{"linux", "darwin"} {
		for _, unattended := range []bool{false, true} {
			for _, native := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/unattended=%t/native-state=%t", platform, unattended, native), func(t *testing.T) {
					home := t.TempDir()
					rc := RunConfig{Agent: "codex", CodexNativeSandbox: true, CWD: home, UserHome: home, DangerouslySkipPermissions: unattended}
					opts := LaunchPreparationOptions{Binary: "/usr/bin/codex", Home: home, HostHome: home, EggDir: filepath.Join(home, "egg"), Platform: platform, CodexCapability: SavedCodexCapability("codex-cli 0.162.1")}
					state, err := NewDryLaunchState(t.TempDir())
					if err != nil {
						t.Fatal(err)
					}
					var launchState LaunchState = state
					if native {
						launchState = NativeLaunchState()
						// Positive outer capability facts cannot establish inner nesting.
						opts.Requirements = &LaunchRequirements{Capabilities: &LaunchCapabilities{OS: platform, NativeSandbox: FactSupported, ParentSandbox: "none"}}
					}
					got, err := PrepareLaunch(rc, opts, launchState)
					if err == nil || !strings.Contains(err.Error(), "nesting") {
						t.Fatalf("unverified nesting admitted: %v", err)
					}
					found := false
					for _, check := range got.Checks {
						if check.Code == "sandbox.nested" && check.Status == LaunchFail {
							found = true
						}
					}
					if !found || got.CodexSandbox == nil || containsExact(got.Args, "--dangerously-bypass-approvals-and-sandbox") {
						t.Fatalf("missing rejection or rendered policy: %+v", got)
					}
				})
			}
		}
	}
}

func TestCodexSandboxOptInInheritance(t *testing.T) {
	cfg, err := LoadEggConfigFromYAML("codex_native_sandbox: true\n")
	if err != nil || !cfg.CodexNativeSandbox {
		t.Fatalf("%+v %v", cfg, err)
	}
	if !MergeEggConfig(cfg, &EggConfig{}).CodexNativeSandbox || !MergeEggConfig(&EggConfig{}, cfg).CodexNativeSandbox {
		t.Fatal("lost opt-in through inheritance")
	}
	if DefaultEggConfig().CodexNativeSandbox {
		t.Fatal("legacy enabled implicitly")
	}
}
