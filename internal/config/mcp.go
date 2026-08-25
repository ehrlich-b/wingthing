package config

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	maxDirectMCPSessions      = 4096
	maxDirectMCPSpawnsPerHour = 100000
)

// DirectMCPConfig optionally narrows the built-in native direct-control policy.
// A missing direct_mcp section preserves the compatible default operation set and
// bounded quotas. Operators can disable the surface, select an allow/deny subset of
// grant names, or override the positive bounds without configuring every user.
type DirectMCPConfig struct {
	Disabled         bool     `yaml:"disabled,omitempty"`
	AllowGrants      []string `yaml:"allow_grants,omitempty"`
	DenyGrants       []string `yaml:"deny_grants,omitempty"`
	MaxSessions      int      `yaml:"max_sessions,omitempty"`
	MaxSpawnsPerHour int      `yaml:"max_spawns_per_hour,omitempty"`
}

// UnmarshalYAML decodes the security-sensitive direct policy strictly even though
// wing.yaml remains permissive at its compatibility-oriented top level.
func (p *DirectMCPConfig) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("direct_mcp must be a mapping")
	}
	if err := rejectUnknownYAMLKeys(value, map[string]bool{
		"disabled": true, "allow_grants": true, "deny_grants": true,
		"max_sessions": true, "max_spawns_per_hour": true,
	}, "direct_mcp"); err != nil {
		return err
	}
	type plainDirectMCPConfig DirectMCPConfig
	var decoded plainDirectMCPConfig
	if err := value.Decode(&decoded); err != nil {
		return err
	}
	*p = DirectMCPConfig(decoded)
	return nil
}

// Validate rejects ambiguous grant policy and nonsensical resource bounds. Grant
// existence is checked against the direct operation registry by the wing so the
// low-level config package does not own a second grant catalog.
func (p *DirectMCPConfig) Validate() error {
	if len(p.AllowGrants) > 0 && len(p.DenyGrants) > 0 {
		return fmt.Errorf("direct_mcp cannot set both allow_grants and deny_grants")
	}
	var err error
	if p.AllowGrants, err = normalizedDirectMCPGrants(p.AllowGrants, "allow_grants"); err != nil {
		return err
	}
	if p.DenyGrants, err = normalizedDirectMCPGrants(p.DenyGrants, "deny_grants"); err != nil {
		return err
	}
	if p.MaxSessions < 0 || p.MaxSessions > maxDirectMCPSessions {
		return fmt.Errorf("direct_mcp max_sessions must be between 0 and %d", maxDirectMCPSessions)
	}
	if p.MaxSpawnsPerHour < 0 || p.MaxSpawnsPerHour > maxDirectMCPSpawnsPerHour {
		return fmt.Errorf("direct_mcp max_spawns_per_hour must be between 0 and %d", maxDirectMCPSpawnsPerHour)
	}
	return nil
}

func normalizedDirectMCPGrants(values []string, field string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("direct_mcp contains an empty %s entry", field)
		}
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out, nil
}

// MCPRoleConfig controls which privileged tools one role may use over the remote MCP
// surface. It does not affect tools available to in-wing agents.
type MCPRoleConfig struct {
	Enabled bool     `yaml:"enabled"`
	Allow   []string `yaml:"allow,omitempty"`
	Deny    []string `yaml:"deny,omitempty"`
	Members []string `yaml:"members,omitempty"`
}

// MCPConfig configures the OAuth-gated MCP endpoint exposed by a roost.
type MCPConfig struct {
	Enabled         bool                      `yaml:"enabled"`
	DefaultAllowAll bool                      `yaml:"default_allow_all"`
	Roles           map[string]*MCPRoleConfig `yaml:"roles"`
}

