package agent

import (
	"bufio"
	"context"
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
	cmd := exec.Command(c.command, "--version")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cursor agent health check failed: %w", err)
	}
	return nil
}

func (c *Cursor) Run(ctx context.Context, prompt string, opts RunOpts) (_ *Stream, err error) {
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

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start cursor agent: %w", err)
	}

	stream := newStream(ctx)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			// Cursor stream-json uses the same event format as Claude Code
			if text, ok := parseStreamEvent(line); ok {
				stream.send(Chunk{Text: text})
			}
			if input, output, ok := parseResultTokens(line); ok {
				stream.SetTokens(input, output)
			}
		}
		err := cmd.Wait()
		if scanErr := scanner.Err(); scanErr != nil && err == nil {
			err = scanErr
		}
		stream.close(err)
	}()

	return stream, nil
}
