package agent

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
)

type Cursor struct {
	command   string
	ctxWindow int
}

func NewCursor(ctxWindow int) *Cursor {
	if ctxWindow <= 0 {
		ctxWindow = 128000
	}
	// Cursor's headless CLI is called "agent"
	return &Cursor{
		command:   "agent",
		ctxWindow: ctxWindow,
	}
}

func (c *Cursor) ContextWindow() int {
	return c.ctxWindow
}

func (c *Cursor) Health() error {
	if err := runHealthCheck(healthCheckTimeout, c.command, "--version"); err != nil {
		return fmt.Errorf("cursor agent health check failed: %w", err)
	}
	return nil
}

func (c *Cursor) Run(ctx context.Context, prompt string, opts RunOpts) (_ *Stream, err error) {
	if opts.Model != "" {
		return nil, errors.New("cursor headless adapter does not support model selection")
	}

	args := []string{"-p", prompt, "--output-format", "stream-json"}

	var cmd *exec.Cmd
	if opts.CmdFactory != nil {
		cmd, err = opts.CmdFactory(ctx, c.command, args)
		if err != nil {
			return nil, fmt.Errorf("sandbox exec: %w", err)
		}
	} else {
		cmd = exec.CommandContext(ctx, c.command, args...)
	}
	if opts.WorkDir != "" {
		cmd.Dir = opts.WorkDir
	}

	stdout, diagnostics, err := startHeadlessCommand(ctx, cmd)
	if err != nil {
		return nil, fmt.Errorf("start cursor agent: %w", err)
	}

	stream := newStream(ctx)
	go func() {
		defer stdout.Close()
		var failureKind ErrorKind
		readErr := readProviderLines(stdout, "cursor", func(line string) {
			// Cursor stream-json uses the same event format as Claude Code
			if kind, failed := parseClaudeFailure(line); failed {
				failureKind = preferFailureKind(failureKind, kind)
			} else if text, ok := parseStreamEvent(line); ok {
				stream.send(Chunk{Text: text})
			}
			if input, output, ok := parseResultTokens(line); ok {
				stream.SetTokens(input, output)
			}
		})
		err := waitHeadlessCommand(cmd, diagnostics, "cursor", failureKind)
		if readErr != nil {
			err = errors.Join(err, readErr)
		}
		stream.close(err)
	}()

	return stream, nil
}
