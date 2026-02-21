package egg

import (
	"fmt"
	"log"
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

// BaseField handles the `base` key in egg configs. It can be a scalar string
// (backward compat: "none", "strict", etc.) or an object with per-section masks.
type BaseField struct {
	Name    string `yaml:"name,omitempty"`
	FS      string `yaml:"fs,omitempty"`
	Network string `yaml:"network,omitempty"`
	Env     string `yaml:"env,omitempty"`
}

func (b *BaseField) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		b.Name = value.Value
		return nil
	}
	type plain BaseField
	return value.Decode((*plain)(b))
}

func (b BaseField) MarshalYAML() (interface{}, error) {
	if b.FS == "" && b.Network == "" && b.Env == "" {
		if b.Name == "" {
			return nil, nil
		}
		return b.Name, nil
	}
	type plain BaseField
	return plain(b), nil
}

func (b BaseField) IsZero() bool {
	return b.Name == "" && b.FS == "" && b.Network == "" && b.Env == ""
}

func (b BaseField) HasMasks() bool {
	return b.FS != "" || b.Network != "" || b.Env != ""
}

// EggConfig holds the sandbox and environment configuration for egg sessions.
type EggConfig struct {
	Base                       BaseField         `yaml:"base,omitempty"`
	FS                         []string          `yaml:"fs"`
	Network                    NetworkField      `yaml:"network"`
	Env                        EnvField          `yaml:"env"`
	Resources                  EggResources      `yaml:"resources"`
	Shell                      string            `yaml:"shell"`
	DangerouslySkipPermissions bool              `yaml:"dangerously_skip_permissions"`
	Audit                      bool              `yaml:"audit"`
	AgentSettings              map[string]string `yaml:"agent_settings,omitempty"` // agent name -> settings file path
}

// EggResources configures resource limits for sandboxed processes.
type EggResources struct {
	CPU     string `yaml:"cpu"`      // duration: "300s"
	Memory  string `yaml:"memory"`   // size: "2GB"
	MaxFDs  uint32 `yaml:"max_fds"`
	MaxPids uint32 `yaml:"max_pids"` // cgroup pids.max (Linux only)
}

// DefaultDenyPaths returns paths that should be blocked by default in sandboxed sessions.
func DefaultDenyPaths() []string {
	return []string{
		"~/.ssh", "~/.gnupg", "~/.aws", "~/.docker",
		"~/.kube", "~/.netrc", "~/.bash_history", "~/.zsh_history",
	}
}

// DefaultCacheDirs returns OS-standard cache directories that build tools need.
// Go, npm, pip, cargo, etc. all write to these. No secrets live here.
func DefaultCacheDirs() []string {
	return []string{
		"~/.cache/",            // XDG_CACHE_HOME default (Linux) — go-build, pip, etc.
		"~/Library/Caches/",    // macOS app caches — go-build, npm, etc.
		"~/go/pkg/mod/cache/",  // Go module download cache
	}
}

