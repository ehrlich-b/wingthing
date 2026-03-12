package sandbox

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
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
	DiagLog() string  // path to sandbox diagnostic log, or ""
	TraceLog() string // path to strace output log, or ""
}

// Mount describes a filesystem mount for the sandbox.
type Mount struct {
	Source   string
	Target   string
	ReadOnly bool
	UseRegex bool // macOS: emit regex rule instead of subpath (covers adjacent files like ~/.claude.json)
}

// Config holds sandbox creation parameters.
type Config struct {
	Mounts      []Mount
	Deny        []string    // paths to mask (e.g. ~/.ssh) — deny read+write
	DenyWrite   []string    // paths to deny writes only (e.g. ./egg.yaml) — read allowed
	NetworkNeed NetworkNeed // granular network access required by the agent
	Domains     []string    // domain allowlist for proxy filtering
	ProxyPort   int         // local domain-filtering proxy port (0 = no proxy)
	CPULimit    time.Duration // RLIMIT_CPU (0 = backend default)
	MemLimit    uint64        // RLIMIT_AS in bytes (0 = backend default)
	MaxFDs      uint32        // RLIMIT_NOFILE (0 = backend default)
	PidLimit    uint32        // cgroup pids.max (0 = no limit)
	SessionID   string        // unique ID for cgroup naming
	UserHome     string        // per-user home override (empty = os.UserHomeDir)
	Trace        bool          // wrap command with strace (Linux only)
	AllowSockets []string      // Unix socket paths to allow outbound connections (macOS Seatbelt)
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
	if cfg.NetworkNeed < NetworkFull {
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

// CheckCapability reports whether the current system can run sandboxed agents.
// Returns (true, "") if capable, or (false, helpMessage) with fix instructions.
func CheckCapability() (bool, string) {
	// Suppress log output from newPlatform during capability check.
	prev := log.Writer()
	log.SetOutput(io.Discard)
	s, err := newPlatform(Config{
		Mounts: []Mount{{Source: "/tmp", Target: "/tmp", ReadOnly: true}},
	})
	log.SetOutput(prev)
	if err != nil {
		return false, platformHelp()
	}
	s.Destroy()
	return true, ""
}

func platformHelp() string {
	switch runtime.GOOS {
	case "darwin":
		return "macOS: requires Apple Containers (macOS 26+, 'container' CLI)"
	case "linux":
		// Check if AppArmor is specifically blocking unprivileged user namespaces (Ubuntu 24.04+, kernel 6.1+).
		if val, err := os.ReadFile("/proc/sys/kernel/apparmor_restrict_unprivileged_userns"); err == nil {
			if strings.TrimSpace(string(val)) == "1" {
				exe, _ := os.Executable()
				if exe == "" {
					exe = "/usr/local/bin/wt"
				}
				return fmt.Sprintf("Linux: AppArmor is blocking unprivileged user namespaces (apparmor_restrict_unprivileged_userns=1). "+
					"Fix: create an AppArmor profile for wt:\n"+
					"  sudo tee /etc/apparmor.d/wingthing <<'EOF'\n"+
					"  abi <abi/4.0>,\n"+
					"  profile wingthing %s flags=(unconfined) {\n"+
					"    userns,\n"+
					"  }\n"+
					"  EOF\n"+
					"  sudo apparmor_parser -r /etc/apparmor.d/wingthing\n"+
					"Or disable the restriction globally: sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0", exe)
			}
		}
		return "Linux: your system does not allow unprivileged user namespaces, which wt needs to sandbox agents. " +
			"Fix: sudo sysctl -w kernel.unprivileged_userns_clone=1 (or run: sudo wt egg claude)"
	default:
		return fmt.Sprintf("platform %s: no sandbox backend available", runtime.GOOS)
	}
}
