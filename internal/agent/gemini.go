package agent

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
)

type Gemini struct {
	command   string
	model     string
	ctxWindow int
}

func NewGemini(model string, ctxWindow int) *Gemini {
	if model == "" {
		model = "gemini-2.5-pro"
	}
	if ctxWindow <= 0 {
		ctxWindow = 1000000
	}
	return &Gemini{
		command:   "gemini",
		model:     model,
		ctxWindow: ctxWindow,
	}
}

func (g *Gemini) ContextWindow() int {
	return g.ctxWindow
}

func (g *Gemini) Health() error {
	if err := runHealthCheck(healthCheckTimeout, g.command, "--version"); err != nil {
		return fmt.Errorf("gemini health check failed: %w", err)
	}
	return nil
}

func (g *Gemini) Run(ctx context.Context, prompt string, opts RunOpts) (_ *Stream, err error) {
	args := []string{"-p", prompt, "--model", g.model}
	// The Wingthing sandbox is already the approval and filesystem boundary.
	// Headless Gemini otherwise cannot approve its own tool calls from a fresh
	// home, so let it execute tools only when that outer boundary is present.
	if opts.CmdFactory != nil {
		args = append(args, "--yolo")
	}

	var cmd *exec.Cmd
	if opts.CmdFactory != nil {
		cmd, err = opts.CmdFactory(ctx, g.command, args)
		if err != nil {
			return nil, fmt.Errorf("sandbox exec: %w", err)
		}
	} else {
		cmd = exec.CommandContext(ctx, g.command, args...)
	}
	if opts.WorkDir != "" {
		cmd.Dir = opts.WorkDir
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	diagnostics, err := startAgentCommand(cmd)
	if err != nil {
		return nil, fmt.Errorf("start gemini: %w", err)
	}

	stream := newStream(ctx)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if line != "" {
				stream.send(Chunk{Text: line + "\n"})
			}
		}
		err := waitAgentCommand(cmd, diagnostics)
		if scanErr := scanner.Err(); scanErr != nil && err == nil {
			err = scanErr
		}
		stream.close(err)
	}()

	return stream, nil
}
