package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
)

// TestExplainPolicyAttributesEveryAgentHole is the point of the command: a hole
// the user did not ask for must name the agent that caused it and say why.
func TestExplainPolicyAttributesEveryAgentHole(t *testing.T) {
	cfg, err := egg.LoadEggConfigFromYAML("fs: [\"rw:./\"]\nnetwork: [corp.example]\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	p := eggclient.ExplainPolicy(cfg, "claude", "/home/test", "egg.yaml")

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
		var found *eggclient.ExplainedHole
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

		var p eggclient.ExplainedPolicy
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
