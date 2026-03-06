package agent

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
)

type OpenCode struct {
	command   string
	ctxWindow int
}

func NewOpenCode(ctxWindow int) *OpenCode {
	if ctxWindow <= 0 {
		ctxWindow = 200000
	}
	return &OpenCode{
		command:   "opencode",
		ctxWindow: ctxWindow,
	}
}

func (o *OpenCode) ContextWindow() int {
	return o.ctxWindow
}

func (o *OpenCode) Health() error {
	cmd := exec.Command(o.command, "--version")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("opencode health check failed: %w", err)
	}
	return nil
}

func (o *OpenCode) Run(ctx context.Context, prompt string, opts RunOpts) (_ *Stream, err error) {
	args := []string{"-p", prompt, "-q"}

	var cmd *exec.Cmd
	if opts.CmdFactory != nil {
		cmd, err = opts.CmdFactory(ctx, o.command, args)
		if err != nil {
			return nil, fmt.Errorf("sandbox exec: %w", err)
		}
	} else {
		cmd = exec.CommandContext(ctx, o.command, args...)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start opencode: %w", err)
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
