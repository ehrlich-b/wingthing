package launchplan

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeFixture(t *testing.T, dir, name string, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func setup(t *testing.T) (Request, Capabilities) {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "provider")
	if err := os.WriteFile(binary, []byte("sentinel executable that must never run"), 0700); err != nil {
		t.Fatal(err)
	}
	req := Request{Route: "agent_start", Agent: "codex", CWD: dir, Home: filepath.Join(dir, "home"), AllowedHomeRoot: dir, Executable: binary, Unattended: true}
	if err := os.Mkdir(req.Home, 0700); err != nil {
		t.Fatal(err)
	}
	caps := Capabilities{OS: "linux", Network: "proxy-only", WorkspaceWritable: Supported, ProviderHomeWritable: Supported, NativeSandbox: Supported, ParentSandbox: "none"}
	req.CapabilitiesPath = writeFixture(t, dir, "facts.json", caps)
	return req, caps
}
func evidence(t *testing.T, req *Request, version, help string, complete bool) {
	t.Helper()
	data, err := os.ReadFile(req.Executable)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	req.EvidencePath = writeFixture(t, req.CWD, "evidence.json", ProviderEvidence{BinarySHA256: hex.EncodeToString(hash[:]), VersionOutput: version, HelpOutput: help, CompleteHelp: complete})
}
func hasGate(t *testing.T, report Report, code, status, part string) Gate {
	t.Helper()
	for _, gate := range report.Gates {
		if gate.Code == code && gate.Status == status && strings.Contains(gate.Reason, part) {
			return gate
		}
	}
	t.Fatalf("missing %s %s %q in %+v", code, status, part, report.Gates)
	return Gate{}
}
func policy(t *testing.T, req *Request, body string) {
	t.Helper()
	req.PolicyPath = filepath.Join(req.CWD, "egg.yaml")
	if err := os.WriteFile(req.PolicyPath, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestLaunchPlanLoggedFailures(t *testing.T) {
	for _, tc := range []struct {
		name, code, part string
		change           func(*Request, *Capabilities)
	}{
		{"outer-network-none", "network.parent", "network-none", func(r *Request, c *Capabilities) { c.Network = "none" }},
		{"outer-read-only-home", "provider_home.write", "read-only", func(r *Request, c *Capabilities) { c.ProviderHomeWritable = Unsupported }},
		{"outer-read-only-workspace", "workspace.write", "read-only", func(r *Request, c *Capabilities) { c.WorkspaceWritable = Unsupported }},
		{"bus-dependent-mode", "user_bus", "no user bus", func(r *Request, c *Capabilities) { c.RequiresUserBus = true; c.UserBus = Unsupported }},
		{"nested-codex", "sandbox.nested", "inner sandbox", func(r *Request, c *Capabilities) { r.Unattended = false; c.ParentSandbox = "sandboxed" }},
		{"raw-sockets", "network.raw_sockets", "raw sockets", func(r *Request, c *Capabilities) { c.RawSocketsRequired = true }},
		{"scoped-linux", "parent.scoped", "unsupported", func(r *Request, c *Capabilities) { c.ParentSandbox = "scoped" }},
		{"denied-native", "sandbox.native", "cannot create", func(r *Request, c *Capabilities) { c.NativeSandbox = Unsupported }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, caps := setup(t)
			tc.change(&req, &caps)
			req.CapabilitiesPath = writeFixture(t, req.CWD, "facts.json", caps)
			report, err := Inspect(req)
			if err != nil {
				t.Fatal(err)
			}
			hasGate(t, report, tc.code, Fail, tc.part)
			if report.Readiness != "blocked" {
				t.Fatal(report.Readiness)
			}
		})
	}
}
func TestLaunchPlanUnknownNeverReadyAndBusOptional(t *testing.T) {
	req, caps := setup(t)
	caps.UserBus = Unsupported
	caps.NativeSandbox = Unknown
	req.CapabilitiesPath = writeFixture(t, req.CWD, "facts.json", caps)
	report, err := Inspect(req)
	if err != nil {
		t.Fatal(err)
	}
	hasGate(t, report, "sandbox.native", Unrun, "not been verified")
	for _, gate := range report.Gates {
		if gate.Code == "user_bus" {
			t.Fatal("optional bus became a requirement")
		}
	}
	if report.Readiness != "unverified" {
		t.Fatal(report.Readiness)
	}
	req.CapabilitiesPath = ""
	report, err = Inspect(req)
	if err != nil {
		t.Fatal(err)
	}
	hasGate(t, report, "network.parent", Unrun, "unknown")
	hasGate(t, report, "workspace.write", Unrun, "unknown")
}
func TestLaunchPlanNetworkPolicyAndTransportAcrossHosts(t *testing.T) {
	for _, tc := range []struct{ os, body, network, code string }{
		{"linux", "base: none\nfs: [rw:./]\nnetwork: {agent_domains: none}\n", "none", "network.provider"},
		{"linux", "network: '*'\n", "proxy-only", ""},
		{"darwin", "network: '*'\n", "full", ""},
		{"darwin", "network: [api.example]\n", "proxy-only", ""},
	} {
		t.Run(tc.os+tc.network, func(t *testing.T) {
			req, caps := setup(t)
			caps.OS = tc.os
			req.CapabilitiesPath = writeFixture(t, req.CWD, "facts.json", caps)
			policy(t, &req, tc.body)
			report, err := Inspect(req)
			if err != nil {
				t.Fatal(err)
			}
			if report.Network != tc.network {
				t.Fatalf("%s want %s", report.Network, tc.network)
			}
			if tc.code != "" {
				hasGate(t, report, tc.code, Fail, "no route")
			}
		})
	}
}
func TestLaunchPlanCanonicalCWDHomeOverrideMissingBinaryAndElan(t *testing.T) {
	req, caps := setup(t)
	// Model macOS's /var -> /private/var even when TMPDIR has no symlinks.
	parentAlias := filepath.Join(t.TempDir(), "parent-alias")
	if err := os.Symlink(req.CWD, parentAlias); err != nil {
		t.Fatal(err)
	}
	req.CWD = parentAlias
	req.Home = filepath.Join(parentAlias, "home")
	req.Executable = filepath.Join(parentAlias, "provider")
	dir := req.CWD
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	req.PathBase = dir
	req.CWD = "./alias/../alias"
	caps.ElanHome = filepath.Join(req.Home, ".elan")
	caps.ElanHomeWritable = Unsupported
	req.CapabilitiesPath = writeFixture(t, dir, "facts.json", caps)
	// pathWriteGate checks declarations before outer facts; explicitly permit ELAN.
	req.PolicyPath = filepath.Join(dir, "policy.yaml")
	if err := os.WriteFile(req.PolicyPath, []byte("fs: [rw:~/.elan]\nenv: [ELAN_HOME]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	report, err := Inspect(req)
	if err != nil {
		t.Fatal(err)
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if report.CWD.Resolved != realDir || report.CWD.Path != alias || report.CWD.Path == report.CWD.Resolved {
		t.Fatalf("cwd: %+v", report.CWD)
	}
	hasGate(t, report, "environment.elan_home", Fail, "read-only")
	req.Executable = filepath.Join(dir, "missing")
	report, err = Inspect(req)
	if err != nil {
		t.Fatal(err)
	}
	hasGate(t, report, "executable", Fail, "missing")
	outside := t.TempDir()
	req.ProviderStateDir = filepath.Join(outside, "state")
	report, err = Inspect(req)
	if err != nil {
		t.Fatal(err)
	}
	hasGate(t, report, "provider_home.scope", Fail, "outside")
	req.Home = outside
	report, err = Inspect(req)
	if err != nil {
		t.Fatal(err)
	}
	hasGate(t, report, "home.scope", Fail, "outside")
}
func TestLaunchPlanInheritedDenialSource(t *testing.T) {
	req, _ := setup(t)
	parent := filepath.Join(req.CWD, "parent.yaml")
	if err := os.WriteFile(parent, []byte("base: none\nfs: [deny:./]\nnetwork: {agent_domains: none}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	policy(t, &req, "base: ./parent.yaml\nfs: [deny:./]\n")
	report, err := Inspect(req)
	if err != nil {
		t.Fatal(err)
	}
	gate := hasGate(t, report, "workspace.write", Fail, "deny")
	if !strings.Contains(gate.Source, parent) || !strings.Contains(gate.Source, req.PolicyPath) {
		t.Fatalf("denial provenance: %s", gate.Source)
	}
}

func TestLaunchPlanSymlinkedParentPreservesDisplayPathsAndDenialSources(t *testing.T) {
	req, _ := setup(t)
	dir := req.CWD
	workspace := filepath.Join(dir, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "parent-alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	req.CWD = filepath.Join(alias, "workspace")
	req.Home = filepath.Join(alias, "home")
	req.Executable = filepath.Join(alias, "provider")
	parent := filepath.Join(alias, "parent.yaml")
	if err := os.WriteFile(parent, []byte("base: none\nfs: [deny:./]\nnetwork: {agent_domains: none}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	req.PolicyPath = filepath.Join(alias, "child.yaml")
	if err := os.WriteFile(req.PolicyPath, []byte("base: ./parent.yaml\nfs: [deny:./]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	report, err := Inspect(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []PathEvidence{report.CWD, report.Home, report.Executable} {
		want, err := filepath.EvalSymlinks(entry.Path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(entry.Path, alias+string(filepath.Separator)) || entry.Resolved != want || entry.Path == entry.Resolved || !entry.Available {
			t.Fatalf("display path or canonical identity lost: %+v; want resolved %s", entry, want)
		}
	}
	hasGate(t, report, "home.scope", Pass, "within")
	hasGate(t, report, "executable", Pass, "present")
	gate := hasGate(t, report, "workspace.write", Fail, "deny")
	if gate.Source != parent+", "+req.PolicyPath || report.PolicySource != req.PolicyPath {
		t.Fatalf("display policy paths lost: policy=%s denial=%s", report.PolicySource, gate.Source)
	}
}

func TestLaunchPlanRoutesAndTwoProviderVersions(t *testing.T) {
	for _, version := range []string{"codex-cli 0.147.0", "codex-cli 0.159.3"} {
		t.Run(version, func(t *testing.T) {
			req, _ := setup(t)
			req.Route = "agent_run"
			evidence(t, &req, version, "  -c, --config <KEY=VALUE>\n  --dangerously-bypass-approvals-and-sandbox\n  --no-daemon\n", true)
			report, err := Inspect(req)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(strings.Join(report.ProviderArgv, " "), "--full-auto") {
				t.Fatal("obsolete flag")
			}
			if strings.Contains(version, "0.147") {
				hasGate(t, report, "route.agent_run", Fail, "does not support")
			} else {
				hasGate(t, report, "route.agent_run", Pass, "declares native")
			}
		})
	}
	req, _ := setup(t)
	req.Route = "terminal_start"
	req.Agent = ""
	report, err := Inspect(req)
	if err != nil {
		t.Fatal(err)
	}
	if report.Network != "none" || len(report.ProviderArgv) != 1 {
		t.Fatalf("terminal got agent policy: %+v", report)
	}
	for _, agent := range []string{"gemini", "ollama", "opencode", "cursor", "hermes"} {
		req.Agent = agent
		req.Route = "agent_run"
		report, err := Inspect(req)
		if err != nil {
			t.Fatal(err)
		}
		hasGate(t, report, "route.agent_run", Fail, "no native")
	}
}
func TestLaunchPlanHelpAbsentUnsupportedAndStaleBinding(t *testing.T) {
	req, _ := setup(t)
	evidence(t, &req, "codex-cli 0.159.3", "Usage: codex\n", true)
	report, err := Inspect(req)
	if err != nil {
		t.Fatal(err)
	}
	hasGate(t, report, "provider.option", Fail, "absent")
	if err := os.WriteFile(req.Executable, []byte("replacement executable"), 0700); err != nil {
		t.Fatal(err)
	}
	report, err = Inspect(req)
	if err != nil {
		t.Fatal(err)
	}
	hasGate(t, report, "provider.evidence", Unrun, "not bound")
}
func TestLaunchPlanExportNeverContainsPromptOrToken(t *testing.T) {
	req, _ := setup(t)
	const prompt = "SENTINEL_PROMPT_123"
	const token = "SENTINEL_TOKEN_456"
	credential := filepath.Join(req.Home, "auth.json")
	if err := os.WriteFile(credential, []byte(token), 0000); err != nil {
		t.Fatal(err)
	}
	req.CredentialPaths = []string{credential}
	evidence(t, &req, "codex-cli 0.159.3", prompt+"\n"+token+"\n --dangerously-bypass-approvals-and-sandbox\n -c, --config\n --no-daemon\n", false)
	policy(t, &req, "agent_settings: {codex: '"+token+"'}\nenv: ['OPENAI_API_KEY="+token+"']\n")
	report, err := Inspect(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, write := range []func(*bytes.Buffer) error{func(b *bytes.Buffer) error { return WriteJSON(b, report) }, func(b *bytes.Buffer) error { return Render(b, report) }} {
		var output bytes.Buffer
		if err := write(&output); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output.String(), prompt) || strings.Contains(output.String(), token) {
			t.Fatal("diagnostic retained sensitive input")
		}
	}
}
func TestLaunchPlanStableAndNoAmbientFallback(t *testing.T) {
	req, _ := setup(t)
	t.Setenv("HOME", "/foreign-home")
	t.Setenv("CODEX_HOME", "/foreign-provider")
	t.Setenv("OPENAI_API_KEY", "DO_NOT_EXPORT")
	t.Setenv("WINGTHING_DIR", "/foreign-production-wing")
	first, err := Inspect(req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Inspect(req)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("diagnosis is unstable")
	}
	for _, change := range []func(*Request){func(r *Request) { r.Home = "" }, func(r *Request) { r.Executable = "codex" }, func(r *Request) { r.CWD = "relative" }, func(r *Request) { r.Route = "unsupported" }} {
		copy := req
		change(&copy)
		if _, err := Inspect(copy); err == nil {
			t.Fatal("missing explicit input accepted")
		}
	}
}

func TestLaunchPlanExportsLegacyDriverWithoutUpgradingSavedFacts(t *testing.T) {
	req, _ := setup(t)
	report, err := Inspect(req)
	if err != nil {
		t.Fatal(err)
	}
	if report.Driver.ID != "legacy" || report.Driver.Backend != "linux-namespaces-seccomp" || report.Driver.Protocol != "egg/v1" {
		t.Fatalf("driver identity missing: %+v", report.Driver)
	}
	if len(report.Driver.Coverage) != 10 {
		t.Fatalf("incomplete process coverage: %+v", report.Driver.Coverage)
	}
	for _, c := range report.Driver.Capabilities {
		if c.Status == egg.CapabilitySupported {
			t.Fatalf("saved facts certified driver capability: %+v", c)
		}
	}
	var machine, human bytes.Buffer
	if err := WriteJSON(&machine, report); err != nil {
		t.Fatal(err)
	}
	if err := Render(&human, report); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(machine.String(), `"coverage"`) || !strings.Contains(machine.String(), `"controller-only-auth"`) {
		t.Fatal("driver report absent from export")
	}
	if strings.Contains(human.String(), "controller-only-auth") {
		t.Fatal("machine capability inventory expanded the compact human report")
	}
}
func TestLaunchPlanBoundedStrictSavedInput(t *testing.T) {
	req, _ := setup(t)
	for _, body := range []string{`{"os":"linux","prompt":"SENTINEL"}`, `{"os":"linux"} {}`, `{"os":"linux","network":"typo"}`, strings.Repeat("x", (64<<10)+1)} {
		path := filepath.Join(req.CWD, "bad.json")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		req.CapabilitiesPath = path
		if _, err := Inspect(req); err == nil || strings.Contains(err.Error(), "SENTINEL") {
			t.Fatalf("unsafe input handling: %v", err)
		}
	}
}

func TestLaunchPlanProviderOverrideAndCredentialDenial(t *testing.T) {
	req, _ := setup(t)
	req.ProviderURL = "https://api.disposable.example/v1?token=SENTINEL_URL_TOKEN"
	credential := filepath.Join(req.Home, "denied", "auth.json")
	if err := os.MkdirAll(filepath.Dir(credential), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credential, []byte("SENTINEL_FILE_TOKEN"), 0600); err != nil {
		t.Fatal(err)
	}
	req.CredentialPaths = []string{credential}
	policy(t, &req, "fs: [deny:~/denied]\nnetwork: {agent_domains: none}\n")
	report, err := Inspect(req)
	if err != nil {
		t.Fatal(err)
	}
	hasGate(t, report, "credential.policy", Fail, "deny")
	if !reflect.DeepEqual(report.Domains, []string{"api.disposable.example"}) {
		t.Fatalf("provider routing: %v", report.Domains)
	}
	derived := false
	for _, rule := range report.PolicyRules {
		if rule.Rule == "api.disposable.example" && strings.Contains(rule.Source, "derived") {
			derived = true
		}
	}
	if !derived {
		t.Fatal("provider-derived provenance missing")
	}
	var wire bytes.Buffer
	if err := WriteJSON(&wire, report); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(wire.String(), "SENTINEL") {
		t.Fatal("URL query or credential content exported")
	}
}
func TestLaunchPlanPolicyProvenanceMatrix(t *testing.T) {
	for _, tc := range []struct {
		name, child     string
		denied, missing bool
		sourceCount     int
	}{
		{"inherited", "base: ./parent.yaml\n", true, false, 1},
		{"duplicate-child", "base: ./parent.yaml\nfs: [deny:./blocked]\n", true, false, 2},
		{"section-reuse", "base: {name: ./parent.yaml, fs: ./parent.yaml}\n", false, true, 0},
		{"masked", "base: {name: ./parent.yaml, fs: none}\nfs: [rw:./]\n", false, false, 0},
		{"override", "base: ./parent.yaml\nfs: [rw:./blocked]\n", false, false, 0},
		{"missing", "base: ./missing.yaml\n", false, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := setup(t)
			base := req.CWD
			work := filepath.Join(base, "blocked")
			if err := os.Mkdir(work, 0700); err != nil {
				t.Fatal(err)
			}
			parent := filepath.Join(base, "parent.yaml")
			if err := os.WriteFile(parent, []byte("base: none\nfs: [rw:./, deny:./blocked]\nnetwork: {agent_domains: none}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			policy(t, &req, tc.child)
			req.CWD = work
			// FS declarations are rooted at the launch CWD; parent denial must name
			// the required path rather than its parent policy file directory.
			if err := os.WriteFile(parent, []byte("base: none\nfs: [rw:./, deny:"+work+"]\nnetwork: {agent_domains: none}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if tc.name == "duplicate-child" {
				if err := os.WriteFile(req.PolicyPath, []byte("base: ./parent.yaml\nfs: [deny:"+work+"]\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.name == "override" {
				if err := os.WriteFile(req.PolicyPath, []byte("base: ./parent.yaml\nfs: [rw:"+work+"]\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			report, err := Inspect(req)
			if tc.missing {
				if err == nil {
					t.Fatal("missing base used defaults")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			sources := map[string]bool{}
			for _, rule := range report.PolicyRules {
				if rule.Rule == "deny:"+work {
					sources[rule.Source] = true
				}
			}
			if len(sources) != tc.sourceCount {
				t.Fatalf("sources=%v expected %d", sources, tc.sourceCount)
			}
			if tc.denied {
				gate := hasGate(t, report, "workspace.write", Fail, "deny")
				if !strings.Contains(gate.Source, parent) {
					t.Fatal("parent source missing")
				}
			} else {
				for _, gate := range report.Gates {
					if gate.Code == "workspace.write" && gate.Status == Fail {
						t.Fatal("masked/overridden denial still applies")
					}
				}
			}
		})
	}
}
func TestLaunchPlanDenyWriteDirectoryPlatformDifference(t *testing.T) {
	for _, host := range []string{"linux", "darwin"} {
		t.Run(host, func(t *testing.T) {
			req, caps := setup(t)
			caps.OS = host
			req.CapabilitiesPath = writeFixture(t, req.CWD, "facts.json", caps)
			policy(t, &req, "fs: [deny-write:~/.codex]\n")
			req.ProviderStateDir = filepath.Join(req.Home, ".codex", "child")
			report, err := Inspect(req)
			if err != nil {
				t.Fatal(err)
			}
			if host == "linux" {
				hasGate(t, report, "provider_state.write", Fail, "deny-write")
			} else {
				for _, gate := range report.Gates {
					if gate.Code == "provider_state.write" && gate.Status == Fail {
						t.Fatal("Mac literal deny-write was broadened")
					}
				}
			}
		})
	}
}
func TestLaunchPlanLinuxRootDenyRequiresNativeMountEvidence(t *testing.T) {
	req, _ := setup(t)
	policy(t, &req, "base: none\nfs: [deny:/, rw:./]\nnetwork: {agent_domains: none}\n")
	report, err := Inspect(req)
	if err != nil {
		t.Fatal(err)
	}
	hasGate(t, report, "workspace.write", Unrun, "mount exceptions")
}

func TestLaunchPlanHelpProseAndMalformedVersionRemainUnverified(t *testing.T) {
	req, _ := setup(t)
	req.Route = "agent_run"
	evidence(t, &req, "SENTINEL_VERSION_TEXT", "Unsupported option: --dangerously-bypass-approvals-and-sandbox\n -c, --config <KEY=VALUE>\n", false)
	report, err := Inspect(req)
	if err != nil {
		t.Fatal(err)
	}
	hasGate(t, report, "provider.version", Unrun, "unrecognized")
	hasGate(t, report, "route.agent_run", Unrun, "requires bound")
	hasGate(t, report, "provider.option", Unrun, "--dangerously-bypass-approvals-and-sandbox")
	hasGate(t, report, "provider.option", Pass, "-c")
	var wire bytes.Buffer
	if err := WriteJSON(&wire, report); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(wire.String(), "SENTINEL") {
		t.Fatal("raw malformed version exported")
	}
}

func TestLaunchPlanPinnedAdapterOracleAndTwoVersionExports(t *testing.T) {
	data, err := os.ReadFile("testdata/codex-command-oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		SourceBase   string `json:"source_base"`
		PlanningRoot string `json:"planning_root"`
		Records      []struct {
			VersionOutput string   `json:"version_output"`
			Argv          []string `json:"argv"`
			NativeRun     bool     `json:"native_run"`
		} `json:"records"`
	}
	if err := json.Unmarshal(data, &oracle); err != nil {
		t.Fatal(err)
	}
	if oracle.SourceBase != "2c3688f1f59cee08713d77476a917d7bc4bf7867" || len(oracle.Records) != 2 {
		t.Fatal("unexpected independent oracle pin/count")
	}
	for _, record := range oracle.Records {
		t.Run(record.VersionOutput, func(t *testing.T) {
			req, _ := setup(t)
			req.Route = "agent_run"
			evidence(t, &req, record.VersionOutput, "SENTINEL_PROMPT_FROM_HELP\nSENTINEL_TOKEN_FROM_HELP\n", false)
			report, err := Inspect(req)
			if err != nil {
				t.Fatal(err)
			}
			actual, native, err := egg.LaunchCommandPreview("codex", oracle.PlanningRoot, filepath.Join(oracle.PlanningRoot, "home"), record.VersionOutput, true)
			if err != nil || native != record.NativeRun {
				t.Fatalf("native builder compatibility mismatch: %v", err)
			}
			for i, arg := range actual {
				actual[i] = strings.ReplaceAll(arg, oracle.PlanningRoot, "<ROOT>")
			}
			if !reflect.DeepEqual(actual, record.Argv) {
				t.Fatalf("planner diverged from pinned adapter argv for %s", record.VersionOutput)
			}
			expectedStatus := Fail
			if record.NativeRun {
				expectedStatus = Pass
			}
			hasGate(t, report, "route.agent_run", expectedStatus, "")
			for _, write := range []func(*bytes.Buffer) error{func(b *bytes.Buffer) error { return WriteJSON(b, report) }, func(b *bytes.Buffer) error { return Render(b, report) }} {
				var output bytes.Buffer
				if err := write(&output); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(output.String(), "SENTINEL") {
					t.Fatal("two-version export retained raw help/prompt/token")
				}
			}
		})
	}
}

func TestLaunchPlanProviderDestinationAgainstProxyPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, network, providerURL string
		blocked                    bool
	}{
		{"unrelated domain", "{domains: [unrelated.example], agent_domains: none}", "", true},
		{"unrelated forward", "{domains: [unrelated.example], local_ports: [8765], agent_domains: none}", "", true},
		{"local only", "{domains: [localhost], local_ports: [8765], agent_domains: none}", "", true},
		{"exact default", "{domains: [API.OPENAI.COM.], agent_domains: none}", "", false},
		{"wildcard default", "{domains: ['*.openai.com'], agent_domains: none}", "", false},
		{"observe", "{domains: [unrelated.example], agent_domains: none, mode: observe}", "", false},
		{"explicit endpoint", "{domains: [unrelated.example], agent_domains: none}", "https://api.disposable.example/v1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := setup(t)
			req.ProviderURL = tc.providerURL
			policy(t, &req, "network: "+tc.network+"\n")
			report, err := Inspect(req)
			if err != nil {
				t.Fatal(err)
			}
			live, err := egg.ResolveEggConfig(req.PolicyPath)
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := egg.ResolvePolicyWithProvider(live, req.Agent, req.Home, req.ProviderURL)
			if err != nil {
				t.Fatal(err)
			}
			host, err := egg.ProviderDestination(req.Agent, req.ProviderURL)
			if err != nil {
				t.Fatal(err)
			}
			if resolved.NetworkNeed == sandbox.NetworkHTTPS {
				denied := resolved.Mode != "observe" && !sandbox.DomainAllowed(resolved.Domains, host)
				if denied != tc.blocked {
					t.Fatalf("runtime proxy decision = %v, want %v", denied, tc.blocked)
				}
			}
			blocked := false
			for _, gate := range report.Gates {
				if gate.Code == "network.provider" && gate.Status == Fail {
					blocked = true
				}
			}
			if blocked != tc.blocked {
				t.Fatalf("preflight blocked = %v, want %v: %+v", blocked, tc.blocked, report.Gates)
			}
		})
	}
}

func TestLaunchPlanDirectoryPreparationMatchesCodexRun(t *testing.T) {
	for _, component := range []string{".codex", ".codex/wingthing-events", ".codex/wingthing-run-notify"} {
		t.Run(component, func(t *testing.T) {
			req, _ := setup(t)
			req.Route = "agent_run"
			evidence(t, &req, "codex-cli 0.159.3", " --no-daemon\n --dangerously-bypass-approvals-and-sandbox\n -c, --config\n", true)
			path := filepath.Join(req.Home, component)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("existing regular file"), 0600); err != nil {
				t.Fatal(err)
			}
			report, err := Inspect(req)
			if err != nil {
				t.Fatal(err)
			}
			hasGate(t, report, "provider.preparation", Fail, "directory")
			var output bytes.Buffer
			if err := Render(&output, report); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), "blocked") {
				t.Fatal(output.String())
			}
			if report.Readiness != "blocked" {
				t.Fatal(report.Readiness)
			}
			_, _, err = egg.CodexRunArgs([]string{"--dangerously-bypass-approvals-and-sandbox"}, req.Home, "launch-plan")
			if err == nil {
				t.Fatal("runtime prepared spool below a regular file")
			}
		})
	}
}

func TestLaunchPlanHumanReachUsesSharedPolicyEvidence(t *testing.T) {
	req, _ := setup(t)
	policy(t, &req, "base: none\nfs: [rw:./, 'deny:/private-data']\nnetwork: {agent_domains: none}\n")
	report, err := Inspect(req)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := Render(&output, report); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{"Egg reach", "native confinement requires acceptance", "Provider HOME:", "deny:/private-data", "source:", "Provider domains:", "Acceptance: externally-pending"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in human policy report", want)
		}
	}
}

func TestLaunchPlanHumanReachDistinguishesTrustedOuterBoundary(t *testing.T) {
	req, _ := setup(t)
	policy(t, &req, "base: none\nnetwork: {domains: ['*'], agent_domains: none}\nenv: ['*']\n")
	report, err := Inspect(req)
	if err != nil {
		t.Fatal(err)
	}
	if report.SandboxRequired {
		t.Fatal("trusted outer policy labelled sandboxed")
	}
	var output bytes.Buffer
	if err := Render(&output, report); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "full OS-user authority (explicit policy selection)") {
		t.Fatal(output.String())
	}
	policy(t, &req, "base: none\nfs: ['rw:./']\nnetwork: {domains: ['*'], agent_domains: none}\nenv: ['*']\n")
	report, err = Inspect(req)
	if err != nil || !report.SandboxRequired {
		t.Fatal("filesystem policy lost native boundary", err)
	}
}
