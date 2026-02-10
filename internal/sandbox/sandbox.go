package sandbox

import (
	"context"
	"os/exec"
	"time"
)

// Sandbox provides isolated execution of commands.
type Sandbox interface {
	Exec(ctx context.Context, name string, args []string) (*exec.Cmd, error)
	PostStart(pid int) error // apply rlimits etc. after process starts
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
	Deny      []string      // paths to mask (e.g. ~/.ssh)
	Timeout   time.Duration
	CPULimit  time.Duration // RLIMIT_CPU (0 = backend default)
	MemLimit  uint64        // RLIMIT_AS in bytes (0 = backend default)
	MaxFDs    uint32        // RLIMIT_NOFILE (0 = backend default)
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
