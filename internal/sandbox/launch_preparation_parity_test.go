//go:build darwin

package sandbox_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/launchplan"
)

// These fixtures preserve round 3's three inputs, plus ancestor variants and
// passing controls. The oracle is the production preparation called by spawn,
// with native filesystem effects on the test-owned HOME; no process is started.
func realPreparation(t *testing.T, req launchplan.Request) (egg.PreparedLaunch, error) {
	t.Helper()
	cfg := runtimeConfig(t, req)
	base := req.PolicyBase
	if base == "" {
		base = req.CWD
	}
	cfg = egg.RebaseLaunchConfig(cfg, base)
	rc := egg.RunConfig{
		Agent: req.Agent, CWD: req.CWD, UserHome: req.Home, FS: cfg.FS,
		Network: cfg.Network.Domains, LocalPorts: cfg.Network.LocalPorts,
		NetworkMode: cfg.Network.Mode, AgentDomains: cfg.Network.AgentDomains,
		DangerouslySkipPermissions: true, ProviderSessionID: "launch-plan", OmitBrowserBridge: true,
	}
	return egg.PrepareLaunch(rc, egg.LaunchPreparationOptions{
		Binary: req.Executable, Home: req.Home, HostHome: req.MergeHome,
		EggDir:          filepath.Join(filepath.Dir(req.PolicyPath), "launch-plan"),
		CodexCapability: egg.SavedCodexCapability("codex-cli 0.159.3"), Platform: "darwin",
	}, egg.NativeLaunchState())
}

func inspectUnchanged(t *testing.T, req launchplan.Request, root string) launchplan.Report {
	t.Helper()
	before := inventory(t, root)
	r, err := launchplan.Inspect(req)
	if err != nil {
		t.Fatal(err)
	}
	again, err := launchplan.Inspect(req)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r, again) {
		t.Fatal("disposable state leaked into the report")
	}
	if !reflect.DeepEqual(before, inventory(t, root)) {
		t.Fatal("preflight changed original input metadata/content")
	}
	return r
}

func expectPreparationFailure(t *testing.T, req launchplan.Request, dir, reason string) egg.PreparedLaunch {
	t.Helper()
	report := inspectUnchanged(t, req, dir)
	prepared, err := realPreparation(t, req)
	if err == nil || !strings.Contains(err.Error(), reason) {
		t.Fatalf("native preparation: %v; expected %q", err, reason)
	}
	gate := find(t, report, "provider.preparation")
	if gate.Status != launchplan.Fail || gate.Reason != "runtime launch preparation failed: "+err.Error() || report.Readiness != "blocked" {
		t.Fatalf("preflight/native preparation disagree: %+v / %v", gate, err)
	}
	return prepared
}

func TestLaunchPreparationDanglingStateParity(t *testing.T) {
	for _, component := range []string{".codex", ".codex/wingthing-events", ".codex/wingthing-run-notify", "HOME"} {
		t.Run(component, func(t *testing.T) {
			req, _, dir := fixture(t)
			path := filepath.Join(req.Home, component)
			if component == "HOME" {
				req.Home = filepath.Join(dir, "dangling-home")
				path = req.Home
			}
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(dir, "missing-target"), path); err != nil {
				t.Fatal(err)
			}
			expectPreparationFailure(t, req, dir, "file exists")
		})
	}
	// Ordinary absence is supported by the actual MkdirAll path.
	t.Run("absent directory", func(t *testing.T) {
		req, _, dir := fixture(t)
		report := inspectUnchanged(t, req, dir)
		prepared, err := realPreparation(t, req)
		if err != nil || !prepared.NativeRun {
			t.Fatalf("native preparation: %v", err)
		}
		absentFails(t, report)
		if !reflect.DeepEqual(report.ProviderArgv[1:], prepared.Args) {
			t.Fatal("argv differs from real preparation")
		}
	})
}

func TestLaunchPreparationSymlinkedStateReaderParity(t *testing.T) {
	for _, component := range []string{".codex", ".codex/wingthing-events", ".codex/wingthing-run-notify", "HOME"} {
		t.Run(component, func(t *testing.T) {
			req, _, dir := fixture(t)
			path := filepath.Join(req.Home, component)
			target := filepath.Join(req.Home, "actual-state")
			if component == "HOME" {
				target = req.Home
				req.Home = filepath.Join(dir, "home-alias")
				path = req.Home
			}
			if err := os.MkdirAll(target, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			// The real preparation now opens through the same O_NOFOLLOW readers
			// before advertising native support, retaining their security policy.
			reason := "not a directory"
			expectPreparationFailure(t, req, dir, reason)
		})
	}
}

func TestLaunchPreparationDeniedCallbackSpoolParity(t *testing.T) {
	for _, leaf := range []string{"wingthing-run-notify", "wingthing-events"} {
		for _, variant := range []string{"spool denial", "descendant denial", "literal pin"} {
			t.Run(leaf+"/"+variant, func(t *testing.T) {
				req, _, dir := fixture(t)
				spool := filepath.Join(req.Home, ".codex", leaf)
				mode, denied := "deny:", spool
				if variant == "descendant denial" {
					denied = filepath.Join(spool, "launch-plan", "completion.json")
				}
				if variant == "literal pin" {
					mode = "deny-write:"
					if err := os.MkdirAll(spool, 0700); err != nil {
						t.Fatal(err)
					}
				}
				put(t, req.PolicyPath, "fs: [rw:./, "+mode+denied+"]\n")
				if variant == "literal pin" {
					report := inspectUnchanged(t, req, dir)
					prepared, err := realPreparation(t, req)
					if err != nil || !prepared.NativeRun {
						t.Fatalf("literal pin: %v", err)
					}
					absentFails(t, report)
					return
				}
				prepared := expectPreparationFailure(t, req, dir, "callback spool")
				if !strings.Contains(strings.Join(prepared.Args, " "), filepath.Join(spool, "launch-plan")) {
					t.Fatal("oracle did not use the denied callback path")
				}
				if !reflect.DeepEqual(prepared.Sandbox.RequiredWriteDirs, []string{
					filepath.Join(req.Home, ".codex", "wingthing-events", "launch-plan"),
					filepath.Join(req.Home, ".codex", "wingthing-run-notify", "launch-plan"),
				}) {
					t.Fatalf("required spools not recorded by real builder: %v", prepared.Sandbox.RequiredWriteDirs)
				}
			})
		}
	}
}
