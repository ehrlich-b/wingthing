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

// EggConfig holds the sandbox and environment configuration for egg sessions.
type EggConfig struct {
	Isolation                  string       `yaml:"isolation"`
	Mounts                     []string     `yaml:"mounts"`    // "~/repos:rw" or "~/repos" (default rw)
	Deny                       []string     `yaml:"deny"`      // paths to mask
	Resources                  EggResources `yaml:"resources"`
	Env                        EggEnv       `yaml:"env"`
	Shell                      string       `yaml:"shell"`
	DangerouslySkipPermissions bool         `yaml:"dangerously_skip_permissions"`
}

// EggResources configures resource limits for sandboxed processes.
type EggResources struct {
	CPU    string `yaml:"cpu"`     // duration: "300s"
	Memory string `yaml:"memory"`  // size: "2GB"
	MaxFDs uint32 `yaml:"max_fds"`
}

// EggEnv configures environment variable handling.
type EggEnv struct {
	AllowAll bool     `yaml:"allow_all"`
	Allow    []string `yaml:"allow"` // explicit allowlist
}

// DefaultEggConfig returns the permissive default config used when no egg.yaml exists.
func DefaultEggConfig() *EggConfig {
	return &EggConfig{
		Isolation: "network",
		Mounts:    []string{"~:rw"},
		Env:       EggEnv{AllowAll: true},
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
	if cfg.Isolation == "" {
		cfg.Isolation = "network"
	}
	return &cfg, nil
}

// LoadEggConfigFromYAML parses an egg config from a YAML string.
func LoadEggConfigFromYAML(yamlStr string) (*EggConfig, error) {
	var cfg EggConfig
	if err := yaml.Unmarshal([]byte(yamlStr), &cfg); err != nil {
		return nil, fmt.Errorf("parse egg config: %w", err)
	}
	if cfg.Isolation == "" {
		cfg.Isolation = "network"
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

// ToSandboxConfig converts the egg config to a sandbox.Config.
func (c *EggConfig) ToSandboxConfig() sandbox.Config {
	home, _ := os.UserHomeDir()

	var mounts []sandbox.Mount
	for _, m := range c.Mounts {
		source, readOnly := parseMount(m, home)
		mounts = append(mounts, sandbox.Mount{
			Source:   source,
			Target:   source, // same path inside sandbox
			ReadOnly: readOnly,
		})
	}

	var deny []string
	for _, d := range c.Deny {
		deny = append(deny, expandTilde(d, home))
	}

	return sandbox.Config{
		Isolation: sandbox.ParseLevel(c.Isolation),
		Mounts:    mounts,
		Deny:      deny,
		CPULimit:  c.Resources.CPUDuration(),
		MemLimit:  c.Resources.MemBytes(),
		MaxFDs:    c.Resources.MaxFDs,
	}
}

// BuildEnv filters the host environment based on the config.
func (c *EggConfig) BuildEnv() []string {
	if c.Env.AllowAll {
		return os.Environ()
	}
	allowed := make(map[string]bool)
	for _, k := range c.Env.Allow {
		allowed[k] = true
	}
	// Always pass through essentials
	for _, k := range []string{"HOME", "PATH", "SHELL", "TERM", "USER", "LANG"} {
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

// parseMount parses a mount string like "~/repos:rw" or "~/repos" into source path and readOnly flag.
func parseMount(s string, home string) (string, bool) {
	parts := strings.SplitN(s, ":", 2)
	source := expandTilde(parts[0], home)
	readOnly := false
	if len(parts) == 2 && parts[1] == "ro" {
		readOnly = true
	}
	return source, readOnly
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
