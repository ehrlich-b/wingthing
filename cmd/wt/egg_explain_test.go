package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

// TestExplainEnforcementReportsPlatformTruth pins the central defect from
// docs/sandbox-enhancement-design.md: the same egg.yaml is a real boundary on
// macOS and a suggestion on Linux. `wt egg explain` is worse than useless if it
// hides that, so the enforcement label is per-platform and tested per-platform.
//
// The linux https/local rows are expected to flip to "proxy" when Phase 3 lands
// (keep CLONE_NEWNET, force traffic through the proxy). This test failing after
// that change is the change working.
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
		{"linux", sandbox.NetworkLocal, "advisory"},
		{"linux", sandbox.NetworkHTTPS, "advisory"},
		{"linux", sandbox.NetworkFull, "unrestricted"},
	}

	for _, tc := range tests {
		t.Run(tc.goos+"/"+tc.need.String(), func(t *testing.T) {
			if got := explainEnforcement(tc.need, tc.goos); got != tc.want {
				t.Errorf("explainEnforcement(%v, %q) = %q, want %q", tc.need, tc.goos, got, tc.want)
			}
		})
	}
}

// TestExplainPolicyAttributesEveryAgentHole is the point of the command: a hole
// the user did not ask for must name the agent that caused it and say why.
func TestExplainPolicyAttributesEveryAgentHole(t *testing.T) {
	cfg, err := egg.LoadEggConfigFromYAML("fs: [\"rw:./\"]\nnetwork: [corp.example]\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	p := explainPolicy(cfg, "claude", "/home/test", "egg.yaml")

	if p.Agent != "claude" {
		t.Errorf("Agent = %q, want claude", p.Agent)
	}
	if p.ConfigSource != "egg.yaml" {
		t.Errorf("ConfigSource = %q, want egg.yaml", p.ConfigSource)
	}

	// The user's own domain survives and is NOT reported as drilled.
	if !containsString(p.Domains, "corp.example") {
		t.Errorf("Domains %v lost the user-declared domain", p.Domains)
	}
	for _, h := range p.Drilled {
		if h.Value == "corp.example" {
			t.Errorf("user-declared domain reported as auto-drilled: %+v", h)
		}
	}

	// Every domain the claude profile requires is present and attributed.
	profile := egg.Profile("claude")
	for _, d := range profile.Domains {
		if !containsString(p.Domains, d) {
			t.Errorf("profile domain %q missing from resolved domains %v", d, p.Domains)
			continue
		}
		var found *explainedHole
		for i := range p.Drilled {
			if p.Drilled[i].Kind == "domain" && p.Drilled[i].Value == d {
				found = &p.Drilled[i]
				break
			}
		}
		if found == nil {
			t.Errorf("profile domain %q was added but not attributed", d)
			continue
		}
		if found.Agent != "claude" {
			t.Errorf("hole %q attributed to %q, want claude", d, found.Agent)
		}
		if found.Reason == "" {
			t.Errorf("hole %q has no reason", d)
		}
	}

	// The agent's env reads are holes too — a capability arriving by a channel
	// that is not a path is exactly the class we under-reported before.
	var sawEnv bool
	for _, h := range p.Drilled {
		if h.Kind == "env" && h.Value == "ANTHROPIC_API_KEY" {
			sawEnv = true
		}
	}
	if !sawEnv {
		t.Error("ANTHROPIC_API_KEY passthrough not reported as a drilled hole")
	}
}

// TestExplainPolicyShellSessionDrillsNothing — no agent, no automatic holes.
// This is the baseline a reviewer compares an agent session against.
func TestExplainPolicyShellSessionDrillsNothing(t *testing.T) {
	cfg, err := egg.LoadEggConfigFromYAML("fs: [\"rw:./\"]\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	p := explainPolicy(cfg, "", "/home/test", "built-in defaults")

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
	if err := writePolicyJSON(&buf, explainPolicy(cfg, "claude", "/home/test", "egg.yaml")); err != nil {
		t.Fatalf("writePolicyJSON: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}

	for _, key := range []string{
		"agent", "config_source", "network_need", "enforcement",
		"domains", "local_ports", "mode", "mounts", "deny", "deny_write", "drilled",
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

// TestRenderPolicyExplainsWhy — the human rendering must carry the same
// attribution as the JSON, or the two surfaces disagree about what the sandbox is.
func TestRenderPolicyExplainsWhy(t *testing.T) {
	cfg, err := egg.LoadEggConfigFromYAML("fs: [\"rw:./\"]\nnetwork: [corp.example]\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p := explainPolicy(cfg, "claude", "/home/test", "egg.yaml")

	var buf bytes.Buffer
	if err := renderPolicy(&buf, p); err != nil {
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
	p := explainPolicy(cfg, "claude", "/home/test", "egg.yaml")

	var jsonBuf, humanBuf bytes.Buffer
	if err := writePolicyJSON(&jsonBuf, p); err != nil {
		t.Fatalf("writePolicyJSON: %v", err)
	}
	if err := renderPolicy(&humanBuf, p); err != nil {
		t.Fatalf("renderPolicy: %v", err)
	}

	var decoded explainedPolicy
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

// TestEggExplainCommand drives the actual cobra command end to end, so the
// command is proven wired up and not just its helpers.
func TestEggExplainCommand(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "egg.yaml")
	if err := os.WriteFile(path, []byte("base: none\nfs: [\"rw:./\"]\nnetwork: [corp.example]\n"), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Run("json", func(t *testing.T) {
		cmd := eggExplainCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"claude", "--config", path, "--json"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("execute: %v\n%s", err, out.String())
		}

		var p explainedPolicy
		if err := json.Unmarshal(out.Bytes(), &p); err != nil {
			t.Fatalf("not JSON: %v\n%s", err, out.String())
		}
		if p.Agent != "claude" {
			t.Errorf("agent = %q, want claude", p.Agent)
		}
		if !containsString(p.Domains, "corp.example") {
			t.Errorf("domains %v missing corp.example", p.Domains)
		}
		if len(p.Drilled) == 0 {
			t.Error("no holes attributed for a claude session")
		}
	})

	t.Run("human", func(t *testing.T) {
		cmd := eggExplainCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"claude", "--config", path})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("execute: %v\n%s", err, out.String())
		}
		if !strings.Contains(out.String(), "corp.example") {
			t.Errorf("human output missing the declared domain:\n%s", out.String())
		}
	})

	t.Run("missing config is an error", func(t *testing.T) {
		cmd := eggExplainCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"claude", "--config", filepath.Join(dir, "nope.yaml")})
		if err := cmd.Execute(); err == nil {
			t.Error("expected an error for a missing --config path")
		}
	})
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
