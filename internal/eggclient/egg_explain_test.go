package eggclient

import (
	"bytes"
	"encoding/json"

	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

// TestExplainEnforcementReportsPlatformTruth pins the kernel-enforced network
// boundary. Linux keeps CLONE_NEWNET and admits only inherited relays, so its
// domain and loopback modes are no longer advisory.
func TestExplainEnforcementReportsPlatformTruth(t *testing.T) {
	tests := []struct {
		goos string
		need sandbox.NetworkNeed
		want string
	}{
		{"darwin", sandbox.NetworkNone, "none"},
		{"darwin", sandbox.NetworkLocal, "kernel"},
		{"darwin", sandbox.NetworkHTTPS, "proxy"},
		{"darwin", sandbox.NetworkFull, "unrestricted"},
		{"linux", sandbox.NetworkNone, "none"},
		{"linux", sandbox.NetworkLocal, "proxy"},
		{"linux", sandbox.NetworkHTTPS, "proxy"},
		{"linux", sandbox.NetworkFull, "proxy"},
	}

	for _, tc := range tests {
		t.Run(tc.goos+"/"+tc.need.String(), func(t *testing.T) {
			if got := ExplainEnforcement(tc.need, tc.goos, ""); got != tc.want {
				t.Errorf("explainEnforcement(%v, %q) = %q, want %q", tc.need, tc.goos, got, tc.want)
			}
		})
	}
	if got := ExplainEnforcement(sandbox.NetworkHTTPS, "linux", "observe"); got != "proxy-observe" {
		t.Fatalf("Linux observe enforcement = %q", got)
	}
}

// TestExplainPolicyShellSessionDrillsNothing — no agent, no automatic holes.
// This is the baseline a reviewer compares an agent session against.
func TestExplainPolicyShellSessionDrillsNothing(t *testing.T) {
	cfg, err := egg.LoadEggConfigFromYAML("fs: [\"rw:./\"]\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	p := ExplainPolicy(cfg, "", "/home/test", "built-in defaults")

	if len(p.Drilled) != 0 {
		t.Errorf("shell session drilled %d holes: %+v", len(p.Drilled), p.Drilled)
	}
	if p.NetworkNeed != "none" {
		t.Errorf("NetworkNeed = %q, want none", p.NetworkNeed)
	}
}

// TestExplainPolicyJSONShape pins the wire contract. An AI consumer reads these
// keys; renaming one is a breaking change to the API surface, not a refactor.
func TestExplainPolicyJSONShape(t *testing.T) {
	cfg, err := egg.LoadEggConfigFromYAML("fs: [\"rw:./\", \"deny:~/.ssh\"]\nnetwork:\n  domains: [corp.example]\n  local_ports: [11434]\n  mode: observe\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	var buf bytes.Buffer
	if err := WritePolicyJSON(&buf, ExplainPolicy(cfg, "claude", "/home/test", "egg.yaml")); err != nil {
		t.Fatalf("writePolicyJSON: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}

	for _, key := range []string{
		"agent", "config_source", "network_need", "enforcement",
		"domains", "local_ports", "mode", "mounts", "deny", "deny_write", "drilled", "derived", "suppressed",
	} {
		if _, ok := got[key]; !ok {
			t.Errorf("missing key %q in %s", key, buf.String())
		}
	}

	if got["mode"] != "observe" {
		t.Errorf("mode = %v, want observe", got["mode"])
	}
	ports, ok := got["local_ports"].([]any)
	if !ok || len(ports) != 1 || ports[0].(float64) != 11434 {
		t.Errorf("local_ports = %v, want [11434]", got["local_ports"])
	}

	drilled, ok := got["drilled"].([]any)
	if !ok || len(drilled) == 0 {
		t.Fatalf("drilled = %v, want a non-empty array", got["drilled"])
	}
	first := drilled[0].(map[string]any)
	for _, key := range []string{"kind", "value", "agent", "reason"} {
		if _, ok := first[key]; !ok {
			t.Errorf("drilled entry missing key %q: %v", key, first)
		}
	}

	mounts, ok := got["mounts"].([]any)
	if !ok || len(mounts) == 0 {
		t.Fatalf("mounts = %v, want a non-empty array", got["mounts"])
	}
	m := mounts[0].(map[string]any)
	for _, key := range []string{"source", "target", "read_only"} {
		if _, ok := m[key]; !ok {
			t.Errorf("mount entry missing key %q: %v", key, m)
		}
	}
}

func TestExplainPolicyShowsDerivedAndSuppressedDomains(t *testing.T) {
	cfg, err := egg.LoadEggConfigFromYAML("network:\n  domains: []\n  agent_domains: none")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := ExplainPolicyWithProvider(cfg, "opencode", "/home/test", "egg.yaml", "https://api.arliai.com/v1")
	if err != nil {
		t.Fatal(err)
	}
	if len(policy.Domains) != 1 || policy.Domains[0] != "api.arliai.com" {
		t.Fatalf("domains = %v, want one derived provider host", policy.Domains)
	}
	var buf bytes.Buffer
	if err := RenderPolicy(&buf, policy); err != nil {
		t.Fatal(err)
	}
	output := buf.String()
	for _, want := range []string{"domains (1)", "api.arliai.com", "derived", "suppressed agent domains", "*.openai.com"} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q:\n%s", want, output)
		}
	}
}

// TestRenderPolicyExplainsWhy — the human rendering must carry the same
// attribution as the JSON, or the two surfaces disagree about what the sandbox is.
func TestRenderPolicyExplainsWhy(t *testing.T) {
	cfg, err := egg.LoadEggConfigFromYAML("fs: [\"rw:./\"]\nnetwork: [corp.example]\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p := ExplainPolicy(cfg, "claude", "/home/test", "egg.yaml")

	var buf bytes.Buffer
	if err := RenderPolicy(&buf, p); err != nil {
		t.Fatalf("renderPolicy: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "claude") {
		t.Error("rendering never names the agent")
	}
	if !strings.Contains(out, "corp.example") {
		t.Error("rendering omits the user-declared domain")
	}
	for _, h := range p.Drilled {
		if h.Kind != "domain" {
			continue
		}
		if !strings.Contains(out, h.Value) {
			t.Errorf("rendering omits drilled domain %q", h.Value)
		}
	}
	if !strings.Contains(out, p.Enforcement) {
		t.Errorf("rendering omits the enforcement label %q", p.Enforcement)
	}
	// Provenance is the whole point — a reader must be able to tell a hole they
	// declared from one the system added.
	if !strings.Contains(out, "auto") {
		t.Error("rendering does not mark auto-drilled holes")
	}
}

// TestRenderPolicyAndJSONAgree — human output is a rendering of the structured
// data, never a second source of truth (CLAUDE.md).
func TestRenderPolicyAndJSONAgree(t *testing.T) {
	cfg, err := egg.LoadEggConfigFromYAML("fs: [\"rw:./\"]\nnetwork: [corp.example]\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p := ExplainPolicy(cfg, "claude", "/home/test", "egg.yaml")

	var jsonBuf, humanBuf bytes.Buffer
	if err := WritePolicyJSON(&jsonBuf, p); err != nil {
		t.Fatalf("writePolicyJSON: %v", err)
	}
	if err := RenderPolicy(&humanBuf, p); err != nil {
		t.Fatalf("renderPolicy: %v", err)
	}

	var decoded ExplainedPolicy
	if err := json.Unmarshal(jsonBuf.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, d := range decoded.Domains {
		if !strings.Contains(humanBuf.String(), d) {
			t.Errorf("domain %q is in JSON but not in the human rendering", d)
		}
	}
	if decoded.Enforcement != p.Enforcement {
		t.Errorf("JSON enforcement %q != policy enforcement %q", decoded.Enforcement, p.Enforcement)
	}
	if len(decoded.Drilled) != len(p.Drilled) {
		t.Errorf("JSON reported %d holes, policy has %d", len(decoded.Drilled), len(p.Drilled))
	}
}
