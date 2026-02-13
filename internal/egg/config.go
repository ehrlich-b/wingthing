package egg

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ehrlich-b/wingthing/internal/sandbox"
	"gopkg.in/yaml.v3"
)

// NetworkField handles YAML unmarshaling of network: string | []string.
// "none" → nil, "*" → ["*"], list → as-is.
type NetworkField []string

func (n *NetworkField) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		s := value.Value
		if s == "none" || s == "" {
			*n = nil
			return nil
		}
		*n = NetworkField{s}
		return nil
	}
	var list []string
	if err := value.Decode(&list); err != nil {
		return err
	}
	*n = NetworkField(list)
	return nil
}

// EnvField handles YAML unmarshaling of env: string | []string.
// "*" → ["*"], list → as-is.
type EnvField []string

func (e *EnvField) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		s := value.Value
		if s == "*" {
			*e = EnvField{"*"}
			return nil
		}
		if s == "" {
			*e = nil
			return nil
		}
		*e = EnvField{s}
		return nil
	}
	var list []string
	if err := value.Decode(&list); err != nil {
		return err
	}
	*e = EnvField(list)
	return nil
}

// EggConfig holds the sandbox and environment configuration for egg sessions.
type EggConfig struct {
	FS                         []string     `yaml:"fs"`
	Network                    NetworkField `yaml:"network"`
	Env                        EnvField     `yaml:"env"`
	Resources                  EggResources `yaml:"resources"`
	Shell                      string       `yaml:"shell"`
	DangerouslySkipPermissions bool         `yaml:"dangerously_skip_permissions"`
}

// EggResources configures resource limits for sandboxed processes.
type EggResources struct {
	CPU    string `yaml:"cpu"`     // duration: "300s"
	Memory string `yaml:"memory"`  // size: "2GB"
	MaxFDs uint32 `yaml:"max_fds"`
}

// DefaultDenyPaths returns paths that should be blocked by default in sandboxed sessions.
func DefaultDenyPaths() []string {
	return []string{
		"~/.ssh", "~/.gnupg", "~/.aws", "~/.docker",
		"~/.kube", "~/.netrc", "~/.bash_history", "~/.zsh_history",
	}
}

// DefaultEggConfig returns the restrictive default config used when no egg.yaml exists.
// CWD is writable, home is read-only except agent-drilled holes. Sensitive dirs are denied.
func DefaultEggConfig() *EggConfig {
	fs := []string{"rw:./"}
	for _, d := range DefaultDenyPaths() {
		fs = append(fs, "deny:"+d)
	}
	return &EggConfig{
		FS: fs,
	}
}

// LoadEggConfig reads and parses an egg.yaml file.
func LoadEggConfig(path string) (*EggConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read egg config: %w", err)
	}
	var cfg EggConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse egg config: %w", err)
	}
	return &cfg, nil
}

// LoadEggConfigFromYAML parses an egg config from a YAML string.
func LoadEggConfigFromYAML(yamlStr string) (*EggConfig, error) {
	var cfg EggConfig
	if err := yaml.Unmarshal([]byte(yamlStr), &cfg); err != nil {
		return nil, fmt.Errorf("parse egg config: %w", err)
	}
	return &cfg, nil
}

// DiscoverEggConfig looks for egg.yaml in the given directory, falls back to the
// wing default, then to built-in defaults.
func DiscoverEggConfig(cwd string, wingDefault *EggConfig) *EggConfig {
	if cwd != "" {
		path := filepath.Join(cwd, "egg.yaml")
		if cfg, err := LoadEggConfig(path); err == nil {
			return cfg
		}
	}
	if wingDefault != nil {
		return wingDefault
	}
	return DefaultEggConfig()
}