// UnmarshalYAML keeps wing.yaml backwards-compatible at the top level while decoding the
// security-sensitive MCP section strictly. A typo in enabled/allow/deny must fail closed.
func (p *MCPConfig) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("mcp must be a mapping")
	}
	if err := rejectUnknownYAMLKeys(value, map[string]bool{
		"enabled": true, "default_allow_all": true, "roles": true,
	}, "mcp"); err != nil {
		return err
	}
	for i := 0; i+1 < len(value.Content); i += 2 {
		if value.Content[i].Value != "roles" {
			continue
		}
		roles := value.Content[i+1]
		if roles.Kind != yaml.MappingNode {
			return fmt.Errorf("mcp.roles must be a mapping")
		}
		for j := 0; j+1 < len(roles.Content); j += 2 {
			name, role := roles.Content[j].Value, roles.Content[j+1]
			if role.Kind != yaml.MappingNode {
				return fmt.Errorf("mcp role %q must be a mapping", name)
			}
			if err := rejectUnknownYAMLKeys(role, map[string]bool{
				"enabled": true, "allow": true, "deny": true, "members": true,
			}, "mcp role "+name); err != nil {
				return err
			}
		}
	}
	type plainMCPConfig MCPConfig
	var decoded plainMCPConfig
	if err := value.Decode(&decoded); err != nil {
		return err
	}
	*p = MCPConfig(decoded)
	return nil
}

func rejectUnknownYAMLKeys(node *yaml.Node, allowed map[string]bool, context string) error {
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if !allowed[key] {
			return fmt.Errorf("%s contains unknown field %q", context, key)
		}
	}
	return nil
}

// Validate rejects ambiguous policy shapes and normalizes member emails.
func (p *MCPConfig) Validate() error {
	if p.Roles == nil {
		p.Roles = map[string]*MCPRoleConfig{}
	}
	for name, role := range p.Roles {
		if name == "" || strings.TrimSpace(name) != name {
			return fmt.Errorf("invalid role name %q", name)
		}
		if role == nil {
			return fmt.Errorf("role %q has no policy", name)
		}
		if len(role.Allow) > 0 && len(role.Deny) > 0 {
			return fmt.Errorf("role %q cannot set both allow and deny", name)
		}
		var err error
		if role.Allow, err = normalizedMCPEntries(role.Allow, "allow", name); err != nil {
			return err
		}
		if role.Deny, err = normalizedMCPEntries(role.Deny, "deny", name); err != nil {
			return err
		}
		seenMembers := map[string]bool{}
		members := make([]string, 0, len(role.Members))
		for _, member := range role.Members {
			member = strings.ToLower(strings.TrimSpace(member))
			if member == "" {
				return fmt.Errorf("role %q contains an empty member", name)
			}
			if !seenMembers[member] {
				seenMembers[member] = true
				members = append(members, member)
			}
		}
		role.Members = members
	}
	return nil
}

func normalizedMCPEntries(values []string, field, role string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("role %q contains an empty %s entry", role, field)
		}
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out, nil
}

// EnabledAny reports whether at least one of the caller's roles enables MCP.
func (p *MCPConfig) EnabledAny(roles []string) bool {
	for _, name := range roles {
		if role := p.Roles[name]; role != nil && role.Enabled {
			return true
		}
	}
	return false
}

// EnabledRoles removes memberships that do not participate in the MCP surface.
func (p *MCPConfig) EnabledRoles(roles []string) []string {
	enabled := make([]string, 0, len(roles))
	for _, name := range roles {
		if role := p.Roles[name]; role != nil && role.Enabled {
			enabled = append(enabled, name)
		}
	}
	return enabled
}

// AllowedAny implements maximum-subset semantics: a tool is visible when any one of the
// caller's MCP-enabled roles allows it. Disabled roles contribute neither grants nor denies.
func (p *MCPConfig) AllowedAny(roles []string, tool string) bool {
	for _, name := range roles {
		role := p.Roles[name]
		if role == nil || !role.Enabled {
			continue
		}
		if len(role.Allow) > 0 && containsMCP(role.Allow, tool) {
			return true
		}
		if len(role.Deny) > 0 && !containsMCP(role.Deny, tool) {
			return true
		}
		if len(role.Allow) == 0 && len(role.Deny) == 0 && p.DefaultAllowAll {
			return true
		}
	}
	return false
}

// RolesForEmail returns every configured role an email belongs to in name order.
func (p *MCPConfig) RolesForEmail(email string) []string {
	email = strings.ToLower(strings.TrimSpace(email))
	names := make([]string, 0, len(p.Roles))
	for name := range p.Roles {
		names = append(names, name)
	}
	sort.Strings(names)
	var matches []string
	for _, name := range names {
		role := p.Roles[name]
		if role != nil && containsMCP(role.Members, email) {
			matches = append(matches, name)
		}
	}
	return matches
}

func containsMCP(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
