package egg

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

// DrilledHole records one capability the system added on the agent's behalf that
// the user did not ask for. The sandbox is egg.yaml plus auto-drilled agent
// holes; without attribution those holes are invisible and the policy cannot be
// reviewed by the person relying on it.
type DrilledHole struct {
	Kind   string // "domain" | "write_dir" | "env" | "local_port"
	Value  string
	Agent  string
	Reason string
}

// EffectivePolicy is the fully resolved sandbox policy for a session: what the
// user declared, plus what the agent profile required, with every addition
// attributable. This is what `wt egg explain` renders.
type EffectivePolicy struct {
	Agent       string
	Mounts      []sandbox.Mount
	Deny        []string
	DenyWrite   []string
	NetworkNeed sandbox.NetworkNeed
	Domains     []string
	LocalPorts  []int
	Mode        string
	Drilled     []DrilledHole
	Derived     []DrilledHole
	Suppressed  []DrilledHole
}

// ResolvePolicy merges an egg config with the named agent's profile and reports
// the result along with the provenance of every automatic addition. An empty
// agent name (a plain shell or command session) drills nothing.
func ResolvePolicy(cfg *EggConfig, agent, home string) EffectivePolicy {
	policy, _ := ResolvePolicyWithProvider(cfg, agent, home, "")
	return policy
}

// ResolvePolicyWithProvider resolves the effective policy while treating an
// explicit provider URL as authoritative routing metadata. The derived exact
// host replaces the profile's static vendor domains and remains visible as a
// distinct source in explain output.
func ResolvePolicyWithProvider(cfg *EggConfig, agent, home, providerURL string) (EffectivePolicy, error) {
	mounts, deny, denyWrite := ParseFSRules(cfg.FS, home)

	policy := EffectivePolicy{
		Agent:     agent,
		Mounts:    mounts,
		Deny:      deny,
		DenyWrite: denyWrite,
		Domains:   append([]string(nil), cfg.Network.Domains...),
		Mode:      cfg.Network.Mode,
	}

	if agent != "" {
		profile := Profile(agent)
		if err := validateAgentDomainsMode(cfg.Network.AgentDomains); err != nil {
			return EffectivePolicy{}, err
		}

		providerHost, hasProvider, err := providerDomain(profile, providerURL)
		if err != nil {
			return EffectivePolicy{}, err
		}
		switch {
		case hasProvider:
			for _, domain := range profile.Domains {
				policy.Suppressed = append(policy.Suppressed, DrilledHole{
					Kind:   "domain",
					Value:  domain,
					Agent:  agent,
					Reason: "explicit WT_PROVIDER_BASE_URL replaces the agent's static provider domains",
				})
			}
			if !containsFold(policy.Domains, providerHost) && !containsFold(policy.Domains, "*") {
				policy.Domains = append(policy.Domains, providerHost)
			}
			policy.Derived = append(policy.Derived, DrilledHole{
				Kind:   "domain",
				Value:  providerHost,
				Agent:  agent,
				Reason: "exact host derived from WT_PROVIDER_BASE_URL",
			})
		case cfg.Network.AgentDomains == "none":
			for _, domain := range profile.Domains {
				policy.Suppressed = append(policy.Suppressed, DrilledHole{
					Kind:   "domain",
					Value:  domain,
					Agent:  agent,
					Reason: "network.agent_domains is none",
				})
			}
		default:
			for _, domain := range profile.Domains {
				if containsFold(policy.Domains, domain) || containsFold(policy.Domains, "*") {
					continue
				}
				policy.Domains = append(policy.Domains, domain)
				policy.Drilled = append(policy.Drilled, DrilledHole{
					Kind:   "domain",
					Value:  domain,
					Agent:  agent,
					Reason: fmt.Sprintf("agent %q requires network access to %s", agent, domain),
				})
			}
		}

		if home != "" {
			for _, dir := range append(append([]string(nil), profile.WriteRegex...), profile.WriteDirs...) {
				path := filepath.Join(home, dir)
				if hasMount(policy.Mounts, path) {
					continue
				}
				policy.Mounts = append(policy.Mounts, sandbox.Mount{Source: path, Target: path})
				policy.Drilled = append(policy.Drilled, DrilledHole{
					Kind:   "write_dir",
					Value:  path,
					Agent:  agent,
					Reason: fmt.Sprintf("agent %q stores state in %s", agent, dir),
				})
			}
		}

		for _, name := range profile.EnvVars {
			policy.Drilled = append(policy.Drilled, DrilledHole{
				Kind:   "env",
				Value:  name,
				Agent:  agent,
				Reason: fmt.Sprintf("agent %q reads %s from the host environment", agent, name),
			})
		}
	}

	policy.NetworkNeed = sandbox.NetworkNeedFromDomains(policy.Domains)
	policy.LocalPorts = InferLocalPorts(cfg, agent, providerURL)
	for _, port := range policy.LocalPorts {
		if containsInt(cfg.Network.LocalPorts, port) {
			continue
		}
		policy.Drilled = append(policy.Drilled, DrilledHole{
			Kind:   "local_port",
			Value:  strconv.Itoa(port),
			Agent:  agent,
			Reason: "loopback service must be forwarded into the network namespace",
		})
	}

	return policy, nil
}

