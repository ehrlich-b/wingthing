package eggclient

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
	"github.com/ehrlich-b/wingthing/internal/ws"
	"golang.org/x/sys/unix"
)

func BrowserEggIdentity(wc *config.WingConfig, start ws.PTYStart, home string, sharedHost bool) EggIdentity {
	return EggIdentity{UserID: start.UserID, Email: start.Email, DisplayName: start.DisplayName,
		OrgWing: wc.Org != "", SharedHost: sharedHost, SealedFS: sharedHost,
		AllowedPaths: wingpolicy.CanonicalPaths(wingpolicy.PathsForRequest(wc.Paths, start.Email, start.OrgRole, home))}
}

// PrepareBrowserLaunch is shared by fresh PTYs and browser forks. Configured
// roots, rather than writable subdirectories, determine browser launch policy.
func PrepareBrowserLaunch(wc *config.WingConfig, start *ws.PTYStart, home string, sharedHost bool, wingDefault *egg.EggConfig) (*egg.EggConfig, EggIdentity, error) {
	return prepareSessionLaunch(wc, start, home, sharedHost, wingDefault, true)
}

// PrepareMCPLaunch permits existing subdirectories within the caller's paths,
// while retaining the covering configured root's sandbox policy.
func PrepareMCPLaunch(wc *config.WingConfig, start *ws.PTYStart, home string, sharedHost bool, wingDefault *egg.EggConfig) (*egg.EggConfig, EggIdentity, error) {
	return prepareSessionLaunch(wc, start, home, sharedHost, wingDefault, false)
}

func prepareSessionLaunch(wc *config.WingConfig, start *ws.PTYStart, home string, sharedHost bool, wingDefault *egg.EggConfig, browser bool) (*egg.EggConfig, EggIdentity, error) {
	if wc.IsAdmin(start.Email) && wingpolicy.IsMemberRole(start.OrgRole) {
		start.OrgRole = "admin"
	}
	identity := BrowserEggIdentity(wc, *start, home, sharedHost)
	if wingpolicy.IsMemberRole(start.OrgRole) && len(identity.AllowedPaths) == 0 {
		return nil, identity, errors.New("no accessible folders on this machine")
	}
	if !browser {
		if start.CWD == "" && len(identity.AllowedPaths) > 0 {
			start.CWD = identity.AllowedPaths[0]
		}
		cwd := start.CWD
		if cwd == "" {
			var err error
			cwd, err = os.Getwd()
			if err != nil {
				return nil, identity, fmt.Errorf("resolve working directory: %w", err)
			}
		}
		cwd, err := filepath.Abs(cwd)
		if err != nil {
			return nil, identity, fmt.Errorf("resolve working directory: %w", err)
		}
		// CanonicalSessionPath falls back to the spelling on resolution failure;
		// require successful resolution before using it for admission.
		resolved, err := filepath.EvalSymlinks(cwd)
		if err != nil {
			return nil, identity, fmt.Errorf("working directory %q: %w", cwd, err)
		}
		cwd = wingpolicy.CanonicalSessionPath(resolved)
		info, err := os.Stat(cwd)
		if err != nil {
			return nil, identity, fmt.Errorf("working directory %q: %w", cwd, err)
		}
		if !info.IsDir() {
			return nil, identity, fmt.Errorf("working directory %q is not a directory", cwd)
		}
		start.CWD = cwd
	}
	if identity.UserID != "" && (identity.SharedHost || identity.OrgWing) {
		if start.CWD == "" && len(identity.AllowedPaths) > 0 {
			start.CWD = identity.AllowedPaths[0]
		}
		roots := wingpolicy.ResolvePathStrings(wc.Paths.Strings(), home)
		cfg, err := LoadRoostEggConfig(start.CWD, roots, identity.AllowedPaths, wingpolicy.IsMemberRole(start.OrgRole), wingDefault)
		return cfg, identity, err
	}
	if browser && len(identity.AllowedPaths) > 0 && !wingpolicy.IsExactPath(wingpolicy.CanonicalSessionPath(start.CWD), identity.AllowedPaths) {
		start.CWD = identity.AllowedPaths[0]
	}
	policyCWD := start.CWD
	if !browser && len(identity.AllowedPaths) > 0 {
		if !wingpolicy.IsUnderPaths(start.CWD, identity.AllowedPaths) {
			return nil, identity, errors.New("working directory is outside current launch paths")
		}
		// Explicit nested roots retain their existing independent policy. A
		// writable child that is not a configured root cannot select egg.yaml.
		selected := ""
		for _, root := range identity.AllowedPaths {
			if wingpolicy.IsUnderPaths(start.CWD, []string{root}) && len(root) > len(selected) {
				selected = root
			}
		}
		policyCWD = selected
	}
	if wingpolicy.IsMemberRole(start.OrgRole) && len(wc.Paths) > 0 {
		if _, err := os.Stat(filepath.Join(policyCWD, "egg.yaml")); os.IsNotExist(err) {
			return nil, identity, fmt.Errorf("no egg.yaml in %s — ask the wing owner to add a sandbox config", policyCWD)
		}
	}
	cfg := egg.DiscoverEggConfig(policyCWD, wingDefault)
	if policyCWD != start.CWD {
		// Relative grants and denies must mean the same paths as a launch at
		// the root; rebasing a deny onto the child could weaken that policy.
		copyCfg := *cfg
		copyCfg.FS = append([]string(nil), cfg.FS...)
		for i, rule := range copyCfg.FS {
			mode, path, hasMode := strings.Cut(rule, ":")
			if !hasMode {
				path = rule
			}
			if !filepath.IsAbs(path) && !strings.HasPrefix(path, "~") {
				copyCfg.FS[i] = filepath.Join(policyCWD, path)
				if hasMode {
					copyCfg.FS[i] = mode + ":" + copyCfg.FS[i]
				}
			}
		}
		cfg = &copyCfg
	}
	return cfg, identity, nil
}

