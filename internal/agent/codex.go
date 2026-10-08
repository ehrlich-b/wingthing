package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
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
	if strings.TrimSpace(prompt) == "" {
		return nil, errors.New("prompt is required")
	}
	// Codex reserves a literal "-" prompt for reading stdin.
	if prompt == "-" {
		prompt += "\n"
	}
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
	if opts.Model != "" {
		args = append(args, "-m", opts.Model)
	}
	args = append(args, "--json", "--", prompt)

	var cmd *exec.Cmd
	if opts.CmdFactory != nil {
		cmd, err = opts.CmdFactory(ctx, c.command, args)
		if err != nil {
			return nil, fmt.Errorf("sandbox exec: %w", err)
		}
	} else {
		cmd = exec.CommandContext(ctx, c.command, args...)
	}
	// A command factory may supply inherited stdin. Headless Codex must get
	// /dev/null even when its coordinator has a pipe open.
	cmd.Stdin = nil
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

	providerHome := codexHome(cmd)
	stream := newStream(ctx)
	go func() {
		var threadID string
		readErr := readProviderLines(stdout, "codex", func(line string) {
			var session struct {
				Type     string `json:"type"`
				ThreadID string `json:"thread_id"`
			}
			if json.Unmarshal([]byte(line), &session) == nil && session.Type == "thread.started" {
				threadID = session.ThreadID
				stream.setProviderSession(threadID, "")
				stream.send(Chunk{ThreadID: threadID})
			}
			if text, ok := parseCodexEvent(line); ok {
				stream.send(Chunk{Text: text})
			}
			if input, output, ok := parseCodexUsage(line); ok {
				stream.SetTokens(input, output)
				// A completed turn supersedes earlier retry diagnostics.
				diagnostics.failure = ""
			}
			if kind, ok := parseCodexFailure(line); ok {
				diagnostics.failure = preferFailureKind(diagnostics.failure, kind)
			}
		})
		err := waitAgentCommand(cmd, diagnostics, "codex")
		if readErr != nil {
			err = errors.Join(err, &Failure{Kind: ProviderError, Provider: "codex"}, readErr)
		}
		stream.setProviderSession(threadID, codexRolloutPath(providerHome, threadID))
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

func parseCodexFailure(line string) (ErrorKind, bool) {
	var ev struct {
		Type    string          `json:"type"`
		Message json.RawMessage `json:"message"`
		Error   json.RawMessage `json:"error"`
		Item    *struct {
			Type    string          `json:"type"`
			Message json.RawMessage `json:"message"`
		} `json:"item"`
	}
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return "", false
	}
	message := providerErrorText(ev.Message) + "\n" + providerErrorText(ev.Error)
	if ev.Item != nil {
		message += "\n" + providerErrorText(ev.Item.Message)
	}
	switch {
	case ev.Type == "turn.failed", ev.Type == "error":
		return eventFailureKind(message), true
	case ev.Type == "item.completed" && ev.Item != nil && ev.Item.Type == "error":
		return eventFailureKind(message), true
	}
	return "", false
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
