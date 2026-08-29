package egg

import (
	"reflect"
	"testing"
)

// TestResolvePolicyReportsDrilledAgentHoles is the core of `wt egg explain`.
// The sandbox is egg.yaml plus auto-drilled agent holes, and today those holes
// are invisible. Every hole must be attributable to the agent that needed it.
func TestResolvePolicyReportsDrilledAgentHoles(t *testing.T) {
	cfg, err := LoadEggConfigFromYAML("fs: [\"rw:./\"]\nnetwork: [\"example.com\"]\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	policy := ResolvePolicy(cfg, "claude", "/home/test")

	// The user's own declaration survives untouched.
	if !containsString(policy.Domains, "example.com") {
		t.Errorf("user domain missing from resolved policy: %v", policy.Domains)
	}
	// The agent profile's domains were drilled in.
	if !containsString(policy.Domains, "*.anthropic.com") {
		t.Errorf("agent domain not drilled: %v", policy.Domains)
	}

	// And every drilled hole is attributable.
	var found bool
	for _, hole := range policy.Drilled {
		if hole.Kind == "domain" && hole.Value == "*.anthropic.com" {
			found = true
			if hole.Reason == "" {
				t.Error("drilled hole has no reason")
			}
			if hole.Agent != "claude" {
				t.Errorf("drilled hole agent = %q, want claude", hole.Agent)
			}
		}
	}
	if !found {
		t.Errorf("no drilled record for *.anthropic.com; got %+v", policy.Drilled)
	}
}

// TestResolvePolicyDrillsAgentWriteDirs — the agent's own state directories are
// holes too, and are the ones users are most surprised by.
func TestResolvePolicyDrillsAgentWriteDirs(t *testing.T) {
	cfg := DefaultEggConfig()
	policy := ResolvePolicy(cfg, "claude", "/home/test")

	var found bool
	for _, hole := range policy.Drilled {
		if hole.Kind == "write_dir" && hole.Value == "/home/test/.claude" {
			found = true
		}
	}
	if !found {
		t.Errorf("agent write dir not drilled; got %+v", policy.Drilled)
	}
}

// TestResolvePolicyWithNoAgentDrillsNothing — a plain `wt terminal` shell has no
// agent profile, so the effective policy must be exactly what the user declared.
func TestResolvePolicyWithNoAgentDrillsNothing(t *testing.T) {
	cfg, err := LoadEggConfigFromYAML("fs: [\"rw:./\"]\nnetwork: [\"example.com\"]\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	policy := ResolvePolicy(cfg, "", "/home/test")
	if len(policy.Drilled) != 0 {
		t.Errorf("drilled holes for a shell session: %+v", policy.Drilled)
	}
	if !reflect.DeepEqual(policy.Domains, []string{"example.com"}) {
		t.Errorf("domains = %v, want [example.com]", policy.Domains)
	}
}

func TestResolvePolicyCanSuppressAgentDomains(t *testing.T) {
	cfg, err := LoadEggConfigFromYAML("network:\n  domains: [api.arliai.com]\n  agent_domains: none")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := ResolvePolicyWithProvider(cfg, "opencode", "/home/test", "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(policy.Domains, []string{"api.arliai.com"}) {
		t.Fatalf("domains = %v, want only declared provider", policy.Domains)
	}
	if len(policy.Suppressed) != len(Profile("opencode").Domains) {
		t.Fatalf("suppressed = %d, want %d profile domains", len(policy.Suppressed), len(Profile("opencode").Domains))
	}
}

func TestResolvePolicyDerivesExactProviderHost(t *testing.T) {
	cfg, err := LoadEggConfigFromYAML("network:\n  domains: []\n  agent_domains: none")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := ResolvePolicyWithProvider(cfg, "opencode", "/home/test", "https://api.arliai.com/v1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(policy.Domains, []string{"api.arliai.com"}) {
		t.Fatalf("domains = %v, want exact derived provider host", policy.Domains)
	}
	if len(policy.Derived) != 1 || policy.Derived[0].Value != "api.arliai.com" {
		t.Fatalf("derived provenance = %+v", policy.Derived)
	}
	if len(policy.Suppressed) != len(Profile("opencode").Domains) {
		t.Fatalf("suppressed = %d, want %d profile domains", len(policy.Suppressed), len(Profile("opencode").Domains))
	}
}

func TestProviderURLGuardrails(t *testing.T) {
	cfg := &EggConfig{}
	tests := []string{
		"http://api.arliai.com/v1",
		"https://user:pass@api.arliai.com/v1",
		"https://203.0.113.7/v1",
		"api.arliai.com/v1",
	}
	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			if _, err := ResolvePolicyWithProvider(cfg, "opencode", "/home/test", raw); err == nil {
				t.Fatalf("provider URL %q was accepted", raw)
			}
		})
	}
	for _, raw := range []string{"http://localhost:4000/v1", "http://127.0.0.1:4000/v1"} {
		t.Run(raw, func(t *testing.T) {
			if _, err := ResolvePolicyWithProvider(cfg, "opencode", "/home/test", raw); err != nil {
				t.Fatalf("loopback provider URL %q: %v", raw, err)
			}
		})
	}
}

// TestInferLocalPorts covers the loopback trap: retaining CLONE_NEWNET means the
// jail's 127.0.0.1 is not the host's, so every local model provider breaks unless
// its port is forwarded. Existing configs declare loopback via domain literals,
// so the ports must be inferable without a config edit.
func TestInferLocalPorts(t *testing.T) {
	tests := []struct {
		name        string
		yaml        string
		agent       string
		providerURL string
		want        []int
	}{
		{
			name:  "ollama profile alone implies its default port",
			yaml:  "{}",
			agent: "ollama",
			want:  []int{11434},
		},
		{
			name:  "suppressing the ollama profile suppresses its port",
			yaml:  "network:\n  agent_domains: none",
			agent: "ollama",
			want:  nil,
		},
		{
			name:        "unsupported provider URL does not drill a port",
			yaml:        "network:\n  agent_domains: none",
			agent:       "ollama",
			providerURL: "http://localhost:4000/v1",
			want:        nil,
		},
		{
			name:        "provider base url on loopback is forwarded",
			yaml:        "network: [localhost]",
			agent:       "codex",
			providerURL: "http://localhost:4000/v1",
			want:        []int{4000},
		},
		{
			name:        "remote provider needs no forwarding",
			yaml:        "network: [api.openai.com]",
			agent:       "codex",
			providerURL: "https://api.openai.com/v1",
			want:        nil,
		},
		{
			name:  "explicit local_ports are honored",
			yaml:  "network:\n  domains: [localhost]\n  local_ports: [8080]",
			agent: "",
			want:  []int{8080},
		},
		{
			name:        "explicit and inferred are merged and deduped",
			yaml:        "network:\n  domains: [localhost]\n  local_ports: [11434, 8080]",
			agent:       "ollama",
			providerURL: "http://127.0.0.1:8080",
			want:        []int{8080, 11434},
		},
		{
			name:  "no loopback declared means no forwarding",
			yaml:  "network: [example.com]",
			agent: "claude",
			want:  nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := LoadEggConfigFromYAML(tc.yaml)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			got := InferLocalPorts(cfg, tc.agent, tc.providerURL)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("InferLocalPorts = %v, want %v", got, tc.want)
			}
		})
	}
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
