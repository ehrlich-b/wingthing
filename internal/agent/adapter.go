package agent

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

const healthCheckTimeout = 10 * time.Second

type Agent interface {
	Run(ctx context.Context, prompt string, opts RunOpts) (*Stream, error)
	Health() error
	ContextWindow() int
}

// CmdFactory creates an exec.Cmd that may run inside a sandbox.
// When nil, agents fall back to exec.CommandContext.
type CmdFactory func(ctx context.Context, name string, args []string) (*exec.Cmd, error)

type RunOpts struct {
	AllowedTools        []string
	SystemPrompt        string
	ReplaceSystemPrompt bool
	Timeout             time.Duration
	WorkDir             string
	CmdFactory          CmdFactory
}

type Chunk struct {
	Text string
}

func runHealthCheck(timeout time.Duration, command string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if err := exec.CommandContext(ctx, command, args...).Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("timed out after %s", timeout)
		}
		return err
	}
	return nil
}
