package wing

import (
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
)

// EntryOptions carries values supplied by the CLI entry point.
type EntryOptions struct {
	Version           string
	LocalOnly         bool
	SetSessionService func(*wingsession.Service)
	// RelayState observes the optional adapter without controlling wing lifetime.
	RelayState func(string, error)
	// SetPolicySource shares the synchronized runtime policy with an embedded roost.
	SetPolicySource func(func() (*config.WingConfig, *egg.EggConfig))
}
