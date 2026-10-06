package eggclient

import (
	"fmt"
	"path/filepath"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
)

// protectContextSecret adds host-owned deny rules after caller policy resolution.
func protectContextSecret(policy *egg.EggConfig, c *config.ContextConfig) (*egg.EggConfig, []string, error) {
	if c == nil {
		return policy, nil, nil
	}
	if err := c.Validate(); err != nil {
		return nil, nil, err
	}
	resolved, err := filepath.EvalSymlinks(c.SecretFile)
	if err != nil {
		return nil, nil, fmt.Errorf("context: cannot resolve secret_file")
	}
	paths := []string{filepath.Clean(c.SecretFile)}
	if resolved != paths[0] {
		paths = append(paths, resolved)
	}
	clone := *policy
	clone.FS = append([]string(nil), policy.FS...)
	for _, path := range paths {
		clone.FS = append(clone.FS, "deny:"+path)
	}
	return &clone, paths, nil
}
