package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
)

type Codex struct {
	command   string
	ctxWindow int
}

func NewCodex(ctxWindow int) *Codex {
	if ctxWindow <= 0 {
		ctxWindow = 192000
	}
	return &Codex{
		command:   "codex",
		ctxWindow: ctxWindow,
	}
}

func (c *Codex) ContextWindow() int {
	return c.ctxWindow
}

func (c *Codex) Health() error {
	if err := runHealthCheck(healthCheckTimeout, c.command, "--version"); err != nil {
		return fmt.Errorf("codex health check failed: %w", err)
	}
	return nil
}

func (c *Codex) Run(ctx context.Context, prompt string, opts RunOpts) (_ *Stream, err error) {
	// Wingthing tasks are valid in arbitrary working directories. Codex rejects
	// non-repository directories by default, which made an otherwise healthy
	// harness fail before the model was contacted.
	args := []string{"exec", "--skip-git-repo-check"}
	// Linux namespace sandboxes cannot be nested reliably. When Wingthing has
	// supplied a command factory, Codex is already inside Wingthing's sandbox,
	// so disable only Codex's inner sandbox. Privileged/direct calls retain the
	// CLI's own protection because they have no command factory.
	if opts.CmdFactory != nil {
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	}
	args = append(args, prompt, "--json")

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

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	diagnostics, err := startAgentCommand(cmd)
	if err != nil {
		return nil, fmt.Errorf("start codex: %w", err)
	}

	stream := newStream(ctx)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if text, ok := parseCodexEvent(line); ok {
				stream.send(Chunk{Text: text})
			}
			if input, output, ok := parseCodexUsage(line); ok {
				stream.SetTokens(input, output)
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

// codexEvent represents a Codex CLI NDJSON event.
type codexEvent struct {
	Type  string      `json:"type"`
	Item  *codexItem  `json:"item,omitempty"`
	Usage *codexUsage `json:"usage,omitempty"`
}

type codexItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type codexUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func parseCodexEvent(line string) (string, bool) {
	var ev codexEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return "", false
	}
	if ev.Type == "item.completed" && ev.Item != nil && ev.Item.Type == "agent_message" && ev.Item.Text != "" {
		return ev.Item.Text, true
	}
	return "", false
}

func parseCodexUsage(line string) (input, output int, ok bool) {
	var ev codexEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return 0, 0, false
	}
	if ev.Type == "turn.completed" && ev.Usage != nil {
		return ev.Usage.InputTokens, ev.Usage.OutputTokens, true
	}
	return 0, 0, false
}
