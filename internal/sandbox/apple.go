//go:build darwin

package sandbox

import (
	"context"
	"fmt"
	"os/exec"
)

type appleSandbox struct {
	cfg Config
}

// newPlatform tries to create an Apple Containers sandbox.
// Returns an error if the container CLI is not available.
func newPlatform(cfg Config) (Sandbox, error) {
	if _, err := exec.LookPath("container"); err != nil {
		return nil, fmt.Errorf("apple containers not available: %w", err)
	}
	// TODO: real Apple Containers implementation
	return nil, fmt.Errorf("apple containers not yet implemented")
}

func (s *appleSandbox) Exec(ctx context.Context, name string, args []string) (*exec.Cmd, error) {
	return nil, fmt.Errorf("apple containers not yet implemented")
}

func (s *appleSandbox) Destroy() error {
	return nil
}
