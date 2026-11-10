package config

import (
	"fmt"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

// ValidateMachineContext keeps context names safe as terminal headings.
func ValidateMachineContext(name string) error {
	if len(name) > 64 || strings.TrimSpace(name) != name {
		return fmt.Errorf("context must be at most 64 bytes without surrounding whitespace")
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return fmt.Errorf("context must not contain control characters")
		}
	}
	return nil
}

func (c *WingConfig) validateMachineContexts() error {
	if err := ValidateMachineContext(c.MachineContext); err != nil {
		return err
	}
	if c.MachineContext != "" && c.Context != nil {
		return fmt.Errorf("context cannot be both a machine name and a service block")
	}
	for _, name := range c.PhoneFleetExcludeContexts {
		if err := ValidateMachineContext(name); err != nil {
			return err
		}
	}
	return nil
}

// The scalar form groups machines; the preexisting mapping form continues to
// configure service impersonation. Keep their runtime fields independent.
func (c *WingConfig) UnmarshalYAML(node *yaml.Node) error {
	type plain WingConfig
	copy := *node
	copy.Content = append([]*yaml.Node(nil), node.Content...)
	seen := map[string]bool{}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if seen[key] {
			return fmt.Errorf("duplicate wing config key %q", key)
		}
		seen[key] = true
	}
	c.MachineContext = ""
	for i := 0; i+1 < len(copy.Content); i += 2 {
		if copy.Content[i].Value == "context" && copy.Content[i+1].Kind == yaml.ScalarNode && copy.Content[i+1].Tag == "!!str" {
			c.MachineContext = copy.Content[i+1].Value
			c.Context = nil
			copy.Content = append(copy.Content[:i:i], copy.Content[i+2:]...)
			break
		}
	}
	if err := copy.Decode((*plain)(c)); err != nil {
		return err
	}
	return c.validateMachineContexts()
}

func (c WingConfig) MarshalYAML() (any, error) {
	type plain WingConfig
	if err := c.validateMachineContexts(); err != nil {
		return nil, err
	}
	var node yaml.Node
	if err := node.Encode(plain(c)); err != nil {
		return nil, err
	}
	if c.MachineContext != "" {
		node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "context"}, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: c.MachineContext})
	}
	return &node, nil
}
