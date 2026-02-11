package sandbox

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
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

// EnforcementError is returned when the system cannot enforce the requested sandbox config.
type EnforcementError struct {
	Gaps     []string
	Platform string
}

func (e *EnforcementError) Error() string {
	msg := "system incapable of enforcing: " + strings.Join(e.Gaps, ", ")
	if e.Platform != "" {
		msg += ". " + e.Platform
	}
	return msg
}

// New creates a platform-appropriate sandbox. Returns EnforcementError if the
// platform cannot enforce the requested isolation — no silent fallback.
func New(cfg Config) (Sandbox, error) {
	s, err := newPlatform(cfg)
	if err == nil {
		return s, nil
	}
	return nil, newEnforcementError(cfg, err)
}

func newEnforcementError(cfg Config, platformErr error) *EnforcementError {
	var gaps []string
	switch cfg.Isolation {
	case Strict, Standard:
		gaps = append(gaps, "network isolation")
	}
	gaps = append(gaps, "filesystem isolation")
	if len(cfg.Deny) > 0 {
		gaps = append(gaps, fmt.Sprintf("deny paths (%d)", len(cfg.Deny)))
	}
	if cfg.CPULimit > 0 || cfg.MemLimit > 0 || cfg.MaxFDs > 0 {
		gaps = append(gaps, "resource limits")
	}
	return &EnforcementError{
		Gaps:     gaps,
		Platform: platformHelp(),
	}
}

func platformHelp() string {
	switch runtime.GOOS {
	case "darwin":
		return "macOS: requires Apple Containers (macOS 26+, 'container' CLI)"
	case "linux":
		return "Linux: requires root or CAP_SYS_ADMIN (try: sudo setcap cap_sys_admin+ep /path/to/wt-egg)"
	default:
		return fmt.Sprintf("platform %s: no sandbox backend available", runtime.GOOS)
	}
}
