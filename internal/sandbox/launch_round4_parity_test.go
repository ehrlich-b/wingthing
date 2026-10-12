//go:build darwin

package sandbox_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/launchplan"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

// Preserve the round-4 review's two reproducer shapes. Preparation uses the
// real caller's boundary selection and native effects on fixture-owned state;
// neither a sandbox, listener nor a provider process is started.
func round4Preparation(t *testing.T, req launchplan.Request, caps launchplan.Capabilities) (egg.PreparedLaunch, error) {
	t.Helper()
	mergeHome := req.MergeHome
	if mergeHome == "" {
		mergeHome = req.Home
	}
	cfg, rules, err := egg.ResolveLaunchConfig(req.PolicyPath, req.BasesDir, mergeHome)
	if err != nil {
		t.Fatal(err)
	}
	base := req.PolicyBase
	if base == "" {
		base = req.CWD
	}
	resolved := egg.RebaseLaunchConfig(cfg, base)
	rc := egg.RunConfig{
		Agent: req.Agent, CWD: req.CWD, UserHome: req.Home, SkipHostAgentEnv: true,
		OuterBoundary: !egg.RequiresSandbox(cfg, req.Agent),
		FS:            resolved.FS, Network: resolved.Network.Domains, LocalPorts: resolved.Network.LocalPorts,
		NetworkMode: resolved.Network.Mode, AgentDomains: resolved.Network.AgentDomains,
		DangerouslySkipPermissions: req.Unattended || cfg.DangerouslySkipPermissions,
		ProviderSessionID:          "launch-plan", OmitBrowserBridge: true,
		CPULimit: cfg.Resources.CPUDuration(), MemLimit: cfg.Resources.MemBytes(),
		MaxFDs: cfg.Resources.MaxFDs, PidLimit: cfg.Resources.MaxPids, Trace: cfg.Trace,
	}
	if req.Route == "terminal_start" {
		rc.Command = []string{req.Executable}
	}
	saved := launchplan.ProviderEvidence{}
	bound := false
	version := ""
	if req.EvidencePath != "" {
		data, err := os.ReadFile(req.EvidencePath)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &saved); err != nil {
			t.Fatal(err)
		}
		// This fixture's saved evidence was independently bound when it was built.
		data, err = os.ReadFile(req.Executable)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(data)
		bound = saved.BinarySHA256 == hex.EncodeToString(digest[:])
		if bound {
			version = saved.VersionOutput
		}
	}
	eggDir := filepath.Join(filepath.Dir(req.PolicyPath), "launch-plan")
	if err := os.MkdirAll(eggDir, 0700); err != nil {
		t.Fatal(err)
	}
	capsData, err := os.ReadFile(req.CapabilitiesPath)
	if err != nil {
		t.Fatal(err)
	}
	capsDigest := sha256.Sum256(capsData)
	capabilitySource := "declared capability facts sha256:" + hex.EncodeToString(capsDigest[:])
	return egg.PrepareLaunch(rc, egg.LaunchPreparationOptions{
		Binary: req.Executable, Home: req.Home, HostHome: mergeHome,
		EggDir:      eggDir,
		ProviderURL: req.ProviderURL, CodexCapability: egg.SavedCodexCapability(version),
		Platform: caps.OS, EnvironmentConfig: cfg,
		Requirements: &egg.LaunchRequirements{
			Route: req.Route, AllowedHomeRoot: req.AllowedHomeRoot, CredentialPaths: req.CredentialPaths,
			ProviderStateDir: req.ProviderStateDir, Capabilities: &caps,
			PolicySource: req.PolicyPath, PolicyBase: base, Rules: rules, CapabilitySource: capabilitySource,
			Evidence: &saved, EvidenceBound: bound, VersionKnown: version != "",
		},
	}, egg.NativeLaunchState())
}

