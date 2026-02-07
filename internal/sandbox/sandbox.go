package sandbox

import (
	"context"
	"os/exec"
	"time"
)

// Sandbox provides isolated execution of commands.
type Sandbox interface {
	Exec(ctx context.Context, name string, args []string) (*exec.Cmd, error)
	Destroy() error
}

// Mount describes a filesystem mount for the sandbox.
type Mount struct {
	Source   string
	Target   string
	ReadOnly bool
}

// Config holds sandbox creation parameters.
type Config struct {
	Isolation Level
	Mounts    []Mount
	Timeout   time.Duration
}

// New creates a platform-appropriate sandbox. It tries platform-specific
// backends first (Apple Containers on macOS, namespaces on Linux) and
// falls back to process-level isolation.
func New(cfg Config) (Sandbox, error) {
	if s, err := newPlatform(cfg); err == nil {
		return s, nil
	}
	return newFallback(cfg)
}