// DefaultEggConfig returns the restrictive default config used when no egg.yaml exists.
// CWD is writable, home is read-only except agent-drilled holes. Sensitive dirs are denied.
// OS-standard cache dirs are writable so build tools (go, npm, cargo, pip) work out of the box.
// egg.yaml itself is deny-write so agents can read but not modify their sandbox config.
func DefaultEggConfig() *EggConfig {
	fs := []string{"ro:/", "rw:./"}
	for _, d := range DefaultCacheDirs() {
		fs = append(fs, "rw:"+d)
	}
	for _, d := range DefaultDenyPaths() {
		fs = append(fs, "deny:"+d)
	}
	fs = append(fs, "deny-write:./egg.yaml")
	return &EggConfig{
		FS:  fs,
		Env: EnvField{"HOME", "PATH", "TERM", "LANG", "USER"},
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
// wing default, then to built-in defaults. Project configs are resolved through
// the base chain (additive inheritance) before being returned.
func DiscoverEggConfig(cwd string, wingDefault *EggConfig) *EggConfig {
	if cwd != "" {
		path := filepath.Join(cwd, "egg.yaml")
		cfg, err := ResolveEggConfig(path)
		if err == nil {
			return cfg
		}
		log.Printf("egg: config discovery failed for %s: %v", path, err)
	}
	if wingDefault != nil {
		return wingDefault
	}
	return DefaultEggConfig()
}

const maxBaseDepth = 10

// ResolveEggConfig loads an egg.yaml and resolves its base chain, returning
// a fully merged config. If base is empty, merges on top of DefaultEggConfig.
// If base is "none", returns the config as-is (empty slate).
func ResolveEggConfig(path string) (*EggConfig, error) {
	return resolveEggConfig(path, make(map[string]bool), 0)
}

func resolveEggConfig(path string, visited map[string]bool, depth int) (*EggConfig, error) {
	if depth > maxBaseDepth {
		return nil, fmt.Errorf("egg config base chain too deep (max %d)", maxBaseDepth)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if visited[abs] {
		return nil, fmt.Errorf("egg config circular base reference: %s", abs)
	}
	visited[abs] = true

	child, err := LoadEggConfig(abs)
	if err != nil {
		return nil, err
	}

	var parent *EggConfig
	switch child.Base.Name {
	case "none":
		if child.Base.HasMasks() {
			return nil, fmt.Errorf("base masks invalid with base: none (nothing to mask)")
		}
		return child, nil
	case "":
		parent = DefaultEggConfig()
	default:
		parentPath := resolveBasePath(child.Base.Name, filepath.Dir(abs))
		var err error
		parent, err = resolveEggConfig(parentPath, visited, depth+1)
		if err != nil {
			return nil, fmt.Errorf("resolve base %q: %w", child.Base.Name, err)
		}
	}

	if child.Base.HasMasks() {
		if err := applySectionMasks(parent, child.Base, filepath.Dir(abs), visited, depth); err != nil {
			return nil, err
		}
	}

	return MergeEggConfig(parent, child), nil
}

// resolveBasePath turns a base value into an absolute path.
// - Relative path (starts with . or /) -> resolve relative to configDir
// - Named base -> ~/.wingthing/bases/<name>.yaml
func resolveBasePath(base, configDir string) string {
	if filepath.IsAbs(base) {
		return base
	}
	if strings.HasPrefix(base, "./") || strings.HasPrefix(base, "../") {
		return filepath.Join(configDir, base)
	}
	// Named base: ~/.wingthing/bases/<name>.yaml
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".wingthing", "bases", base+".yaml")
}

// applySectionMasks replaces individual sections of the parent config based on
// per-section mask values. "none" clears the section; a name/path resolves
// that file's full chain and extracts the section.
func applySectionMasks(parent *EggConfig, masks BaseField, configDir string,
	visited map[string]bool, depth int) error {
	if masks.FS != "" {
		if masks.FS == "none" {
			parent.FS = nil
		} else {
			refPath := resolveBasePath(masks.FS, configDir)
			ref, err := resolveEggConfig(refPath, visited, depth+1)
			if err != nil {
				return fmt.Errorf("resolve base.fs %q: %w", masks.FS, err)
			}
			parent.FS = ref.FS
		}
	}
	if masks.Network != "" {
		if masks.Network == "none" {
			parent.Network = nil
		} else {
			refPath := resolveBasePath(masks.Network, configDir)
			ref, err := resolveEggConfig(refPath, visited, depth+1)
			if err != nil {
				return fmt.Errorf("resolve base.network %q: %w", masks.Network, err)
			}
			parent.Network = ref.Network
		}
	}
	if masks.Env != "" {
		if masks.Env == "none" {
			parent.Env = nil
		} else {
			refPath := resolveBasePath(masks.Env, configDir)
			ref, err := resolveEggConfig(refPath, visited, depth+1)
			if err != nil {
				return fmt.Errorf("resolve base.env %q: %w", masks.Env, err)
			}
			parent.Env = ref.Env
		}
	}
	return nil
}

// MergeEggConfig merges a child config on top of a parent config.
// - fs: append child to parent; child ro/rw overrides parent deny for same path
// - network: union (dedup); "*" in either -> ["*"]
// - env: union (dedup); "*" in either -> ["*"]
// - resources: child wins per-field (non-zero overrides parent)
// - shell: child wins if non-empty
// - dangerously_skip_permissions: OR
func MergeEggConfig(parent, child *EggConfig) *EggConfig {
	merged := &EggConfig{}

	// FS: append child to parent, with deny override logic
	merged.FS = mergeFS(parent.FS, child.FS)

	// Network: union with wildcard short-circuit
	merged.Network = NetworkField(mergeStringSet([]string(parent.Network), []string(child.Network)))

	// Env: union with wildcard short-circuit
	merged.Env = EnvField(mergeStringSet([]string(parent.Env), []string(child.Env)))

	// Resources: child wins per-field
	merged.Resources = mergeResources(parent.Resources, child.Resources)

	// Shell: child wins if non-empty
	merged.Shell = parent.Shell
	if child.Shell != "" {
		merged.Shell = child.Shell
	}

	// DangerouslySkipPermissions: OR
	merged.DangerouslySkipPermissions = parent.DangerouslySkipPermissions || child.DangerouslySkipPermissions

	// Audit: OR (once enabled by org/parent, can't be disabled)
	merged.Audit = parent.Audit || child.Audit

	// AgentSettings: child overrides parent per-key
	if len(parent.AgentSettings) > 0 || len(child.AgentSettings) > 0 {
		merged.AgentSettings = make(map[string]string)
		for k, v := range parent.AgentSettings {
			merged.AgentSettings[k] = v
		}
		for k, v := range child.AgentSettings {
			merged.AgentSettings[k] = v
		}
	}

	return merged
}

// mergeFS appends child fs rules to parent, but if child has ro:P or rw:P,
// drops deny:P from parent (normalized path comparison).
func mergeFS(parent, child []string) []string {
	home, _ := os.UserHomeDir()

	// Collect child access paths (ro/rw) for deny override
	childAccess := make(map[string]bool)
	for _, entry := range child {
		mode, path, ok := strings.Cut(entry, ":")
		if !ok {
			continue
		}
		if mode == "ro" || mode == "rw" {
			childAccess[normalizeFSPath(path, home)] = true
		}
	}

	// Copy parent rules, dropping denies that child overrides
	var result []string
	for _, entry := range parent {
		mode, path, ok := strings.Cut(entry, ":")
		if !ok {
			result = append(result, entry)
			continue
		}
		if mode == "deny" && childAccess[normalizeFSPath(path, home)] {
			continue // child overrides this deny
		}
		result = append(result, entry)
	}

	// Append all child rules
	result = append(result, child...)
	return result
}

// normalizeFSPath expands tilde and cleans the path for comparison.
func normalizeFSPath(path, home string) string {
	expanded := expandTilde(path, home)
	return filepath.Clean(expanded)
}

// mergeStringSet unions two string slices with dedup. "*" in either -> ["*"].
func mergeStringSet(a, b []string) []string {
	for _, s := range a {
		if s == "*" {
			return []string{"*"}
		}
	}
	for _, s := range b {
		if s == "*" {
			return []string{"*"}
		}
	}
	seen := make(map[string]bool, len(a)+len(b))
	var out []string
	for _, s := range a {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, s := range b {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// mergeResources returns a merged EggResources where child non-zero fields win.
func mergeResources(parent, child EggResources) EggResources {
	r := parent
	if child.CPU != "" {
		r.CPU = child.CPU
	}
	if child.Memory != "" {
		r.Memory = child.Memory
	}
	if child.MaxFDs > 0 {
		r.MaxFDs = child.MaxFDs
	}
	if child.MaxPids > 0 {
		r.MaxPids = child.MaxPids
	}
	return r
}

// ParseFSRules splits fs entries into mounts, deny paths, and deny-write paths.
// Entries are "mode:path" where mode is rw, ro, deny, or deny-write.
func ParseFSRules(fs []string, home string) ([]sandbox.Mount, []string, []string) {
	var mounts []sandbox.Mount
	var deny []string
	var denyWrite []string
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
		case "deny-write":
			denyWrite = append(denyWrite, expanded)
		case "ro":
			mounts = append(mounts, sandbox.Mount{Source: expanded, Target: expanded, ReadOnly: true})
		default: // "rw" or unknown
			mounts = append(mounts, sandbox.Mount{Source: expanded, Target: expanded})
		}
	}
	return mounts, deny, denyWrite
}

// ToSandboxConfig converts the egg config to a sandbox.Config.
func (c *EggConfig) ToSandboxConfig() sandbox.Config {
	home, _ := os.UserHomeDir()
	mounts, deny, denyWrite := ParseFSRules(c.FS, home)
	netNeed := sandbox.NetworkNeedFromDomains([]string(c.Network))

	return sandbox.Config{
		Mounts:      mounts,
		Deny:        deny,
		DenyWrite:   denyWrite,
		NetworkNeed: netNeed,
		Domains:     []string(c.Network),
		CPULimit:    c.Resources.CPUDuration(),
		MemLimit:    c.Resources.MemBytes(),
		MaxFDs:      c.Resources.MaxFDs,
		PidLimit:    c.Resources.MaxPids,
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

// sshDirDenied returns true if any FS deny rule covers the user's ~/.ssh directory.
func (c *EggConfig) sshDirDenied() bool {
	home, _ := os.UserHomeDir()
	sshDir := filepath.Join(home, ".ssh")
	for _, entry := range c.FS {
		mode, path, ok := strings.Cut(entry, ":")
		if !ok {
			continue
		}
		if mode != "deny" {
			continue
		}
		expanded := expandTilde(path, home)
		if expanded == sshDir || strings.HasPrefix(sshDir+"/", expanded+"/") {
			return true
		}
	}
	return false
}

// BuildEnv filters the host environment based on the config.
// SSH_AUTH_SOCK is stripped when ~/.ssh is denied — otherwise the agent can
// still make outbound SSH connections via the forwarded socket despite the
// filesystem deny, causing unexpected host-key prompts inside the egg.
func (c *EggConfig) BuildEnv() []string {
	stripSSHAgent := c.sshDirDenied()

	filter := func(env []string) []string {
		if !stripSSHAgent {
			return env
		}
		out := env[:0:0]
		for _, e := range env {
			if !strings.HasPrefix(e, "SSH_AUTH_SOCK=") {
				out = append(out, e)
			}
		}
		return out
	}

	if c.IsAllEnv() {
		return filter(os.Environ())
	}
	allowed := make(map[string]bool)
	for _, k := range c.Env {
		allowed[k] = true
	}
	var env []string
	for _, e := range os.Environ() {
		k, _, ok := strings.Cut(e, "=")
		if ok && allowed[k] {
			env = append(env, e)
		}
	}
	return filter(env)
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
