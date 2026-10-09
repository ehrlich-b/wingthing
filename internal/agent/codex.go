package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

const codexCompletionGrace = 2 * time.Second

// Closing an inherited pipe after completion is an EOF for the event parser,
// which must still report any oversized final events it skipped earlier.
type codexOutputPipe struct {
	io.ReadCloser
	closing atomic.Bool
}

func (p *codexOutputPipe) Read(b []byte) (int, error) {
	n, err := p.ReadCloser.Read(b)
	if p.closing.Load() && errors.Is(err, os.ErrClosed) {
		err = io.EOF
	}
	return n, err
}

func (p *codexOutputPipe) close() {
	p.closing.Store(true)
	_ = p.ReadCloser.Close()
}

type Codex struct {
	command   string
	ctxWindow int
	// Tests may observe the actual group signal without relying on PID reuse.
	signalGroup func(*os.Process) error
}

// The unreaped provider pins its PID (and therefore our PGID). All group
// signals must finish before Wait can release that ownership, including calls
// from exec.CommandContext's cancellation watcher.
type codexProcess struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	signal  func() error
	waiting bool
}

func (p *codexProcess) cancel() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.waiting {
		// Only the process handle is safe once Wait may have reaped the leader.
		return p.cmd.Process.Kill()
	}
	return p.signal()
}

func (p *codexProcess) complete() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.waiting {
		return false
	}
	// A successful group signal can kill descendants of an already-dead
	// provider. It does not establish that we killed the provider itself.
	exited, err := processExited(p.cmd.Process)
	return p.signal() == nil && err == nil && !exited
}

func (p *codexProcess) beginWait() {
	p.mu.Lock()
	p.waiting = true
	p.mu.Unlock()
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
	startedAt := time.Now()
	configureProcessTree(cmd)
	process := &codexProcess{cmd: cmd, signal: cmd.Cancel}
	if c.signalGroup != nil {
		process.signal = func() error { return c.signalGroup(cmd.Process) }
	} else if process.signal == nil {
		process.signal = func() error { return cmd.Process.Kill() }
	}
	if cmd.Cancel != nil {
		cmd.Cancel = process.cancel
	}
	diagnostics, err := startConfiguredAgentCommand(cmd)
	if err != nil {
		return nil, fmt.Errorf("start codex: %w", err)
	}

	providerHome := codexHome(cmd)
	stream := newStream(ctx)
	go func() {
		output := &codexOutputPipe{ReadCloser: stdout}
		// Even a descendant that escapes the process group must not keep the
		// supervisor stuck reading stdout after the caller cancels the run.
		stopContextClose := context.AfterFunc(ctx, output.close)
		defer stopContextClose()
		var completionTimer *time.Timer
		cleanupDone := make(chan struct{})
		var completed, killed bool
		completionCleanup := func() {
			killed = process.complete()
			output.close()
			close(cleanupDone)
		}
		startCompletionCleanup := func() {
			if completionTimer != nil {
				return
			}
			// Keep draining during normal CLI shutdown, then stop our process
			// group and close stdout without depending on EOF or cmd.Wait.
			completionTimer = time.AfterFunc(codexCompletionGrace, completionCleanup)
		}
		var threadID string
		readErr := readProviderLines(output, "codex", func(line string) {
			var session struct {
				Type     string `json:"type"`
				ThreadID string `json:"thread_id"`
			}
			if json.Unmarshal([]byte(line), &session) == nil {
				switch session.Type {
				case "thread.started":
					threadID = ""
					if validCodexThreadID(session.ThreadID) {
						threadID = session.ThreadID
					}
					stream.setProviderSession(threadID, "")
					if threadID != "" {
						stream.send(Chunk{ThreadID: threadID})
					}
				case "turn.completed":
					completed = true
					// A completed turn supersedes earlier retry diagnostics,
					// including terminal events without optional usage fields.
					diagnostics.failure = ""
					startCompletionCleanup()
				case "turn.failed":
					startCompletionCleanup()
				}
			}
			if text, ok := parseCodexEvent(line); ok {
				stream.send(Chunk{Text: text})
			}
			if input, output, ok := parseCodexUsage(line); ok {
				stream.SetTokens(input, output)
			}
			if kind, ok := parseCodexFailure(line); ok {
				diagnostics.failure = preferFailureKind(diagnostics.failure, kind)
			}
		})
		if completionTimer != nil {
			// Even EOF on stdout does not mean inherited stderr has closed.
			// Finish the last group signal while the leader is still unreaped.
			// A known exit or cancellation needs no further shutdown grace.
			exited, exitErr := processExited(cmd.Process)
			if (ctx.Err() != nil || exitErr == nil && exited) && completionTimer.Stop() {
				completionCleanup()
			}
			<-cleanupDone
		}
		process.beginWait()
		err := waitAgentCommand(cmd, diagnostics, "codex")
		if completed && killed && ctx.Err() == nil && diagnostics.failure == "" && processTreeKilled(err) {
			// Only our forced SIGKILL after a successful terminal event is
			// expected. Preserve natural nonzero exits and provider failures.
			err = nil
		}
		if readErr != nil {
			err = errors.Join(err, &Failure{Kind: ProviderError, Provider: "codex"}, readErr)
		}
		stream.setProviderSession(threadID, codexRolloutPath(providerHome, threadID, startedAt))
		stream.close(err)
	}()

	return stream, nil
}

func validCodexThreadID(id string) bool {
	if len(id) != 36 {
		return false
	}
	_, err := uuid.Parse(id)
	return err == nil
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
