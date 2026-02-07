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
	env := []string{
		"PATH=/usr/bin:/bin",
		"HOME=" + s.tmpDir,
		"TMPDIR=" + s.tmpDir,
	}
	return env
}

func (s *fallbackSandbox) setLimits(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{}
}
