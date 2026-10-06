package eggclient

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

func BrowserEggIdentity(wc *config.WingConfig, start ws.PTYStart, home string, sharedHost bool) EggIdentity {
	return EggIdentity{UserID: start.UserID, Email: start.Email, DisplayName: start.DisplayName,
		OrgWing: wc.Org != "", SharedHost: sharedHost, SealedFS: sharedHost,
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