func providerDomain(profile AgentProfile, raw string) (string, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || !containsExact(profile.EnvVars, "WT_PROVIDER_BASE_URL") {
		return "", false, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || !parsed.IsAbs() {
		return "", false, fmt.Errorf("WT_PROVIDER_BASE_URL must be an absolute URL with a host")
	}
	if parsed.User != nil {
		return "", false, fmt.Errorf("WT_PROVIDER_BASE_URL must not contain userinfo")
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	loopback := isLoopbackDomain(host)
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && loopback) {
		return "", false, fmt.Errorf("WT_PROVIDER_BASE_URL must use https; http is allowed only for loopback")
	}
	if ip := net.ParseIP(host); ip != nil && !ip.IsLoopback() {
		return "", false, fmt.Errorf("WT_PROVIDER_BASE_URL must use a hostname; IP literals are allowed only for loopback")
	}
	return host, true, nil
}

func containsExact(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func isLoopbackDomain(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

// defaultAgentPorts are loopback services an agent profile implies but cannot
// express as a domain. Keeping this narrow and explicit is deliberate: a
// forwarded port is a hole in the network namespace.
var defaultAgentPorts = map[string]int{
	"ollama": 11434,
}

// InferLocalPorts returns the host loopback ports that must be forwarded into
// the jail's network namespace.
//
// Retaining CLONE_NEWNET means the jail's 127.0.0.1 is not the host's, so every
// local model provider breaks unless its port is forwarded. Existing configs
// declare loopback intent with domain literals rather than ports, so the ports
// are inferred from the agent profile and the provider URL. Explicit
// network.local_ports entries are always honored.
func InferLocalPorts(cfg *EggConfig, agent, providerURL string) []int {
	ports := append([]int(nil), cfg.Network.LocalPorts...)
	providerPort, hasProviderPort := loopbackPortFromURL(providerURL)

	if declaresLoopback(cfg.Network.Domains) || len(cfg.Network.LocalPorts) > 0 || hasProviderPort {
		if port, ok := defaultAgentPorts[agent]; ok {
			ports = append(ports, port)
		}
		if hasProviderPort {
			ports = append(ports, providerPort)
		}
	}

	if len(ports) == 0 {
		return nil
	}
	ports = mergeIntSet(ports, nil)
	sort.Ints(ports)
	return ports
}

// declaresLoopback reports whether the domain list names a loopback host, which
// is how every current agent profile expresses "I talk to something local".
func declaresLoopback(domains []string) bool {
	for _, d := range domains {
		switch strings.ToLower(d) {
		case "localhost", "127.0.0.1", "::1":
			return true
		}
	}
	return false
}

func loopbackPortFromURL(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return 0, false
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "localhost", "127.0.0.1", "::1":
	default:
		return 0, false
	}
	if p := parsed.Port(); p != "" {
		port, err := strconv.Atoi(p)
		if err != nil {
			return 0, false
		}
		return port, true
	}
	switch parsed.Scheme {
	case "https":
		return 443, true
	case "http":
		return 80, true
	}
	return 0, false
}

func hasMount(mounts []sandbox.Mount, path string) bool {
	for _, m := range mounts {
		if m.Source == path {
			return true
		}
	}
	return false
}

func containsFold(list []string, want string) bool {
	for _, v := range list {
		if strings.EqualFold(v, want) {
			return true
		}
	}
	return false
}

func containsInt(list []int, want int) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
