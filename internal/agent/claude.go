package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

type Claude struct {
	contextWindow int
}

func NewClaude(contextWindow int) *Claude {
	if contextWindow <= 0 {
		contextWindow = 200000
	}
	return &Claude{contextWindow: contextWindow}
}

func (c *Claude) ContextWindow() int {
	return c.contextWindow
}

func (c *Claude) Health() error {
	cmd := exec.Command("claude", "--version")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("claude health check failed: %w", err)
	}
	return nil
}

func (c *Claude) Run(ctx context.Context, prompt string, opts RunOpts) (_ *Stream, err error) {
	args := []string{"-p", prompt, "--output-format", "stream-json", "--verbose"}
	if opts.SystemPrompt != "" {
		if opts.ReplaceSystemPrompt {
			args = append(args, "--system-prompt", opts.SystemPrompt)
		} else {
			args = append(args, "--append-system-prompt", opts.SystemPrompt)
		}
	}
	if len(opts.AllowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(opts.AllowedTools, ","))
	}

	var cmd *exec.Cmd
	if opts.CmdFactory != nil {
		cmd, err = opts.CmdFactory(ctx, "claude", args)
		if err != nil {
			return nil, fmt.Errorf("sandbox exec: %w", err)
		}
	} else {
		cmd = exec.CommandContext(ctx, "claude", args...)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start claude: %w", err)
	}

	stream := newStream(ctx)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
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

type streamEvent struct {
	Type    string       `json:"type"`
	Message *messageBody `json:"message,omitempty"`
	Delta   *deltaBody   `json:"delta,omitempty"`
}

type messageBody struct {
	Content []contentBlock `json:"content"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type deltaBody struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type resultEvent struct {
	Type         string `json:"type"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
}

func parseResultTokens(line string) (input, output int, ok bool) {
	var ev resultEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return 0, 0, false
	}
	if ev.Type != "result" {
		return 0, 0, false
	}
	return ev.InputTokens, ev.OutputTokens, true
}

func parseStreamEvent(line string) (string, bool) {
	var ev streamEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return "", false
	}
	switch ev.Type {
	case "assistant":
		if ev.Message != nil {
			for _, block := range ev.Message.Content {
				if block.Type == "text" && block.Text != "" {
					return block.Text, true
				}
			}
		}
	case "content_block_delta":
		if ev.Delta != nil && ev.Delta.Type == "text_delta" && ev.Delta.Text != "" {
			return ev.Delta.Text, true
		}
	}
	return "", false
}
