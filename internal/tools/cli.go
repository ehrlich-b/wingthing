package tools

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

type CLIRunner struct {
	timeout time.Duration
}

func NewCLIRunner() *CLIRunner {
	return &CLIRunner{
		timeout: 30 * time.Second, // Default timeout
	}
}

func (cr *CLIRunner) SetTimeout(timeout time.Duration) {
	cr.timeout = timeout
}

func (cr *CLIRunner) Run(ctx context.Context, tool string, params map[string]any) (*Result, error) {
	if tool != "cli" {
		return &Result{Error: "unsupported tool: " + tool}, nil
	}
	
	command, ok := params["command"].(string)
	if !ok {
		return &Result{Error: "missing or invalid 'command' parameter"}, nil
	}
	
	// Create context with timeout
	ctx, cancel := context.WithTimeout(ctx, cr.timeout)
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

func (cr *CLIRunner) SupportedTools() []string {
	return []string{"cli"}
}