// LoadRoostEggConfig discovers only the administrator-configured root's policy,
// never a policy planted in a writable child directory or an ancestor.
func LoadRoostEggConfig(cwd string, roots, allowedRoots []string, member bool, wingDefault *egg.EggConfig) (*egg.EggConfig, error) {
	// Validate configured spellings before canonicalization erases symlinks.
	for _, root := range roots {
		for path := filepath.Clean(root); ; path = filepath.Dir(path) {
			info, err := os.Lstat(path)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("inspect configured roost path %q: %w", root, err)
			}
			if err == nil && info.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("configured roost path %q resolves through symlink %q; configure a real directory", root, path)
			}
			if filepath.Dir(path) == path {
				break
			}
		}
	}
	roots = wingpolicy.CanonicalPaths(roots)
	for i, root := range roots {
		for j, parent := range roots {
			if i != j && root != parent && wingpolicy.SessionPolicyContains(parent, root) {
				return nil, fmt.Errorf("nested roost paths are not supported: %s is inside %s", root, parent)
			}
		}
	}
	allowedRoots = wingpolicy.CanonicalPaths(allowedRoots)
	if cwd == "" && len(allowedRoots) > 0 {
		cwd = allowedRoots[0]
	}
	cwd = wingpolicy.CanonicalSessionPath(cwd)
	root := ""
	for _, candidate := range roots {
		if wingpolicy.SessionPolicyContains(candidate, cwd) {
			root = candidate
			break
		}
	}
	if root == "" {
		if member {
			return nil, fmt.Errorf("working directory %q is outside this user's roost paths", cwd)
		}
		return protectRoostRootPolicies(egg.RuntimeEggConfig(wingDefault), roots), nil
	}
	// Check the selected root's ACL before reading its policy.
	if member && !wingpolicy.IsExactPath(root, allowedRoots) {
		return nil, fmt.Errorf("configured roost path %q is not accessible to this user", root)
	}
	path := filepath.Join(root, "egg.yaml")
	cfg, err := loadRoostRolePolicy(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if member {
				return nil, fmt.Errorf("no egg.yaml in %s — ask the wing owner to add a sandbox config", root)
			}
			return protectRoostRootPolicies(egg.RuntimeEggConfig(wingDefault), roots), nil
		}
		return nil, err
	}
	for i, rule := range cfg.FS {
		mode, path, hasMode := strings.Cut(rule, ":")
		if !hasMode {
			path = rule
		}
		if !filepath.IsAbs(path) && !strings.HasPrefix(path, "~") {
			cfg.FS[i] = filepath.Join(root, path)
			if hasMode {
				cfg.FS[i] = mode + ":" + cfg.FS[i]
			}
		}
	}
	return protectRoostRootPolicies(cfg, roots), nil
}

