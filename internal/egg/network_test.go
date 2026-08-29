package egg

import (
	"reflect"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/sandbox"
	"gopkg.in/yaml.v3"
)

// TestNetworkFieldAcceptsAllThreeForms is the backwards-compatibility guard for
// the `network:` key. The scalar and list forms are a frozen contract (see
// docs/sandbox-enhancement-design.md); the mapping form is the additive third
// shape, following the same scalar-or-object pattern BaseField already uses.
func TestNetworkFieldAcceptsAllThreeForms(t *testing.T) {
	tests := []struct {
		name             string
		yaml             string
		wantDomains      []string
		wantPorts        []int
		wantMode         string
		wantLog          *bool
		wantAgentDomains string
	}{
		{"scalar none", "network: none", nil, nil, "", nil, ""},
		{"scalar empty", `network: ""`, nil, nil, "", nil, ""},
		{"scalar wildcard", `network: "*"`, []string{"*"}, nil, "", nil, ""},
		{"scalar single domain", "network: api.anthropic.com", []string{"api.anthropic.com"}, nil, "", nil, ""},
		{"list", "network:\n  - a.example\n  - b.example", []string{"a.example", "b.example"}, nil, "", nil, ""},
		{
			"mapping",
			"network:\n  domains: [a.example]\n  local_ports: [11434]\n  mode: observe\n  log: false\n  agent_domains: none",
			[]string{"a.example"}, []int{11434}, "observe", boolPointer(false), "none",
		},
		{
			"mapping domains only",
			"network:\n  domains: [a.example]",
			[]string{"a.example"}, nil, "", nil, "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := LoadEggConfigFromYAML(tc.yaml)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if !reflect.DeepEqual(cfg.Network.Domains, tc.wantDomains) {
				t.Errorf("domains = %v, want %v", cfg.Network.Domains, tc.wantDomains)
			}
			if !reflect.DeepEqual(cfg.Network.LocalPorts, tc.wantPorts) {
				t.Errorf("local_ports = %v, want %v", cfg.Network.LocalPorts, tc.wantPorts)
			}
			if cfg.Network.Mode != tc.wantMode {
				t.Errorf("mode = %q, want %q", cfg.Network.Mode, tc.wantMode)
			}
			if !reflect.DeepEqual(cfg.Network.Log, tc.wantLog) {
				t.Errorf("log = %v, want %v", cfg.Network.Log, tc.wantLog)
			}
			if cfg.Network.AgentDomains != tc.wantAgentDomains {
				t.Errorf("agent_domains = %q, want %q", cfg.Network.AgentDomains, tc.wantAgentDomains)
			}
		})
	}
}

func TestNetworkFieldLegacyLogRoundTripsAndMerges(t *testing.T) {
	parent, err := LoadEggConfigFromYAML("network:\n  domains: [a.example]\n  log: true")
	if err != nil {
		t.Fatal(err)
	}
	child, err := LoadEggConfigFromYAML("network:\n  domains: [b.example]\n  log: false")
	if err != nil {
		t.Fatal(err)
	}
	merged := MergeEggConfig(parent, child)
	if merged.Network.Log == nil || *merged.Network.Log {
		t.Fatalf("child network.log override was not preserved: %v", merged.Network.Log)
	}
	rendered, err := yaml.Marshal(merged)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(rendered), "log: false") {
		t.Fatalf("legacy network.log disappeared during render:\n%s", rendered)
	}
}

func boolPointer(value bool) *bool { return &value }

func TestNetworkFieldRejectsUnknownAgentDomainsMode(t *testing.T) {
	_, err := LoadEggConfigFromYAML("network:\n  domains: [a.example]\n  agent_domains: replace")
	if err == nil || !contains(err.Error(), "agent_domains must be merge or none") {
		t.Fatalf("error = %v, want agent_domains validation", err)
	}
}

func TestNetworkFieldRejectsUnknownFieldsModesAndPorts(t *testing.T) {
	for name, body := range map[string]string{
		"unknown field": "network:\n  domains: [a.example]\n  local_port: 11434",
		"unknown mode":  "network:\n  domains: [a.example]\n  mode: maybe",
		"zero port":     "network:\n  domains: [localhost]\n  local_ports: [0]",
		"large port":    "network:\n  domains: [localhost]\n  local_ports: [65536]",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadEggConfigFromYAML(body); err == nil {
				t.Fatalf("accepted invalid network policy:\n%s", body)
			}
		})
	}
}

// TestNetworkFieldRoundTripsScalarAndList guards RenderedConfig stability: a
// config written with the legacy forms must not be re-emitted as a mapping,
// because the rendered YAML is surfaced to users and to the browser UI.
func TestNetworkFieldRoundTripsScalarAndList(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{"list stays a list", "network:\n  - a.example\n  - b.example", "network:\n    - a.example\n    - b.example"},
		// yaml.v3 single-quotes "*" because it is the alias indicator; both
		// forms parse back to the same scalar.
		{"wildcard stays scalar", `network: "*"`, `network: '*'`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := LoadEggConfigFromYAML(tc.yaml)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			out, err := yaml.Marshal(cfg)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if got := string(out); !contains(got, tc.want) {
				t.Errorf("marshal =\n%s\nwant to contain:\n%s", got, tc.want)
			}
		})
	}
}

// TestToSandboxConfigUnchangedForLegacyForms pins that adding the mapping form
// did not move any existing behavior. These are the exact values the sandbox
// layer consumed before the change.
func TestToSandboxConfigUnchangedForLegacyForms(t *testing.T) {
	tests := []struct {
		name     string
		yaml     string
		wantNeed sandbox.NetworkNeed
		wantDoms []string
	}{
		{"none", "network: none", sandbox.NetworkNone, nil},
		{"wildcard", `network: "*"`, sandbox.NetworkFull, []string{"*"}},
		{"domains", "network: [a.example]", sandbox.NetworkHTTPS, []string{"a.example"}},
		{"loopback only", "network: [localhost, 127.0.0.1]", sandbox.NetworkLocal, []string{"localhost", "127.0.0.1"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := LoadEggConfigFromYAML(tc.yaml)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			sc := cfg.ToSandboxConfig("/home/test")
			if sc.NetworkNeed != tc.wantNeed {
				t.Errorf("NetworkNeed = %v, want %v", sc.NetworkNeed, tc.wantNeed)
			}
			if !reflect.DeepEqual(sc.Domains, tc.wantDoms) {
				t.Errorf("Domains = %v, want %v", sc.Domains, tc.wantDoms)
			}
			if len(sc.LocalPorts) != 0 || sc.NetworkMode != "" {
				t.Errorf("legacy mapping-only fields changed: ports=%v mode=%q", sc.LocalPorts, sc.NetworkMode)
			}
		})
	}
}

func TestToSandboxConfigCarriesMappingOnlyNetworkPolicy(t *testing.T) {
	cfg, err := LoadEggConfigFromYAML("network:\n  domains: [a.example]\n  local_ports: [11434]\n  mode: observe")
	if err != nil {
		t.Fatal(err)
	}
	sandboxConfig := cfg.ToSandboxConfig("/home/test")
	if sandboxConfig.NetworkMode != "observe" || !reflect.DeepEqual(sandboxConfig.LocalPorts, []int{11434}) {
		t.Fatalf("sandbox network mapping = mode %q ports %v", sandboxConfig.NetworkMode, sandboxConfig.LocalPorts)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
