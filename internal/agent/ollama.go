package agent

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

type Ollama struct {
	command   string
	model     string
	ctxWindow int
}

// DefaultOllamaModel is small enough for a developer workstation while still
// supporting Ollama's native structured tool-call protocol. Keep the catalog
// and headless adapter on the same model so PTY and prompt behavior agree.
const DefaultOllamaModel = "qwen3:4b"

func NewOllama(model string, ctxWindow int) *Ollama {
	if model == "" {
		model = DefaultOllamaModel
	}
	if ctxWindow <= 0 {
		ctxWindow = 128000
	}
	return &Ollama{
		command:   "ollama",
		model:     model,
		ctxWindow: ctxWindow,
	}
}

func (o *Ollama) ContextWindow() int {
	return o.ctxWindow
}

func (o *Ollama) Health() error {
	if err := runHealthCheck(healthCheckTimeout, o.command, "list"); err != nil {
		return fmt.Errorf("ollama health check failed: %w", err)
	}
	return nil
}

func (o *Ollama) Run(ctx context.Context, prompt string, opts RunOpts) (_ *Stream, err error) {
	args := []string{"run", o.model}

	var cmd *exec.Cmd
	if opts.CmdFactory != nil {
		cmd, err = opts.CmdFactory(ctx, o.command, args)
		if err != nil {
			return nil, fmt.Errorf("sandbox exec: %w", err)
		}
	} else {
		cmd = exec.CommandContext(ctx, o.command, args...)
	}
	if opts.WorkDir != "" {
		cmd.Dir = opts.WorkDir
	}
	cmd.Stdin = strings.NewReader(prompt)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	diagnostics, err := startAgentCommand(cmd)
	if err != nil {
		return nil, fmt.Errorf("start ollama: %w", err)
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
