package sandbox

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"syscall"
)

type fallbackSandbox struct {
	cfg    Config
	tmpDir string
}

func newFallback(cfg Config) (Sandbox, error) {
	dir, err := os.MkdirTemp("", "wt-sandbox-*")
	if err != nil {
		return nil, fmt.Errorf("create sandbox tmpdir: %w", err)
	}
	log.Printf("warning: no platform sandbox available, using process-level isolation (tmpdir=%s)", dir)
	return &fallbackSandbox{cfg: cfg, tmpDir: dir}, nil
}

func (s *fallbackSandbox) Exec(ctx context.Context, name string, args []string) (*exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = s.tmpDir
	cmd.Env = s.buildEnv()
	s.setLimits(cmd)
	return cmd, nil
}

func (s *fallbackSandbox) Destroy() error {
	return os.RemoveAll(s.tmpDir)
}

func (s *fallbackSandbox) buildEnv() []string {
	// Fallback sandbox is process-level isolation only (not a real sandbox).
	// Pass through the full environment so agents can authenticate (keychain,
	// session tokens, etc). Override TMPDIR for isolation. Real sandboxing
	// happens via Apple Containers (macOS) or namespaces (Linux).
	env := os.Environ()
	filtered := env[:0]
	for _, e := range env {
		if len(e) > 7 && e[:7] == "TMPDIR=" {
			continue
		}
		filtered = append(filtered, e)
	}
	return append(filtered, "TMPDIR="+s.tmpDir)
}

func (s *fallbackSandbox) PostStart(pid int) error {
	if len(s.cfg.Deny) > 0 {
		log.Printf("warning: fallback sandbox does not support deny paths")
	}
	return nil
}

func (s *fallbackSandbox) setLimits(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{}
}
