package agent

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"strings"
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
	cmd := exec.Command(g.command, "--version")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gemini health check failed: %w", err)
	}
	return nil
}

func (g *Gemini) Run(ctx context.Context, prompt string, opts RunOpts) (_ *Stream, err error) {
	args := []string{"-p", prompt}

	var cmd *exec.Cmd
	if opts.CmdFactory != nil {
		cmd, err = opts.CmdFactory(ctx, g.command, args)
		if err != nil {
			return nil, fmt.Errorf("sandbox exec: %w", err)
		}
	} else {
		cmd = exec.CommandContext(ctx, g.command, args...)
	}
	cmd.Stdin = strings.NewReader(prompt)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
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
		err := cmd.Wait()
		if scanErr := scanner.Err(); scanErr != nil && err == nil {
			err = scanErr
		}
		stream.close(err)
	}()

	return stream, nil
}