func assertRound4Checks(t *testing.T, report launchplan.Report, real egg.PreparedLaunch) {
	t.Helper()
	if report.Network != real.Transport {
		t.Fatalf("transport: preflight=%s, native=%s", report.Network, real.Transport)
	}
	// Compare shared decisions and provenance, including duplicate profile paths.
	var actual, expected []egg.LaunchCheck
	for _, check := range real.Checks {
		if report.Agent == "claude" && check.Code == "provider.option" && strings.HasSuffix(check.Reason, ": --settings") {
			gate := find(t, report, "provider.lifecycle_argv")
			if check.Status != launchplan.Unrun || gate.Status != launchplan.Unrun {
				t.Fatal("deferred Claude settings flags promoted to verified")
			}
			continue // generated settings argv is explicitly deferred by the dry effect
		}
		expected = append(expected, check)
		for _, gate := range report.Gates {
			if gate.Code == check.Code && gate.Status == check.Status && gate.Reason == check.Reason && gate.Source == check.Source && gate.Correction == check.Correction {
				actual = append(actual, check)
				break
			}
		}
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("missing shared decision: native=%+v; preflight=%+v", real.Checks, report.Gates)
	}
	for _, gate := range report.Gates {
		if strings.HasPrefix(gate.Code, "provider.preparation") || gate.Code == "cwd" || gate.Code == "executable" || gate.Code == "policy.base" || gate.Code == "authentication" || gate.Code == "provider.evidence" || gate.Code == "provider.lifecycle_argv" || gate.Code == "credential.path" || gate.Code == "provider.version" || gate.Code == "environment.selector" {
			continue
		}
		found := false
		for _, check := range real.Checks {
			if check.Code == gate.Code && check.Status == gate.Status && check.Reason == gate.Reason {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("consumer added a policy decision: %+v", gate)
		}
	}
}

func TestLaunchPreparationRound4UnrestrictedBoundary(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		for _, agentName := range []string{"", "codex"} {
			t.Run(platform+"/"+agentName, func(t *testing.T) {
				req, caps, dir := fixture(t)
				req.Agent, req.Route = agentName, "agent_start"
				if agentName == "" {
					req.Route, req.Executable, req.EvidencePath, req.Unattended = "terminal_start", "/bin/sh", "", false
				}
				caps.OS, caps.NativeSandbox = platform, launchplan.Unsupported
				putJSON(t, req.CapabilitiesPath, caps)
				put(t, req.PolicyPath, "base: none\nfs: []\nnetwork: {domains: ['*'], agent_domains: none}\nenv: '*'\n")
				report := inspectUnchanged(t, req, dir)
				cfg := runtimeConfig(t, req)
				if egg.RequiresSandbox(cfg, agentName) {
					t.Fatal("real caller requires a sandbox for unrestricted policy")
				}
				real, err := round4Preparation(t, req, caps)
				if err != nil || real.Sandboxed {
					t.Fatalf("real unrestricted preparation: sandboxed=%v, error=%v", real.Sandboxed, err)
				}
				if report.Readiness == "blocked" || report.Network != "full" {
					t.Fatalf("unrestricted launch blocked or misclassified: %+v", report)
				}
				for _, gate := range report.Gates {
					if gate.Code == "sandbox.native" {
						t.Fatalf("unrestricted route requires a native sandbox: %+v", gate)
					}
				}
				assertRound4Checks(t, report, real)
				if !reflect.DeepEqual(report.ProviderArgv, append([]string{req.Executable}, real.Args...)) {
					t.Fatalf("unrestricted argv differs: %v / %v", report.ProviderArgv, real.Args)
				}
			})
		}
	}
}

func TestLaunchPreparationRound4LoopbackDefaultDestination(t *testing.T) {
	for _, agentName := range []string{"claude", "gemini"} {
		for _, platform := range []string{"darwin", "linux"} {
			t.Run(agentName+"/"+platform, func(t *testing.T) {
				req, caps, dir := fixture(t)
				req.Agent, req.Route, req.EvidencePath = agentName, "agent_start", ""
				caps.OS = platform
				putJSON(t, req.CapabilitiesPath, caps)
				put(t, req.PolicyPath, "network: {domains: [localhost], agent_domains: none}\n")
				report := inspectUnchanged(t, req, dir)
				host := "api.anthropic.com"
				if agentName == "gemini" {
					host = "generativelanguage.googleapis.com"
				}
				if sandbox.DomainAllowed(report.Domains, host) || sandbox.ReviewProxyStatus(report.Domains, host+":443") != 403 {
					t.Fatal("independent CONNECT oracle permits the remote default")
				}
				real, err := round4Preparation(t, req, caps)
				if err != nil {
					t.Fatal(err)
				}
				profile := sandbox.ReviewProfile(real.Sandbox)
				if real.Sandbox.NetworkNeed != sandbox.NetworkLocal || !strings.Contains(profile, "(deny network*)") || strings.Contains(profile, `(remote tcp "*:443`) {
					t.Fatal("emitted policy permits remote traffic")
				}
				// Profiles intentionally don't establish an installed provider's active
				// default. The explicit unknown is the review's accepted alternative.
				found := false
				for _, deferred := range real.Deferred {
					if strings.Contains(deferred, "provider destination absent from the runtime profile") {
						gate := find(t, report, "provider.preparation.deferred")
						for _, g := range report.Gates {
							if g.Code == gate.Code && g.Status == launchplan.Unrun && strings.Contains(g.Reason, deferred) {
								found = true
							}
						}
					}
				}
				if !found || report.Readiness == "ready-local" {
					t.Fatalf("restricted destination has no explicit unknown: %+v / %+v", real, report)
				}
				assertRound4Checks(t, report, real)
			})
		}
	}
}