func loadRoostRolePolicy(path string) (*egg.EggConfig, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("stat roost egg config %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("roost egg config %q must be a regular file (symlinks are not allowed)", path)
	}
	// O_NOFOLLOW closes a symlink swap between Lstat and open; O_NONBLOCK
	// avoids hanging if a regular file is replaced with a FIFO before open.
	f, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open roost egg config %q: %w", path, err)
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil {
		return nil, fmt.Errorf("stat open roost egg config %q: %w", path, err)
	} else if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("roost egg config %q must be a regular file", path)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("read roost egg config %q: %w", path, err)
	}
	cfg, err := egg.LoadEggConfigFromYAML(string(data))
	if err != nil {
		return nil, fmt.Errorf("roost egg config %q: %w", path, err)
	}
	// Named bases normally resolve to files in the state directory, which
	// may itself be under a writable root. Only the built-in default is safe.
	for _, field := range []struct{ name, value string }{
		{"base", cfg.Base.Name}, {"base.fs", cfg.Base.FS},
		{"base.network", cfg.Base.Network}, {"base.env", cfg.Base.Env},
	} {
		if field.value != "" && field.value != "none" && field.value != "default" {
			return nil, fmt.Errorf("roost egg config %q: %s %q references a file; only none or built-in default is allowed", path, field.name, field.value)
		}
	}
	if cfg.Base.Name == "none" {
		if cfg.Base.HasMasks() {
			return nil, fmt.Errorf("roost egg config %q: base masks invalid with base: none (nothing to mask)", path)
		}
		return cfg, nil
	}
	parent := egg.DefaultEggConfig()
	if cfg.Base.FS == "none" {
		parent.FS = nil
	}
	if cfg.Base.Network == "none" {
		parent.Network = egg.NetworkField{}
	}
	if cfg.Base.Env == "none" {
		parent.Env = nil
	}
	return egg.MergeEggConfig(parent, cfg), nil
}

func protectRoostRootPolicies(cfg *egg.EggConfig, roots []string) *egg.EggConfig {
	// Copy the FS slice so fallbacks cannot mutate the captured runtime policy.
	copyCfg := *cfg
	copyCfg.FS = append([]string(nil), cfg.FS...)
	copyCfg.RoostPolicyPaths = nil
	for _, root := range roots {
		path := filepath.Join(root, "egg.yaml")
		copyCfg.RoostPolicyPaths = append(copyCfg.RoostPolicyPaths, path)
		rule := "deny-write:" + path
		if !ContainsExactPath(copyCfg.FS, rule) {
			copyCfg.FS = append(copyCfg.FS, rule)
		}
	}
	for _, root := range roots {
		// A file deny cannot stop a writable parent from replacing the whole
		// policy directory. Pin every root and ancestor regardless of FS rules;
		// each backend decides which entries the sandbox can reach.
		for dir := filepath.Clean(root); dir != "/"; dir = filepath.Dir(dir) {
			rule := "deny-rename:" + dir
			if !ContainsExactPath(copyCfg.FS, rule) {
				copyCfg.FS = append(copyCfg.FS, rule)
			}
		}
	}
	return &copyCfg
}
