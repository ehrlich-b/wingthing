package agent

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Hermes adapts Nous Research's pure one-shot interface. `hermes -z` is
// deliberately machine-facing: one prompt in, final response on stdout, no
// banner/spinner/session preamble.
type Hermes struct {
	command   string
	ctxWindow int
}

func NewHermes(ctxWindow int) *Hermes {
	if ctxWindow <= 0 {
		ctxWindow = 200000
	}
	return &Hermes{command: "hermes", ctxWindow: ctxWindow}
}

func (h *Hermes) ContextWindow() int {
	return h.ctxWindow
}

func (h *Hermes) Health() error {
	if err := runHealthCheck(healthCheckTimeout, h.command, "--version"); err != nil {
		return fmt.Errorf("hermes health check failed: %w", err)
	}
	return nil
}

func (h *Hermes) Run(ctx context.Context, prompt string, opts RunOpts) (_ *Stream, err error) {
	args := []string{}
	// Small local models are much more reliable when Hermes does not inject
	// every optional tool schema. This is an opt-in Wingthing canary control;
	// ordinary Hermes runs retain the upstream default tool selection.
	if toolsets := strings.TrimSpace(os.Getenv("WT_HERMES_TOOLSETS")); toolsets != "" {
		args = append(args, "-t", toolsets)
	}
	args = append(args, "-z", prompt)
	var cmd *exec.Cmd
	if opts.CmdFactory != nil {
		cmd, err = opts.CmdFactory(ctx, h.command, args)
		if err != nil {
			return nil, fmt.Errorf("sandbox exec: %w", err)
		}
	} else {
		cmd = exec.CommandContext(ctx, h.command, args...)
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
		return nil, fmt.Errorf("start hermes: %w", err)
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
