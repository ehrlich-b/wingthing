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

func NewOllama(model string, ctxWindow int) *Ollama {
	if model == "" {
		model = "llama3.2"
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
	cmd := exec.Command(o.command, "list")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ollama health check failed: %w", err)
	}
	return nil
}

func (o *Ollama) Run(ctx context.Context, prompt string, opts RunOpts) (*Stream, error) {
	args := []string{"run", o.model}

	cmd := exec.CommandContext(ctx, o.command, args...)
	cmd.Stdin = strings.NewReader(prompt)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
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
		err := cmd.Wait()
		if scanErr := scanner.Err(); scanErr != nil && err == nil {
			err = scanErr
		}
		stream.close(err)
	}()

	return stream, nil
}
