package agent

import (
	"context"
	"time"
)

type Agent interface {
	Run(ctx context.Context, prompt string, opts RunOpts) (*Stream, error)
	Health() error
	ContextWindow() int
}

type RunOpts struct {
	AllowedTools []string
	SystemPrompt string
	Timeout      time.Duration
}

type Chunk struct {
	Text string
}
