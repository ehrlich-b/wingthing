package config

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var validToolName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)
var validToolParamName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*$`)
var validToolEnvName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

var validToolParamTypes = map[string]bool{
	"string":  true,
	"integer": true,
	"number":  true,
	"boolean": true,
	"object":  true,
	"array":   true,
}

func validateToolName(name string) error {
	if len(name) > 64 {
		return fmt.Errorf("tool name %q exceeds 64 characters", name)
	}
	if !validToolName.MatchString(name) {
		return fmt.Errorf("tool name %q contains invalid characters (must match [a-zA-Z][a-zA-Z0-9_-]*)", name)
	}
	return nil
}

// ToolParam optionally describes one positional tool argument as a named MCP parameter. Params
// remain ordered: the MCP adapter maps named arguments back to argv in this order so the existing
// tool runner and egg socket stay backwards-compatible.
type ToolParam struct {
	Name        string   `yaml:"name" json:"name"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	Type        string   `yaml:"type,omitempty" json:"type,omitempty"`
	Required    bool     `yaml:"required,omitempty" json:"required,omitempty"`
	Enum        []string `yaml:"enum,omitempty" json:"enum,omitempty"`
	Examples    []any    `yaml:"examples,omitempty" json:"examples,omitempty"`
}

// ToolConfig defines a privileged tool that the wing daemon can execute on behalf of sandboxed agents.
type ToolConfig struct {
	Name          string            `yaml:"name"`
	Description   string            `yaml:"description,omitempty"`
	Params        []ToolParam       `yaml:"params,omitempty"`
	Run           string            `yaml:"run"`
	Env           map[string]string `yaml:"env,omitempty"`
	Timeout       string            `yaml:"timeout,omitempty"`
	MaxConcurrent int               `yaml:"max_concurrent,omitempty"`
}

func validateToolParams(params []ToolParam) error {
	seen := make(map[string]bool, len(params))
	for i := range params {
		param := &params[i]
		if !validToolParamName.MatchString(param.Name) || len(param.Name) > 64 {
			return fmt.Errorf("invalid parameter name %q (must match [a-zA-Z][a-zA-Z0-9_]* and be at most 64 characters)", param.Name)
		}
		if seen[param.Name] {
			return fmt.Errorf("duplicate parameter name %q", param.Name)
		}
		seen[param.Name] = true
		if param.Type == "" {
			param.Type = "string"
		}
		if !validToolParamTypes[param.Type] {
			return fmt.Errorf("parameter %q has unsupported type %q", param.Name, param.Type)
		}
		if len(param.Enum) > 0 && param.Type != "string" {
			return fmt.Errorf("parameter %q: enum is only supported for string parameters", param.Name)
		}
		enumSeen := make(map[string]bool, len(param.Enum))
		for _, value := range param.Enum {
			if value == "" {
				return fmt.Errorf("parameter %q contains an empty enum value", param.Name)
			}
			if enumSeen[value] {
				return fmt.Errorf("parameter %q contains duplicate enum value %q", param.Name, value)
			}
			enumSeen[value] = true
		}
	}
	return nil
}

// TimeoutDuration parses the Timeout field as a time.Duration.
// Returns 0 if empty or unparseable.
func (t *ToolConfig) TimeoutDuration() time.Duration {
	if t.Timeout == "" {
		return 0
	}
	d, err := time.ParseDuration(t.Timeout)
	if err != nil {
		return 0
	}
	return d
}

// LoadToolsDir reads all .yaml files from dir and returns parsed tool configs.
// Returns nil (no error) if dir doesn't exist. Warns on world-readable files.
func LoadToolsDir(dir string) ([]*ToolConfig, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read tools dir: %w", err)
	}
	var tools []*ToolConfig
	seen := make(map[string]string) // name -> filename
	for _, e := range entries {
		if e.IsDir() || (!strings.HasSuffix(e.Name(), ".yaml") && !strings.HasSuffix(e.Name(), ".yml")) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		info, err := e.Info()
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", path, err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			fmt.Fprintf(os.Stderr, "warning: tool config %s is world-readable (mode %o), should be 0600\n", path, info.Mode().Perm())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		var tc ToolConfig
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		if err := dec.Decode(&tc); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		var extra any
		if err := dec.Decode(&extra); err != io.EOF {
			if err == nil {
				return nil, fmt.Errorf("parse %s: multiple YAML documents", path)
			}
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		if tc.Name == "" {
			return nil, fmt.Errorf("tool config %s: missing name", path)
		}
		if err := validateToolName(tc.Name); err != nil {
			return nil, fmt.Errorf("tool config %s: %w", path, err)
		}
		if prev, ok := seen[tc.Name]; ok {
			return nil, fmt.Errorf("duplicate tool name %q in %s and %s", tc.Name, prev, e.Name())
		}
		seen[tc.Name] = e.Name()
		if tc.Run == "" {
			return nil, fmt.Errorf("tool config %s: missing run", path)
		}
		for name := range tc.Env {
			if !validToolEnvName.MatchString(name) {
				return nil, fmt.Errorf("tool config %s: invalid environment variable name %q", path, name)
			}
		}
		if err := validateToolParams(tc.Params); err != nil {
			return nil, fmt.Errorf("tool config %s: %w", path, err)
		}
		tools = append(tools, &tc)
	}
	return tools, nil
}

// ResolveToolsDir returns the configured privileged-tools directory, applying the same
// default and home expansion for both the wing and its remote MCP surface.
func ResolveToolsDir(configDir, configured string) string {
	if configured == "" {
		return filepath.Join(configDir, "tools")
	}
	if configured == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	if strings.HasPrefix(configured, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, configured[2:])
		}
	}
	return configured
}

// ToolNames returns just the tool names from a slice of configs.
func ToolNames(tools []*ToolConfig) []string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name
	}
	return names
}
