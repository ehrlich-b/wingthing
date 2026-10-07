package wing

import "github.com/ehrlich-b/wingthing/internal/config"

// EntryOptions carries values supplied by the CLI entry point.
type EntryOptions struct {
	Version string
	// SetPolicySource shares the guarded runtime policy with an embedded roost.
	SetPolicySource func(func() *config.WingConfig)
}
