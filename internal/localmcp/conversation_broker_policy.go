package localmcp

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
)

// providerWriteRegion is one place a sandboxed provider may write.
type providerWriteRegion struct {
	Path   string `json:"path"`
	Prefix bool   `json:"prefix,omitempty"` // Seatbelt regex "^path": also matches path-prefixed siblings
	Exact  bool   `json:"exact,omitempty"`  // a single-file mount
	Reason string `json:"reason"`
}

// providerWriteModel reproduces the write rules of the existing macOS Seatbelt
// profile (internal/sandbox/apple.go) for the mounts the egg runtime assembles
// (internal/egg/server.go): allow-default, then deny writes under the egg
// process HOME, then reopen writable mounts, agent profile directories,
// keychains, TMPDIR and /private/tmp. Deny and deny-write rules are
// deliberately ignored. They only remove write access, so ignoring them may
// refuse a safe layout but can never approve an exposed one.
//
// This is a host-side model of the policy that will be rendered, used before
// any provider runs. The native fixture separately observes the real denial.
type providerWriteModel struct {
	DenyRoot string                `json:"deny_root"`
	Regions  []providerWriteRegion `json:"regions"`
}

func modelProviderWrites(cfg *config.Config, eggCfg *egg.EggConfig, agentName, cwd, sessionID string, identity eggclient.EggIdentity) (providerWriteModel, error) {
	var model providerWriteModel
	if runtime.GOOS != "darwin" {
		return model, fmt.Errorf("provider write protection is modeled only for the macOS Seatbelt profile, not %s", runtime.GOOS)
	}
	if eggCfg == nil || !egg.RequiresSandbox(eggCfg, agentName) {
		return model, errors.New("an outer-boundary session has the full authority of the OS user")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return model, errors.New("egg HOME is unavailable, so no write-deny root exists")
	}
	model.DenyRoot = wingpolicy.CanonicalPolicyPath(home)
	if model.DenyRoot == string(filepath.Separator) {
		return model, errors.New("egg HOME is the filesystem root")
	}
	// Same per-user data home selection as spawnEgg/egg runtime (rc.UserHome).
	dataHome := home
	if config.Channel() == "preview" || identity.UserID != "" && (identity.OrgWing || identity.SharedHost) {
		dataHome = eggclient.EffectiveSessionHome(cfg, identity)
	}
	resolved := make([]string, 0, len(eggCfg.FS))
	for _, entry := range eggCfg.FS {
		mode, path, ok := strings.Cut(entry, ":")
		if !ok {
			mode, path = "rw", entry
		}
		if path == "." || path == "./" {
			path = cwd
		} else if !filepath.IsAbs(path) && !strings.HasPrefix(path, "~") {
			path = filepath.Join(cwd, path)
		}
		resolved = append(resolved, mode+":"+path)
	}
	mounts, _, _ := egg.ParseFSRules(resolved, dataHome)
	add := func(path, reason string, prefix, exact bool) {
		model.Regions = append(model.Regions, providerWriteRegion{Path: wingpolicy.CanonicalPolicyPath(path), Prefix: prefix, Exact: exact, Reason: reason})
	}
	for _, mount := range mounts {
		if !mount.ReadOnly {
			add(mount.Source, "writable egg filesystem mount", false, false)
		}
	}
	// Broker-managed eggs omit the browser bridge, so its request file mount
	// is not part of their policy.
	profile := egg.Profile(agentName)
	for _, dir := range profile.WriteRegex {
		add(filepath.Join(dataHome, dir), "agent profile configuration prefix", true, false)
	}
	for _, dir := range profile.WriteDirs {
		add(filepath.Join(dataHome, dir), "agent profile write directory", false, false)
	}
	add(filepath.Join(model.DenyRoot, "Library", "Keychains"), "keychain directory", false, false)
	add(os.TempDir(), "TMPDIR", false, false)
	add("/private/tmp", "system temporary directory", false, false)
	return model, nil
}

// writable reports whether the modeled provider can create, change, rename or
// remove path, and why.
func (m providerWriteModel) writable(path string) (string, bool) {
	path = wingpolicy.CanonicalPolicyPath(path)
	for _, region := range m.Regions {
		switch {
		case region.Exact && path == region.Path,
			region.Prefix && strings.HasPrefix(path, region.Path),
			!region.Exact && !region.Prefix && wingpolicy.SessionPolicyContains(region.Path, path):
			return region.Reason + " " + region.Path, true
		}
	}
	if !wingpolicy.SessionPolicyContains(m.DenyRoot, path) {
		return "outside the sandbox HOME write-deny root " + m.DenyRoot, true
	}
	return "", false
}

// verifyProtected refuses a layout in which the provider could alter
// authoritative state, its directory, or a directory it could rename.
func (m providerWriteModel) verifyProtected(stateDir string, targets []string) error {
	state := wingpolicy.CanonicalPolicyPath(stateDir)
	for _, target := range append([]string{state}, targets...) {
		if reason, exposed := m.writable(target); exposed {
			return fmt.Errorf("%s is provider-writable (%s)", target, reason)
		}
	}
	// No provider write region may lie inside the state tree: the provider data
	// home is required to be outside it and the browser bridge is omitted.
	for _, region := range m.Regions {
		inside := wingpolicy.SessionPolicyContains(state, region.Path)
		if region.Prefix {
			inside = inside || strings.HasPrefix(state, region.Path)
		}
		if !inside {
			continue
		}
		return fmt.Errorf("provider-writable %s %s is inside protected state %s", region.Reason, region.Path, state)
	}
	return verifyAncestorsNotRenamable(m.DenyRoot)
}

// Seatbelt allows writes outside the HOME deny root. Any ancestor above it that
// the OS user can write would let the provider rename the protected tree and
// substitute another, so require ordinary POSIX protection there.
func verifyAncestorsNotRenamable(denyRoot string) error {
	uid := os.Getuid()
	if uid == 0 {
		return errors.New("a root-owned egg process can rename any ancestor")
	}
	groups, _ := os.Getgroups()
	child := denyRoot
	for {
		parent := filepath.Dir(child)
		if parent == child {
			return nil
		}
		parentInfo, err := os.Stat(parent)
		if err != nil {
			return err
		}
		childInfo, err := os.Lstat(child)
		if err != nil {
			return err
		}
		if renamableByUser(parentInfo, childInfo, uid, groups) {
			return fmt.Errorf("ancestor %s of the sandbox HOME is writable by this OS user, so the provider could rename %s", parent, child)
		}
		child = parent
	}
}

func renamableByUser(parent, child os.FileInfo, uid int, groups []int) bool {
	ps, ok := parent.Sys().(*syscall.Stat_t)
	if !ok {
		return true
	}
	mode := parent.Mode()
	writable := mode&0o002 != 0 || int(ps.Uid) == uid && mode&0o200 != 0
	if !writable && mode&0o020 != 0 {
		for _, gid := range groups {
			if gid == int(ps.Gid) {
				writable = true
			}
		}
	}
	if !writable {
		return false
	}
	if mode&os.ModeSticky == 0 {
		return true
	}
	cs, ok := child.Sys().(*syscall.Stat_t)
	return !ok || int(cs.Uid) == uid || int(ps.Uid) == uid
}
