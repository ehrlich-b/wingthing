package egg

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	wingconfig "github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"
)

// NetworkField handles YAML unmarshaling of network: string | []string | mapping.
// The scalar and list forms are a frozen contract: "none"/"" → no network,
// "*" → unrestricted, list → domain allowlist. The mapping form is additive and
// carries the loopback forwarding, enforcement mode, and legacy egress-logging
// option described in docs/sandbox-enhancement-design.md. Log is retained as a
// frozen config contract; policy decisions are audited independently of it.
type NetworkField struct {
	Domains      []string `yaml:"domains,omitempty"`
	LocalPorts   []int    `yaml:"local_ports,omitempty"`
	Mode         string   `yaml:"mode,omitempty"`          // "" (default) | "observe" | "enforce"
	Log          *bool    `yaml:"log,omitempty"`           // legacy compatibility; policy-decision auditing is always on
	AgentDomains string   `yaml:"agent_domains,omitempty"` // ""/"merge" (default) | "none"
}

func (n *NetworkField) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		s := value.Value
		if s == "none" || s == "" {
			*n = NetworkField{}
			return nil
		}
		*n = NetworkField{Domains: []string{s}}
		return nil
	case yaml.MappingNode:
		allowed := map[string]bool{
			"domains": true, "local_ports": true, "mode": true, "log": true, "agent_domains": true,
		}
		for index := 0; index+1 < len(value.Content); index += 2 {
			key := value.Content[index].Value
			if !allowed[key] {
				return fmt.Errorf("network contains unknown field %q", key)
			}
		}
		type plain NetworkField
		var p plain
		if err := value.Decode(&p); err != nil {
			return err
		}
		if err := validateAgentDomainsMode(p.AgentDomains); err != nil {
			return err
		}
		if p.Mode != "" && p.Mode != "enforce" && p.Mode != "observe" {
			return fmt.Errorf("network.mode must be enforce or observe, got %q", p.Mode)
		}
		seenPorts := make(map[int]bool, len(p.LocalPorts))
		var normalizedPorts []int
		for _, port := range p.LocalPorts {
			if port < 1 || port > 65535 {
				return fmt.Errorf("network.local_ports contains invalid port %d", port)
			}
			if !seenPorts[port] {
				seenPorts[port] = true
				normalizedPorts = append(normalizedPorts, port)
			}
		}
		p.LocalPorts = normalizedPorts
		*n = NetworkField(p)
		return nil
	default:
		var list []string
		if err := value.Decode(&list); err != nil {
			return err
		}
		*n = NetworkField{Domains: list}
		return nil
	}
}

// MarshalYAML re-emits the legacy scalar and list shapes whenever no mapping-only
// option is set, so rendered configs stay byte-stable for existing users.
func (n NetworkField) MarshalYAML() (interface{}, error) {
	if len(n.LocalPorts) == 0 && n.Mode == "" && n.Log == nil && n.AgentDomains == "" {
		if len(n.Domains) == 0 {
			return "none", nil
		}
		if len(n.Domains) == 1 && n.Domains[0] == "*" {
			return "*", nil
		}
		return n.Domains, nil
	}
	type plain NetworkField
	return plain(n), nil
}

// IsZero lets yaml omitempty treat an unset network block as absent.
func (n NetworkField) IsZero() bool {
	return len(n.Domains) == 0 && len(n.LocalPorts) == 0 && n.Mode == "" && n.Log == nil && n.AgentDomains == ""
}

func validateAgentDomainsMode(mode string) error {
	switch mode {
	case "", "merge", "none":
		return nil
	default:
		return fmt.Errorf("network.agent_domains must be merge or none, got %q", mode)
	}
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
	resolutionError            error
	Base                       BaseField         `yaml:"base,omitempty"`
	FS                         []string          `yaml:"fs"`
	Network                    NetworkField      `yaml:"network"`
	Env                        EnvField          `yaml:"env,omitempty"`
	Resources                  EggResources      `yaml:"resources"`
	Shell                      string            `yaml:"shell"`
	DangerouslySkipPermissions bool              `yaml:"dangerously_skip_permissions"`
	Audit                      bool              `yaml:"audit"`
	Trace                      bool              `yaml:"trace"`
	AgentSettings              map[string]string `yaml:"agent_settings,omitempty"` // agent name -> settings file path
}

