package eggclient

import (
	"errors"
	"fmt"
	"github.com/ehrlich-b/wingthing/internal/protectedfile"
	"os"
	"path/filepath"
	"strings"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

func BrowserEggIdentity(wc *config.WingConfig, start ws.PTYStart, home string, sharedHost bool) EggIdentity {
	sealedBoundary := sharedHost || wc.Org != "" && wingpolicy.IsMemberRole(start.OrgRole)
	return EggIdentity{UserID: start.UserID, Email: start.Email, DisplayName: start.DisplayName,
		OrgWing: wc.Org != "", SharedHost: sealedBoundary, SealedFS: sealedBoundary,
		AllowedPaths: wingpolicy.CanonicalPaths(wingpolicy.PathsForRequest(wc.Paths, start.Email, start.OrgRole, home))}
}

// PrepareBrowserLaunch is shared by fresh PTYs and browser forks. Configured
// roots, rather than writable subdirectories, determine browser launch policy.
func PrepareBrowserLaunch(wc *config.WingConfig, start *ws.PTYStart, home string, sharedHost bool, wingDefault *egg.EggConfig) (*egg.EggConfig, EggIdentity, error) {
	if wc.IsAdmin(start.Email) && wingpolicy.IsMemberRole(start.OrgRole) {
		start.OrgRole = "admin"
	}
	identity := BrowserEggIdentity(wc, *start, home, sharedHost)
	if wingpolicy.IsMemberRole(start.OrgRole) && len(identity.AllowedPaths) == 0 {
		return nil, identity, errors.New("no accessible folders on this machine")
	}
	if identity.UserID != "" && (identity.SharedHost || identity.OrgWing) {
		if wingpolicy.IsMemberRole(start.OrgRole) && !wingpolicy.IsUnderPaths(wingpolicy.CanonicalSessionPath(start.CWD), identity.AllowedPaths) {
			return nil, identity, errors.New("working directory is outside this user's roost paths")
		}
		roots := wingpolicy.CanonicalPaths(wingpolicy.ResolvePathStrings(wc.Paths.Strings(), home))
		cfg, err := LoadRoostEggConfig(start.CWD, roots, wingpolicy.IsMemberRole(start.OrgRole), wingDefault)
		return cfg, identity, err
	}
	if len(identity.AllowedPaths) > 0 && !wingpolicy.IsExactPath(wingpolicy.CanonicalSessionPath(start.CWD), identity.AllowedPaths) {
		start.CWD = identity.AllowedPaths[0]
	}
	if wingpolicy.IsMemberRole(start.OrgRole) && len(wc.Paths) > 0 {
		if _, err := os.Stat(filepath.Join(start.CWD, "egg.yaml")); os.IsNotExist(err) {
			return nil, identity, fmt.Errorf("no egg.yaml in %s — ask the wing owner to add a sandbox config", start.CWD)
		}
	}
	cfg := egg.DiscoverEggConfig(start.CWD, wingDefault)
	return cfg, identity, cfg.ResolutionError()
}

// LoadRoostEggConfig trusts only the deepest administrator-configured root.
// Relative rules remain anchored to that root, including when the CWD is below it.
func LoadRoostEggConfig(cwd string, roots []string, member bool, wingDefault *egg.EggConfig) (*egg.EggConfig, error) {
	cwd = wingpolicy.CanonicalSessionPath(cwd)
	root := ""
	for _, candidate := range wingpolicy.CanonicalPaths(roots) {
		if wingpolicy.IsUnderPaths(cwd, []string{candidate}) && len(candidate) > len(root) {
			root = candidate
		}
	}
	if root == "" {
		if member {
			return nil, errors.New("working directory is outside configured wing paths")
		}
		cfg := egg.RuntimeEggConfig(wingDefault, cwd)
		return cfg, cfg.ResolutionError()
	}
	path := filepath.Join(root, "egg.yaml")
	f, err := protectedfile.OpenPolicyResolved(path)
	if errors.Is(err, os.ErrNotExist) {
		if member {
			return nil, fmt.Errorf("no egg.yaml in %s — ask the wing owner to add a sandbox config", root)
		}
		cfg := egg.RuntimeEggConfig(wingDefault, cwd)
		return cfg, cfg.ResolutionError()
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	resolved, err := f.ResolvedPath()
	if err != nil {
		return nil, err
	}
	if !wingpolicy.IsUnderPaths(resolved, []string{root}) {
		return nil, errors.New("root egg.yaml resolves outside configured wing path")
	}
	cfg, err := egg.ResolveEggConfig(path, root)
	if err != nil {
		return nil, err
	}
	// Recheck the alias and the descriptor identity after the existing loader.
	current, err := protectedfile.OpenPolicyResolved(path)
	if err != nil {
		return nil, err
	}
	defer current.Close()
	if !os.SameFile(f.Info, current.Info) {
		return nil, errors.New("root egg.yaml changed during policy resolution")
	}
	copyCfg := *cfg
	copyCfg.FS = append([]string(nil), cfg.FS...)
	for i, rule := range copyCfg.FS {
		mode, name, ok := strings.Cut(rule, ":")
		if !ok {
			mode, name = "rw", rule
		}
		if !filepath.IsAbs(name) && !strings.HasPrefix(name, "~") {
			copyCfg.FS[i] = mode + ":" + filepath.Join(root, name)
		}
	}
	return &copyCfg, copyCfg.ResolutionError()
}
