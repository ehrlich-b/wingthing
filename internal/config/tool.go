package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var validToolName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)

func validateToolName(name string) error {
	if len(name) > 64 {
		return fmt.Errorf("tool name %q exceeds 64 characters", name)
	}
	if !validToolName.MatchString(name) {
		return fmt.Errorf("tool name %q contains invalid characters (must match [a-zA-Z][a-zA-Z0-9_-]*)", name)
	}
	return nil
}

// ToolConfig defines a privileged tool that the wing daemon can execute on behalf of sandboxed agents.
type ToolConfig struct {
	Name          string            `yaml:"name"`
	Description   string            `yaml:"description,omitempty"`
	Run           string            `yaml:"run"`
	Env           map[string]string `yaml:"env,omitempty"`
	Timeout       string            `yaml:"timeout,omitempty"`
	MaxConcurrent int               `yaml:"max_concurrent,omitempty"`
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
		if err := yaml.Unmarshal(data, &tc); err != nil {
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
		tools = append(tools, &tc)
	}
	return tools, nil
}

// ToolNames returns just the tool names from a slice of configs.
func ToolNames(tools []*ToolConfig) []string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name
	}
	return names
}
