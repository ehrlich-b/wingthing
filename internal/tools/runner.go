package tools

import (
	"context"
)

type Result struct {
	Output string `json:"output"`
	Error  string `json:"error,omitempty"`
}

type Runner interface {
	Run(ctx context.Context, tool string, params map[string]any) (*Result, error)
	SupportedTools() []string
}

type MultiRunner struct {
	runners map[string]Runner
}

func NewMultiRunner() *MultiRunner {
	return &MultiRunner{
		runners: make(map[string]Runner),
	}
}

func (mr *MultiRunner) RegisterRunner(name string, runner Runner) {
	mr.runners[name] = runner
}

func (mr *MultiRunner) Run(ctx context.Context, tool string, params map[string]any) (*Result, error) {
	runner, exists := mr.runners[tool]
	if !exists {
		return &Result{Error: "unsupported tool: " + tool}, nil
	}
	
	return runner.Run(ctx, tool, params)
}

func (mr *MultiRunner) SupportedTools() []string {
	var tools []string
	for name := range mr.runners {
		tools = append(tools, name)
	}
	return tools
}