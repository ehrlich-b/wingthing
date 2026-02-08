package agent

import (
	"context"
	"os/exec"
	"time"
)

type Agent interface {
	Run(ctx context.Context, prompt string, opts RunOpts) (*Stream, error)
	Health() error
	ContextWindow() int
}

// CmdFactory creates an exec.Cmd that may run inside a sandbox.
// When nil, agents fall back to exec.CommandContext.
type CmdFactory func(ctx context.Context, name string, args []string) (*exec.Cmd, error)

type RunOpts struct {
	AllowedTools         []string
	SystemPrompt         string
	ReplaceSystemPrompt  bool
	Timeout              time.Duration
	CmdFactory           CmdFactory
}

type Chunk struct {
	Text string
}
