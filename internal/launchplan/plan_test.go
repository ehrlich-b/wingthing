package launchplan

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	if report.CWD.Resolved != dir {
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
	policy(t, &req, "agent_settings: {codex: '"+token+"'}\n")
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