// EggResources configures resource limits for sandboxed processes.
type EggResources struct {
	CPU     string `yaml:"cpu"`    // duration: "300s"
	Memory  string `yaml:"memory"` // size: "2GB"
	MaxFDs  uint32 `yaml:"max_fds"`
	MaxPids uint32 `yaml:"max_pids"` // cgroup pids.max (Linux only)
}

// DefaultDenyPaths returns paths that should be blocked by default in sandboxed sessions.
func DefaultDenyPaths() []string {
	paths, _ := defaultDenyPaths()
	return paths
}

func defaultDenyPaths() ([]string, error) {
	paths := []string{
		"~/.ssh", "~/.gnupg", "~/.aws", "~/.docker",
		"~/.kube", "~/.netrc", "~/.bash_history", "~/.zsh_history",
	}
	control, err := eggControlDenyPaths("")
	return append(paths, control...), err
}

// Include both release channels and an explicitly selected state directory.
// An egg never needs a controller token, even its own egg.token.
func eggControlDenyPaths(sessionDir string, grants ...[]sandbox.Mount) ([]string, error) {
	home, _ := os.UserHomeDir()
	states := []string{filepath.Join(home, ".wingthing"), filepath.Join(home, ".wingthing-preview")}
	if state, err := wingconfig.StateDir(); err == nil {
		states = append(states, state)
	}
	if filepath.Base(filepath.Dir(sessionDir)) == "eggs" {
		states = append(states, filepath.Dir(filepath.Dir(sessionDir)))
	}
	seen := make(map[string]bool)
	var paths []string
	var loaders []string
	paths = append(paths, wingconfig.CanonicalProviderPath(filepath.Join(home, ".gnupg", "wingthing-control")))
	for _, state := range states {
		loaderState := state
		state = wingconfig.CanonicalProviderPath(state)
		// Validate the lexical path too, before canonicalization hides links
		// that could redirect the host's next configuration load.
		loaders = append(loaders, filepath.Join(loaderState, "wing.yaml"), filepath.Join(loaderState, "egg.yaml"))
		if seen[state] {
			continue
		}
		seen[state] = true
		for _, name := range []string{"eggs", "tools", "device_token.yaml", "local_device_token.yaml", "wing_key", "sync.key", "wing.yaml", "egg.yaml", "config.yaml", "roost.db", "wt.db"} {
			paths = append(paths, filepath.Join(state, name))
		}
		toolsDir := filepath.Join(loaderState, "tools")
		if cfg, err := wingconfig.LoadWingConfig(state); err == nil {
			toolsDir = wingconfig.ResolveToolsDir(loaderState, cfg.ToolsDir)
		}
		loaders = append(loaders, toolsDir)
		// YAML definitions can themselves be aliases to files outside the
		// configured directory. Protect the files actually loaded by the host.
		if entries, err := os.ReadDir(toolsDir); err == nil {
			for _, entry := range entries {
				if !entry.IsDir() && (strings.HasSuffix(entry.Name(), ".yaml") || strings.HasSuffix(entry.Name(), ".yml")) {
					loader := filepath.Join(toolsDir, entry.Name())
					loaders = append(loaders, loader)
				}
			}
		}
	}
	for _, loader := range loaders {
		resolved, err := resolveLoaderPath(loader, true, grants...)
		if err != nil {
			return paths, fmt.Errorf("protect tool loader %s: %w", loader, err)
		}
		paths = append(paths, loader, resolved)
	}
	return paths, nil
}