func TestLaunchPreparationRound4RemainingPolicyDecisions(t *testing.T) {
	cases := []struct {
		name      string
		configure func(*testing.T, *launchplan.Request, *launchplan.Capabilities, string)
	}{
		{"denied-executable", func(t *testing.T, r *launchplan.Request, c *launchplan.Capabilities, d string) {
			put(t, r.PolicyPath, "fs: [rw:./, deny:"+r.Executable+"]\n")
		}},
		{"denied-credential", func(t *testing.T, r *launchplan.Request, c *launchplan.Capabilities, d string) {
			p := filepath.Join(r.Home, "auth.json")
			put(t, p, "SECRET_SENTINEL")
			r.CredentialPaths = []string{p}
			put(t, r.PolicyPath, "fs: [rw:./, deny:"+p+"]\n")
		}},
		{"known-hosts-exception", func(t *testing.T, r *launchplan.Request, c *launchplan.Capabilities, d string) {
			p := filepath.Join(r.Home, ".ssh")
			if err := os.Mkdir(p, 0700); err != nil {
				t.Fatal(err)
			}
			p = filepath.Join(p, "known_hosts")
			put(t, p, "SECRET_SENTINEL")
			r.CredentialPaths = []string{p}
			put(t, r.PolicyPath, "fs: [rw:./, deny:~/.ssh]\n")
		}},
		{"known-hosts-explicit-denial", func(t *testing.T, r *launchplan.Request, c *launchplan.Capabilities, d string) {
			p := filepath.Join(r.Home, ".ssh")
			if err := os.Mkdir(p, 0700); err != nil {
				t.Fatal(err)
			}
			p = filepath.Join(p, "known_hosts")
			put(t, p, "SECRET_SENTINEL")
			r.CredentialPaths = []string{p}
			put(t, r.PolicyPath, "fs: [rw:./, deny:~/.ssh, deny:~/.ssh/known_hosts]\n")
		}},
		{"workspace-denial", func(t *testing.T, r *launchplan.Request, c *launchplan.Capabilities, d string) {
			put(t, r.PolicyPath, "fs: [rw:./, deny:"+r.CWD+"]\n")
		}},
		{"state-override-denial", func(t *testing.T, r *launchplan.Request, c *launchplan.Capabilities, d string) {
			r.ProviderStateDir = filepath.Join(r.Home, "alternate")
			put(t, r.PolicyPath, "fs: [rw:./, deny:"+r.ProviderStateDir+"]\n")
		}},
		{"state-override-scope", func(t *testing.T, r *launchplan.Request, c *launchplan.Capabilities, d string) {
			r.AllowedHomeRoot = r.Home
			r.ProviderStateDir = filepath.Join(d, "alternate")
		}},
		{"home-scope", func(t *testing.T, r *launchplan.Request, c *launchplan.Capabilities, d string) {
			r.AllowedHomeRoot = r.CWD
		}},
		{"credential-scope", func(t *testing.T, r *launchplan.Request, c *launchplan.Capabilities, d string) {
			p := filepath.Join(d, "auth.json")
			put(t, p, "SECRET_SENTINEL")
			r.CredentialPaths = []string{p}
		}},
		{"profile-state-scope", func(t *testing.T, r *launchplan.Request, c *launchplan.Capabilities, d string) {
			p := filepath.Join(d, "external-state")
			if err := os.Mkdir(p, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(p, filepath.Join(r.Home, ".codex")); err != nil {
				t.Fatal(err)
			}
			r.AllowedHomeRoot = r.Home
			r.EvidencePath = ""
		}},
		{"elan-filtered", func(t *testing.T, r *launchplan.Request, c *launchplan.Capabilities, d string) {
			c.ElanHome = filepath.Join(r.Home, ".elan")
			c.ElanHomeWritable = launchplan.Unsupported
			put(t, r.PolicyPath, "env: []\n")
		}},
		{"elan-selected", func(t *testing.T, r *launchplan.Request, c *launchplan.Capabilities, d string) {
			c.ElanHome = filepath.Join(r.Home, ".elan")
			c.ElanHomeWritable = launchplan.Unsupported
			put(t, r.PolicyPath, "env: [ELAN_HOME]\nfs: [rw:./, rw:~/.elan]\n")
		}},
		{"parent-network-none", func(t *testing.T, r *launchplan.Request, c *launchplan.Capabilities, d string) { c.Network = "none" }},
		{"raw-sockets", func(t *testing.T, r *launchplan.Request, c *launchplan.Capabilities, d string) {
			c.RawSocketsRequired = true
		}},
		{"linux-scoped-parent", func(t *testing.T, r *launchplan.Request, c *launchplan.Capabilities, d string) {
			c.OS = "linux"
			c.ParentSandbox = "scoped"
		}},
		{"nested-codex", func(t *testing.T, r *launchplan.Request, c *launchplan.Capabilities, d string) {
			r.Unattended = false
			c.ParentSandbox = "sandboxed"
		}},
		{"saved-help", func(t *testing.T, r *launchplan.Request, c *launchplan.Capabilities, d string) {
			putJSON(t, r.EvidencePath, launchplan.ProviderEvidence{BinarySHA256: fixtureEvidence(t, *r).BinarySHA256, VersionOutput: "codex-cli 0.159.3", HelpOutput: " --no-daemon\n", CompleteHelp: true})
		}},
	}
	expected := map[string]struct{ code, status string }{
		"denied-executable":           {"executable.policy", launchplan.Fail},
		"denied-credential":           {"credential.policy", launchplan.Fail},
		"known-hosts-explicit-denial": {"credential.policy", launchplan.Fail},
		"workspace-denial":            {"workspace.write", launchplan.Fail},
		"state-override-denial":       {"provider_state.write", launchplan.Fail},
		"state-override-scope":        {"provider_home.scope", launchplan.Fail},
		"home-scope":                  {"home.scope", launchplan.Fail},
		"credential-scope":            {"credential.scope", launchplan.Fail},
		"profile-state-scope":         {"provider_home.scope", launchplan.Fail},
		"elan-filtered":               {"environment.elan_home.filtered", launchplan.Pass},
		"elan-selected":               {"environment.elan_home", launchplan.Fail},
		"parent-network-none":         {"network.parent", launchplan.Fail},
		"raw-sockets":                 {"network.raw_sockets", launchplan.Fail},
		"linux-scoped-parent":         {"parent.scoped", launchplan.Fail},
		"nested-codex":                {"sandbox.nested", launchplan.Fail},
		"saved-help":                  {"provider.option", launchplan.Fail},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, caps, dir := fixture(t)
			tc.configure(t, &req, &caps, dir)
			putJSON(t, req.CapabilitiesPath, caps)
			report := inspectUnchanged(t, req, dir)
			real, err := round4Preparation(t, req, caps)
			if err != nil {
				t.Fatalf("real fixture preparation: %v", err)
			}
			assertRound4Checks(t, report, real)
			if want, ok := expected[tc.name]; ok {
				got := find(t, report, want.code)
				for _, g := range report.Gates {
					if g.Code == want.code && g.Status == want.status {
						got = g
						break
					}
				}
				if got.Status != want.status {
					t.Fatalf("%s=%s, want %s", want.code, got.Status, want.status)
				}
			}
			if tc.name == "state-override-denial" && real.Environment["CODEX_HOME"] != req.ProviderStateDir {
				t.Fatal("state override was not passed to the production environment")
			}
			if tc.name == "known-hosts-exception" {
				profile := sandbox.ReviewProfile(real.Sandbox)
				if !strings.Contains(profile, fmt.Sprintf("(allow file-read* (literal %q))", filepath.Join(req.Home, ".ssh", "known_hosts"))) {
					t.Fatal("emitted read exception missing")
				}
				for _, g := range report.Gates {
					if g.Code == "credential.policy" && g.Status == launchplan.Fail {
						t.Fatal("preflight ignored emitted exception")
					}
				}
			}
			serialized, _ := json.Marshal(report)
			if strings.Contains(string(serialized), "SECRET_SENTINEL") {
				t.Fatal("credential contents escaped")
			}
		})
	}
}

func fixtureEvidence(t *testing.T, req launchplan.Request) launchplan.ProviderEvidence {
	t.Helper()
	var saved launchplan.ProviderEvidence
	data, err := os.ReadFile(req.EvidencePath)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	return saved
}

func TestLaunchPreparationRound4RootReadPolicy(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		t.Run(platform, func(t *testing.T) {
			req, caps, dir := fixture(t)
			req.Route, req.Agent, req.EvidencePath = "terminal_start", "", ""
			caps.OS = platform
			putJSON(t, req.CapabilitiesPath, caps)
			put(t, req.PolicyPath, "base: none\nfs: [rw:./, deny:/]\nnetwork: {agent_domains: none}\n")
			report := inspectUnchanged(t, req, dir)
			real, err := round4Preparation(t, req, caps)
			if err != nil {
				t.Fatal(err)
			}
			assertRound4Checks(t, report, real)
			got := find(t, report, "executable.policy")
			want := launchplan.Fail
			if platform == "linux" {
				want = launchplan.Unrun
			}
			if got.Status != want {
				t.Fatalf("root read policy=%s, want %s", got.Status, want)
			}
		})
	}
}