// ParseFSRules splits fs entries into mounts and deny paths.
// Entries are "mode:path" where mode is rw, ro, or deny.
func ParseFSRules(fs []string, home string) ([]sandbox.Mount, []string) {
	var mounts []sandbox.Mount
	var deny []string
	for _, entry := range fs {
		mode, path, ok := strings.Cut(entry, ":")
		if !ok {
			// No colon — treat as rw mount
			path = entry
			mode = "rw"
		}
		expanded := expandTilde(path, home)
		switch mode {
		case "deny":
			deny = append(deny, expanded)
		case "ro":
			mounts = append(mounts, sandbox.Mount{Source: expanded, Target: expanded, ReadOnly: true})
		default: // "rw" or unknown
			mounts = append(mounts, sandbox.Mount{Source: expanded, Target: expanded})
		}
	}
	return mounts, deny
}

// ToSandboxConfig converts the egg config to a sandbox.Config.
func (c *EggConfig) ToSandboxConfig() sandbox.Config {
	home, _ := os.UserHomeDir()
	mounts, deny := ParseFSRules(c.FS, home)
	netNeed := sandbox.NetworkNeedFromDomains([]string(c.Network))

	return sandbox.Config{
		Mounts:      mounts,
		Deny:        deny,
		NetworkNeed: netNeed,
		Domains:     []string(c.Network),
		CPULimit:    c.Resources.CPUDuration(),
		MemLimit:    c.Resources.MemBytes(),
		MaxFDs:      c.Resources.MaxFDs,
	}
}

// IsAllEnv returns true if the env config passes all environment variables.
func (c *EggConfig) IsAllEnv() bool {
	for _, v := range c.Env {
		if v == "*" {
			return true
		}
	}
	return false
}

// BuildEnv filters the host environment based on the config.
func (c *EggConfig) BuildEnv() []string {
	if c.IsAllEnv() {
		return os.Environ()
	}
	allowed := make(map[string]bool)
	for _, k := range c.Env {
		allowed[k] = true
	}
	// Always pass through essentials (minimal set — agents set their own vars at runtime)
	// USER is required for macOS Keychain lookups (e.g. Claude Code OAuth tokens).
	for _, k := range []string{"HOME", "PATH", "TERM", "LANG", "USER"} {
		allowed[k] = true
	}
	var env []string
	for _, e := range os.Environ() {
		k, _, ok := strings.Cut(e, "=")
		if ok && allowed[k] {
			env = append(env, e)
		}
	}
	return env
}

// BuildEnvMap returns the environment as a map for proto SpawnRequest.
func (c *EggConfig) BuildEnvMap() map[string]string {
	envSlice := c.BuildEnv()
	m := make(map[string]string, len(envSlice))
	for _, e := range envSlice {
		k, v, ok := strings.Cut(e, "=")
		if ok {
			m[k] = v
		}
	}
	return m
}

// YAML returns the config serialized as YAML.
func (c *EggConfig) YAML() (string, error) {
	data, err := yaml.Marshal(c)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// NetworkSummary returns a short description of the network config for logging.
func (c *EggConfig) NetworkSummary() string {
	if len(c.Network) == 0 {
		return "none"
	}
	for _, d := range c.Network {
		if d == "*" {
			return "*"
		}
	}
	return strings.Join([]string(c.Network), ",")
}

func expandTilde(path string, home string) string {
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	if path == "~" {
		return home
	}
	return path
}

// CPUDuration parses the CPU field as a duration.
func (r *EggResources) CPUDuration() time.Duration {
	if r.CPU == "" {
		return 0
	}
	d, err := time.ParseDuration(r.CPU)
	if err != nil {
		return 0
	}
	return d
}

// MemBytes parses the Memory field as bytes (supports GB, MB suffixes).
func (r *EggResources) MemBytes() uint64 {
	if r.Memory == "" {
		return 0
	}
	s := strings.TrimSpace(r.Memory)
	s = strings.ToUpper(s)

	multiplier := uint64(1)
	if strings.HasSuffix(s, "GB") {
		multiplier = 1024 * 1024 * 1024
		s = strings.TrimSuffix(s, "GB")
	} else if strings.HasSuffix(s, "MB") {
		multiplier = 1024 * 1024
		s = strings.TrimSuffix(s, "MB")
	} else if strings.HasSuffix(s, "KB") {
		multiplier = 1024
		s = strings.TrimSuffix(s, "KB")
	}

	n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return n * multiplier
}
