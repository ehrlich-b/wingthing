package localmcp

import (
	"fmt"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
)

// PhoneFleetContextRefusal means a saved machine context is outside phone
// delegation. SSH CLI access retains its independent authority.
type PhoneFleetContextRefusal struct{ Remote, Context string }

func (e *PhoneFleetContextRefusal) Error() string {
	return fmt.Sprintf("phone fleet delegation refused for remote %q in context %q", e.Remote, e.Context)
}

func checkPhoneFleetContext(wc *config.WingConfig, name string, remote config.Remote) error {
	for _, excluded := range wc.PhoneFleetExcludeContexts {
		if remote.Context == excluded {
			return &PhoneFleetContextRefusal{Remote: name, Context: remote.Context}
		}
	}
	return nil
}

// Check every matching route, so an alternate alias cannot bypass an exclusion.
// A fanout is refused as a whole before any inventory or metadata crosses it.
func checkPhoneFleetContexts(dir string, wc *config.WingConfig, origin string, all bool) error {
	remotes, err := config.LoadRemotes(dir)
	if err != nil {
		return err
	}
	for _, name := range eggclient.SortedRemoteNames(remotes) {
		remote := remotes[name]
		if all || remote.WingID == origin {
			if err := checkPhoneFleetContext(wc, name, remote); err != nil {
				return err
			}
		}
	}
	return nil
}
