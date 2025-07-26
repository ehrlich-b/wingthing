package tools

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

type BashRunner struct {
	timeout time.Duration
}

func NewBashRunner() *BashRunner {
	return &BashRunner{
		timeout: 30 * time.Second, // Default timeout
	}
}

func (br *BashRunner) SetTimeout(timeout time.Duration) {
	br.timeout = timeout
}

func (br *BashRunner) Run(ctx context.Context, tool string, params map[string]any) (*Result, error) {
	if tool != "bash" {
		return &Result{Error: "unsupported tool: " + tool}, nil
	}
	
	command, ok := params["command"].(string)
	if !ok {
		return &Result{Error: "missing or invalid 'command' parameter"}, nil
	}
	
	// Create context with timeout
	ctx, cancel := context.WithTimeout(ctx, br.timeout)
	defer cancel()
	
	// Execute command
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	output, err := cmd.CombinedOutput()
	
	result := &Result{
		Output: strings.TrimSpace(string(output)),
	}
	
	if err != nil {
		result.Error = err.Error()
	}
	
	return result, nil
}

func (br *BashRunner) SupportedTools() []string {
	return []string{"bash"}
}
