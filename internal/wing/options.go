package wing

import (
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
)

// EntryOptions carries values supplied by the CLI entry point.
type EntryOptions struct {
	Version string
	// SetPolicySource shares the guarded runtime policy with an embedded roost.
	SetPolicySource func(func() (*config.WingConfig, *egg.EggConfig))
}