// Linux deny masks cannot have holes: they are applied after writable mounts.
// Compile the existing mounts into a jail allowlist with the control trees
// omitted, then add only this session's bridge mounts. Splitting ancestor
// mounts also keeps future sibling eggs out; enumerating current eggs alone
// would leave a race with the next session launch.
func isolateLinuxEggControl(mounts []sandbox.Mount, control []string, bridges []sandbox.Mount, deny ...[]string) ([]sandbox.Mount, error) {
	var denied []string
	for _, paths := range deny {
		for _, path := range sandbox.CanonicalDenyPaths(paths) {
			if path != "/" { // jail marker, not a deny inside the allowlist
				denied = append(denied, path)
			}
		}
	}
	var trees []string
	for _, path := range control {
		trees = append(trees, wingconfig.CanonicalProviderPath(path))
	}
	var err error
	trees, err = physicalControlAliases(trees)
	if err != nil {
		return nil, err
	}
	var result []sandbox.Mount
	splitting := make(map[string]bool)
	var splittingIdentity []os.FileInfo
	var split func(sandbox.Mount) error
	split = func(m sandbox.Mount) error {
		path := wingconfig.CanonicalProviderPath(m.Source)
		ancestor := false
		for _, tree := range trees {
			if controlPathWithin(path, tree) {
				return nil
			}
			ancestor = ancestor || controlPathWithin(tree, path)
		}
		if !ancestor {
			// The jail recreates fixed merged-usr links itself. Mount their
			// destinations directly instead of trying to overmount the link.
			source := filepath.Clean(m.Source)
			if (source == "/bin" || source == "/sbin" || source == "/lib" || source == "/lib64") && path == "/usr"+source {
				m.Source, m.Target = path, path
			}
			if info, err := os.Lstat(m.Source); err == nil && info.Mode()&os.ModeSymlink != 0 {
				for _, root := range denied {
					if controlPathWithin(path, root) {
						return nil // never reopen a masked target through an alias
					}
				}
				// Safe HOME aliases remain readable, but never inherit a
				// writable grant. Bind the resolved source into a synthetic
				// target rather than following a symlink in the jail walker.
				if _, err := os.Stat(path); os.IsNotExist(err) {
					return nil // dangling aliases were unreadable on the host too
				}
				m.Target, m.Source, m.ReadOnly = m.Source, path, true
			}
			result = append(result, m)
			return nil
		}
		if splitting[path] {
			return nil // an alias back to an ancestor must not reopen the tree
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		for _, active := range splittingIdentity {
			if os.SameFile(info, active) {
				return nil
			}
		}
		splittingIdentity = append(splittingIdentity, info)
		defer func() { splittingIdentity = splittingIdentity[:len(splittingIdentity)-1] }()
		splitting[path] = true
		defer delete(splitting, path)
		// Split the resolved ancestor, so an alias cannot leave symlink
		// components in the child sources handed to the confined jail builder.
		entries, err := os.ReadDir(path)
		if err != nil {
			return fmt.Errorf("split control-tree ancestor %s: %w", m.Source, err)
		}
		for _, entry := range entries {
			child := filepath.Join(path, entry.Name())
			if filepath.Clean(m.Source) == "/" {
				// The jail supplies private proc, dev and tmp. It recreates
				// merged-usr aliases before installing the other mounts.
				mergedUSR := child == "/bin" || child == "/sbin" || child == "/lib" || child == "/lib64"
				if child == "/proc" || child == "/dev" || child == "/tmp" || (mergedUSR && entry.Type()&os.ModeSymlink != 0) {
					continue
				}
			}
			if err := split(sandbox.Mount{Source: child, Target: child, ReadOnly: m.ReadOnly}); err != nil {
				return err
			}
		}
		return nil
	}
	for _, m := range mounts {
		if err := split(m); err != nil {
			return nil, err
		}
	}
	return append(result, bridges...), nil
}

func controlPathWithin(path, root string) bool {
	return path == root || strings.HasPrefix(path, strings.TrimSuffix(root, "/")+"/")
}

func linuxEggReadMounts(mounts []sandbox.Mount, deny []string) []sandbox.Mount {
	for _, path := range deny {
		if path == "/" {
			return mounts // an explicit jail already has an allowlist
		}
	}
	for _, mount := range mounts {
		if filepath.Clean(mount.Source) == "/" {
			return mounts
		}
	}
	// Outside explicit jail mode, the Linux backend implicitly allows reads
	// everywhere. Preserve those reads when compiling control-tree isolation.
	return append([]sandbox.Mount{{Source: "/", Target: "/", ReadOnly: true}}, mounts...)
}

// DefaultCacheDirs returns OS-standard cache directories that build tools need.
// Go, npm, pip, cargo, etc. all write to these. No secrets live here.
func DefaultCacheDirs() []string {
	return []string{
		"~/.cache/",           // XDG_CACHE_HOME default (Linux) — go-build, pip, etc.
		"~/Library/Caches/",   // macOS app caches — go-build, npm, etc.
		"~/go/pkg/mod/cache/", // Go module download cache
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
	deny, err := defaultDenyPaths()
	for _, d := range deny {
		fs = append(fs, "deny:"+d)
	}
	fs = append(fs, "deny-write:./egg.yaml")
	return &EggConfig{
		resolutionError: err,
		FS:              fs,
		Env:             EnvField{"HOME", "PATH", "TERM", "LANG", "USER"},
	}
}

// UnsandboxedEggConfig returns the explicit trusted-host policy used when an
// outer VM or container is the security boundary. It passes the full
// environment and network through and declares no filesystem or resource
// restrictions, which causes the egg runtime to keep PTY persistence while
// skipping the OS sandbox entirely.
func UnsandboxedEggConfig() *EggConfig {
	return &EggConfig{
		Base:    BaseField{Name: "none"},
		Network: NetworkField{Domains: []string{"*"}, AgentDomains: "none"},
		Env:     EnvField{"*"},
	}
}

// RequiresSandbox reports whether cfg asks the runtime to enforce any policy.
// Trusted-host compatibility is intentionally recognized only when every
// relevant boundary is wide open. In particular, a wildcard network policy by
// itself must not silently disable filesystem, syscall, and resource isolation.
// Keep this predicate aligned with the explicit OuterBoundary marker passed to
// the egg subprocess by spawnEgg.
func RequiresSandbox(cfg *EggConfig, agentName string) bool {
	if cfg == nil {
		return true
	}
	if len(cfg.FS) > 0 || cfg.Trace || cfg.Resources.CPU != "" ||
		cfg.Resources.Memory != "" || cfg.Resources.MaxFDs > 0 || cfg.Resources.MaxPids > 0 {
		return true
	}
	if !cfg.IsAllEnv() || len(cfg.Network.LocalPorts) > 0 || cfg.Network.Mode != "" || cfg.Network.Log != nil {
		return true
	}
	domains := append([]string(nil), cfg.Network.Domains...)
	if agentName != "" && cfg.Network.AgentDomains != "none" {
		domains = mergeStringSet(domains, Profile(agentName).Domains)
	}
	return len(domains) != 1 || domains[0] != "*" || sandbox.NetworkNeedFromDomains(domains) != sandbox.NetworkFull
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
		if _, statErr := os.Stat(path); statErr == nil {
			cfg, err := ResolveEggConfig(path)
			if err == nil {
				return cfg
			}
			log.Printf("egg: config discovery failed for %s: %v", path, err)
			if isUnsafePolicyPath(err) {
				return &EggConfig{resolutionError: err}
			}
		}
	}
	if wingDefault != nil {
		return wingDefault
	}
	if dir, err := wingconfig.StateDir(); err == nil {
		path := filepath.Join(dir, "egg.yaml")
		if _, statErr := os.Stat(path); os.IsNotExist(statErr) && wingconfig.Channel() == "stable" {
			if home, err := os.UserHomeDir(); err == nil {
				path = filepath.Join(home, ".wingthing", "egg.yaml")
			}
		}
		if _, statErr := os.Stat(path); statErr == nil {
			cfg, resolveErr := ResolveEggConfig(path)
			if resolveErr == nil {
				return cfg
			}
			log.Printf("egg: global config discovery failed for %s: %v", path, resolveErr)
			if isUnsafePolicyPath(resolveErr) {
				return &EggConfig{resolutionError: resolveErr}
			}
		}
	}
	return DefaultEggConfig()
}

const maxBaseDepth = 10

// ResolveEggConfig loads an egg.yaml and resolves its base chain, returning
// a fully merged config. If base is empty, merges on top of DefaultEggConfig.
// If base is "none", returns the config as-is (empty slate).
func ResolveEggConfig(path string) (*EggConfig, error) {
	dependencies := make(map[string]bool)
	cfg, err := resolveEggConfig(path, dependencies, 0)
	if err != nil {
		return nil, err
	}
	// Add these after all inheritance and section masks, so clearing the FS
	// section cannot discard protection for a policy that was already read.
	// Explicit trusted-host policies have no OS sandbox to enforce it.
	if RequiresSandbox(cfg, "") {
		home, _ := os.UserHomeDir()
		mounts, _, _ := ParseFSRules(cfg.FS, home)
		protected := make(map[string]bool)
		ancestors := make(map[string]bool)
		workspace := wingconfig.CanonicalProviderPath(filepath.Dir(path))
		for dependency := range dependencies {
			real, err := resolveLoaderPath(dependency, false, mounts)
			if err != nil {
				return nil, fmt.Errorf("resolve policy dependency: %w", err)
			}
			protected[real] = true
			// System aliases are immutable; only user-managed aliases need a
			// lexical rule to recheck after later agent grants are installed.
			if _, err := resolveLoaderPath(dependency, false, []sandbox.Mount{{Source: "/"}}); isUnsafePolicyPath(err) {
				protected[dependency] = true
			}
			// Pin every directory entry that could relocate a loaded policy.
			// External bases need their chain protected to the filesystem root.
			for dir := filepath.Dir(real); dir != "/"; dir = filepath.Dir(dir) {
				ancestors[dir] = true
				if dir == workspace {
					break
				}
			}
		}
		paths := make([]string, 0, len(protected))
		for dependency := range protected {
			paths = append(paths, dependency)
		}
		sort.Strings(paths)
		for _, dependency := range paths {
			cfg.FS = append(cfg.FS, "deny-write:"+dependency)
		}
		paths = paths[:0]
		for dir := range ancestors {
			paths = append(paths, dir)
		}
		sort.Strings(paths)
		for _, dir := range paths {
			cfg.FS = append(cfg.FS, "deny-rename:"+dir)
		}
	}
	return cfg, nil
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
	// Read the chain before checking aliases against the final inherited FS.
	if _, err := resolveLoaderPath(abs, false, nil); err != nil {
		return nil, err
	}

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
	if err := parent.ResolutionError(); err != nil {
		return nil, err
	}

	if child.Base.HasMasks() {
		if err := applySectionMasks(parent, child.Base, filepath.Dir(abs), visited, depth); err != nil {
			return nil, err
		}
	}

	return MergeEggConfig(parent, child), nil
}

// PolicyPathError refuses aliases whose entry or ancestors the sandboxed agent
// could replace. Host-user write permissions alone do not make an alias unsafe.
type PolicyPathError struct{ Path string }

func (e *PolicyPathError) Error() string {
	return fmt.Sprintf("policy path traverses a replaceable symlink: %s", e.Path)
}

func isUnsafePolicyPath(err error) bool {
	var pathErr *PolicyPathError
	return errors.As(err, &pathErr)
}

// Walk the actual loader path, including links in link targets. System aliases
// such as macOS /var remain usable when their ancestor entries are immutable to
// this UID. A read-only child directory inside a writable tree is not enough.
func validatePolicyPath(path string) error {
	_, err := resolveLoaderPath(path, false)
	return err
}

// Return the actual loader destination so immutable aliases are sealed at their
// target too, including paths containing .. after a system symlink.
func resolveLoaderPath(path string, allowMissing bool, grants ...[]sandbox.Mount) (string, error) {
	var mounts []sandbox.Mount
	if len(grants) > 0 {
		mounts = grants[0]
	} else {
		home, _ := os.UserHomeDir()
		fs := []string{"rw:./"}
		for _, cache := range DefaultCacheDirs() {
			fs = append(fs, "rw:"+cache)
		}
		mounts, _, _ = ParseFSRules(fs, home)
	}
	checkAncestors := func(dir, alias string) error {
		for parent := dir; ; parent = filepath.Dir(parent) {
			if loaderDirectoryWritable(parent, mounts) && unix.Access(parent, unix.W_OK|unix.X_OK) == nil {
				return &PolicyPathError{Path: alias}
			}
			if parent == "/" {
				return nil
			}
		}
	}
	if !filepath.IsAbs(path) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		// Preserve .. components until their preceding symlinks are inspected.
		path = cwd + "/" + path
	}
	pending := strings.Split(strings.TrimPrefix(path, "/"), "/")
	current := "/"
	links := 0
	var mutableAlias string
	for len(pending) > 0 {
		part := pending[0]
		pending = pending[1:]
		if part == "" || part == "." {
			continue
		}
		next := filepath.Join(current, part)
		info, err := os.Lstat(next)
		if err != nil {
			if allowMissing && os.IsNotExist(err) {
				if mutableAlias != "" {
					if err := checkAncestors(current, mutableAlias); err != nil {
						return "", err
					}
				}
				// An absent loader is sealed before creation.
				return filepath.Join(append([]string{next}, pending...)...), nil
			}
			return "", fmt.Errorf("inspect policy path: %w", err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			current = next
			continue
		}
		if err := checkAncestors(current, next); err != nil {
			return "", err
		}
		// Immutable system aliases need no target-chain check. User-managed
		// links also require their destination's ancestors to remain unwritable.
		if unix.Access(current, unix.W_OK|unix.X_OK) == nil {
			mutableAlias = next
		}
		links++
		if links > 255 {
			return "", fmt.Errorf("policy path has too many symlinks: %s", path)
		}
		target, err := os.Readlink(next)
		if err != nil {
			return "", err
		}
		if filepath.IsAbs(target) {
			current = "/"
		}
		pending = append(strings.Split(target, "/"), pending...)
	}
	if mutableAlias != "" {
		if err := checkAncestors(filepath.Dir(current), mutableAlias); err != nil {
			return "", err
		}
	}
	return current, nil
}

func loaderDirectoryWritable(path string, mounts []sandbox.Mount) bool {
	for _, mount := range mounts {
		if mount.ReadOnly {
			continue
		}
		root := wingconfig.CanonicalProviderPath(mount.Source)
		if controlPathWithin(path, root) || (mount.UseRegex && strings.HasPrefix(path, root)) {
			return true
		}
	}
	return false
}

// ResolutionError preserves a security refusal through legacy discovery APIs.
// Callers must check it before rendering or executing the returned config.
func (c *EggConfig) ResolutionError() error {
	if c == nil {
		return nil
	}
	return c.resolutionError
}

// resolveBasePath turns a base value into an absolute path.
// - Relative path (starts with . or /) -> resolve relative to configDir
// - Named base -> state directory's bases/<name>.yaml, with a stable legacy fallback
func resolveBasePath(base, configDir string) string {
	if filepath.IsAbs(base) {
		return base
	}
	if strings.HasPrefix(base, "./") || strings.HasPrefix(base, "../") {
		return filepath.Join(configDir, base)
	}
	dir, err := wingconfig.StateDir()
	if err != nil {
		return ""
	}
	path := filepath.Join(dir, "bases", base+".yaml")
	if _, err := os.Stat(path); os.IsNotExist(err) && wingconfig.Channel() == "stable" {
		legacy := filepath.Join(wingconfig.DefaultDir(), "bases", base+".yaml")
		if info, err := os.Stat(legacy); err == nil && !info.IsDir() {
			return legacy
		}
	}
	return path
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
			parent.Network = NetworkField{}
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

	// Network: union domains with wildcard short-circuit; mapping-only options
	// follow child-wins, matching how Resources merge.
	merged.Network = NetworkField{
		Domains:      mergeStringSet(parent.Network.Domains, child.Network.Domains),
		LocalPorts:   mergeIntSet(parent.Network.LocalPorts, child.Network.LocalPorts),
		Mode:         firstNonEmpty(child.Network.Mode, parent.Network.Mode),
		Log:          firstNonNilBool(child.Network.Log, parent.Network.Log),
		AgentDomains: firstNonEmpty(child.Network.AgentDomains, parent.Network.AgentDomains),
	}

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

	// Trace: OR
	merged.Trace = parent.Trace || child.Trace

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
func mergeIntSet(a, b []int) []int {
	seen := make(map[int]bool, len(a)+len(b))
	var out []int
	for _, group := range [][]int{a, b} {
		for _, v := range group {
			if !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func firstNonNilBool(values ...*bool) *bool {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

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
		case "deny-rename":
			// Consumed separately by the runtime; never a writable grant.
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

// denyRenamePaths preserves directory contents' existing permissions while
// preventing replacement of the directory entry itself.
func denyRenamePaths(fs []string, home string) []string {
	var paths []string
	for _, entry := range fs {
		if path, ok := strings.CutPrefix(entry, "deny-rename:"); ok {
			paths = append(paths, expandTilde(path, home))
		}
	}
	return paths
}

// ToSandboxConfig converts the egg config to a sandbox.Config.
// If home is non-empty it is used to expand ~ in FS rules; otherwise os.UserHomeDir().
func (c *EggConfig) ToSandboxConfig(home string) sandbox.Config {
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	mounts, deny, denyWrite := ParseFSRules(c.FS, home)
	deny = append(deny, c.SSHAgentSocketDenyPaths(home, false)...)
	netNeed := sandbox.NetworkNeedFromDomains(c.Network.Domains)

	return sandbox.Config{
		Mounts:      mounts,
		Deny:        deny,
		DenyWrite:   denyWrite,
		DenyRename:  denyRenamePaths(c.FS, home),
		NetworkNeed: netNeed,
		NetworkMode: c.Network.Mode,
		Domains:     c.Network.Domains,
		LocalPorts:  append([]int(nil), c.Network.LocalPorts...),
		CPULimit:    c.Resources.CPUDuration(),
		MemLimit:    c.Resources.MemBytes(),
		MaxFDs:      c.Resources.MaxFDs,
		PidLimit:    c.Resources.MaxPids,
		Trace:       c.Trace,
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
// If home is non-empty it is used to expand ~; otherwise os.UserHomeDir().
func (c *EggConfig) sshDirDenied(home string) bool {
	if home == "" {
		home, _ = os.UserHomeDir()
	}
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

func (c *EggConfig) explicitlyAllowsEnv(name string) bool {
	for _, entry := range c.Env {
		if entry == name {
			return true
		}
	}
	return false
}

// SSHAgentSocketDenyPaths returns the live SSH agent endpoint that must be
// masked alongside ~/.ssh. Removing SSH_AUTH_SOCK from the child environment
// is not sufficient: an agent can discover common socket paths under /tmp or
// the user runtime directory and connect to them directly.
//
// Listing SSH_AUTH_SOCK explicitly (rather than via env: ["*"]) is a deliberate
// opt-in to agent-backed SSH without exposing raw key files. Shared-host callers
// pass force=true because they never forward host authentication agents.
func (c *EggConfig) SSHAgentSocketDenyPaths(home string, force bool) []string {
	if !c.sshDirDenied(home) || (!force && c.explicitlyAllowsEnv("SSH_AUTH_SOCK")) {
		return nil
	}
	path := strings.TrimSpace(os.Getenv("SSH_AUTH_SOCK"))
	if path == "" || !filepath.IsAbs(path) {
		return nil
	}
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return nil
	}
	return []string{path}
}

// BuildEnv filters the host environment based on the config.
// SSH_AUTH_SOCK is stripped when ~/.ssh is denied — otherwise the agent can
// still make outbound SSH connections via the forwarded socket despite the
// filesystem deny, causing unexpected host-key prompts inside the egg.
// If home is non-empty it is used to expand ~ in FS rules; otherwise os.UserHomeDir().
func (c *EggConfig) BuildEnv(home string) []string {
	stripSSHAgent := c.sshDirDenied(home) && !c.explicitlyAllowsEnv("SSH_AUTH_SOCK")

	filter := func(env []string) []string {
		out := env[:0:0]
		for _, e := range env {
			if stripSSHAgent && strings.HasPrefix(e, "SSH_AUTH_SOCK=") {
				continue
			}
			// Never pass parent agent session vars into sandboxed agents.
			// CLAUDECODE causes Claude Code to refuse to start ("nested session").
			// CLAUDE_CODE_* are internal session state that shouldn't leak.
			k, _, _ := strings.Cut(e, "=")
			if k == "CLAUDECODE" || strings.HasPrefix(k, "CLAUDE_CODE_") {
				continue
			}
			out = append(out, e)
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
// If home is non-empty it is used to expand ~ in FS rules; otherwise os.UserHomeDir().
func (c *EggConfig) BuildEnvMap(home string) map[string]string {
	envSlice := c.BuildEnv(home)
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
	if err := c.ResolutionError(); err != nil {
		return "", err
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// NetworkSummary returns a short description of the network config for logging.
func (c *EggConfig) NetworkSummary() string {
	if len(c.Network.Domains) == 0 {
		return "none"
	}
	for _, d := range c.Network.Domains {
		if d == "*" {
			return "*"
		}
	}
	return strings.Join(c.Network.Domains, ",")
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
