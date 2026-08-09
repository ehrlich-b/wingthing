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
		name        string
		yaml        string
		wantDomains []string
		wantPorts   []int
		wantMode    string
	}{
		{"scalar none", "network: none", nil, nil, ""},
		{"scalar empty", `network: ""`, nil, nil, ""},
		{"scalar wildcard", `network: "*"`, []string{"*"}, nil, ""},
		{"scalar single domain", "network: api.anthropic.com", []string{"api.anthropic.com"}, nil, ""},
		{"list", "network:\n  - a.example\n  - b.example", []string{"a.example", "b.example"}, nil, ""},
		{
			"mapping",
			"network:\n  domains: [a.example]\n  local_ports: [11434]\n  mode: observe",
			[]string{"a.example"}, []int{11434}, "observe",
		},
		{
			"mapping domains only",
			"network:\n  domains: [a.example]",
			[]string{"a.example"}, nil, "",
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
		})
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
