//go:build linux

package sandbox

import (
	"context"
	"fmt"
	"os/exec"
)

type linuxSandbox struct {
	cfg Config
}

// newPlatform tries to create a namespace+seccomp sandbox.
// Returns an error if capabilities are insufficient.
func newPlatform(cfg Config) (Sandbox, error) {
	// TODO: detect namespace/seccomp capabilities and implement
	return nil, fmt.Errorf("linux namespace sandbox not yet implemented")
}

func (s *linuxSandbox) Exec(ctx context.Context, name string, args []string) (*exec.Cmd, error) {
	return nil, fmt.Errorf("linux namespace sandbox not yet implemented")
}

func (s *linuxSandbox) Destroy() error {
	return nil
}
