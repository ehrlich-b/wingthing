package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"
	"syscall"

	"github.com/ehrlich-b/wingthing/internal/control"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
	"golang.org/x/term"
)

// claudeFailure never renders the underlying error, which may contain provider
// text or request values. Unwrap retains identity for existing programmatic users.
type claudeLaunchFailure struct {
	cause      error
	diagnostic control.LaunchDiagnostic
}

func (e *claudeLaunchFailure) Unwrap() error { return e.cause }

func (e *claudeLaunchFailure) LaunchDiagnostic() control.LaunchDiagnostic { return e.diagnostic }
func (e *claudeLaunchFailure) Error() string                              { return control.FormatLaunchDiagnostic(e.diagnostic) }

// The dashboard supplies an owner-controlled pipe. Providers and background
// wings receive no descriptor, and only the approved projection crosses it.
func claudeDiagnosticPipe() *os.File {
	if os.Getenv("WT_LAUNCH_DIAGNOSTIC_FD") != "3" {
		return nil
	}
	_ = os.Unsetenv("WT_LAUNCH_DIAGNOSTIC_FD")
	// The implicit wing and provider must not retain the dashboard's writer.
	// Mark it before any startup child is forked, rather than at error return.
	syscall.CloseOnExec(3)
	return os.NewFile(3, "launch-diagnostic")
}

// Route metadata through the outer CLI after it has checked the real terminal.
// Wrapping dashboard streams earlier would change SSH's TTY allocation.
func runRemoteClaude(ctx context.Context, invocation remotepkg.Invocation, streams remotepkg.IO) error {
	pipe := claudeDiagnosticPipe()
	if pipe == nil {
		return remotepkg.RunRemoteInvocation(ctx, invocation, streams)
	}
	defer pipe.Close()
	capture := &launchDiagnosticCapture{}
	if streams.Out == nil {
		streams.Out = io.Discard
	}
	if streams.ErrOut == nil {
		streams.ErrOut = io.Discard
	}
	streams.Out = io.MultiWriter(streams.Out, capture)
	streams.ErrOut = io.MultiWriter(streams.ErrOut, capture)
	err := remotepkg.RunRemoteInvocation(ctx, invocation, streams)
	if d, ok := capture.diagnostic(); err != nil && ok {
		wire, marshalErr := json.Marshal(d)
		if marshalErr == nil && len(wire) <= 4096 {
			_, _ = pipe.Write(wire)
		}
	}
	return err
}

// Arbitrary SSH output never becomes a diagnostic. Retain a bounded tail and
// decode only the optional controller frame into the approved field set.
type launchDiagnosticCapture struct {
	mu   sync.Mutex
	tail []byte
}

func (c *launchDiagnosticCapture) Write(data []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	count := len(data)
	if len(data) >= 16<<10 {
		c.tail = bytes.Clone(data[len(data)-(16<<10):])
		return count, nil
	}
	c.tail = append(c.tail, data...)
	if len(c.tail) > 16<<10 {
		c.tail = bytes.Clone(c.tail[len(c.tail)-(16<<10):])
	}
	return count, nil
}

func (c *launchDiagnosticCapture) diagnostic() (control.LaunchDiagnostic, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	start := bytes.LastIndex(c.tail, []byte(control.LaunchDiagnosticFramePrefix))
	if start < 0 {
		return control.LaunchDiagnostic{}, false
	}
	frame := c.tail[start+len(control.LaunchDiagnosticFramePrefix):]
	end := bytes.Index(frame, []byte("\x1b\\"))
	if end < 0 || end > 5464 {
		return control.LaunchDiagnostic{}, false
	}
	wire, err := base64.StdEncoding.DecodeString(string(frame[:end]))
	var d control.LaunchDiagnostic
	if err != nil || len(wire) > 4096 || json.Unmarshal(wire, &d) != nil {
		return d, false
	}
	return d.Bounded(), true
}

func publishClaudeDiagnostic(pipe *os.File, terminal io.Writer, err error) {
	if pipe != nil {
		defer pipe.Close()
	}
	var failure *claudeLaunchFailure
	if !errors.As(err, &failure) {
		return
	}
	wire, marshalErr := json.Marshal(failure.diagnostic.Bounded())
	if marshalErr == nil && len(wire) <= 4096 {
		if pipe != nil {
			_, _ = pipe.Write(wire)
			return
		}
		// SSH's allocated PTY merges remote stderr into stdout. An optional
		// terminal metadata frame survives that transport without changing the
		// launch argv or exposing provider text. Ordinary terminals ignore it.
		if file, ok := terminal.(*os.File); ok && term.IsTerminal(int(file.Fd())) && os.Getenv("TERM") != "dumb" {
			_, _ = io.WriteString(terminal, control.LaunchDiagnosticFramePrefix+base64.StdEncoding.EncodeToString(wire)+"\x1b\\")
		}
	}
}

func diagnoseClaudeFailure(err error, stage, reason, session string) error {
	if err == nil {
		return nil
	}
	d, ok := control.LaunchDiagnosticOf(err)
	if !ok {
		d = control.LaunchDiagnostic{Stage: stage, Reason: reason}
	}
	if errors.Is(err, context.Canceled) {
		d.Reason = "canceled"
	}
	if errors.Is(err, control.ErrSessionNameInUse) {
		d.Reason = "session_name_in_use"
	}
	if stage == "attach-after-admission" {
		d.Stage, d.Reason, d.Session = stage, reason, session
	}
	return &claudeLaunchFailure{cause: err, diagnostic: d.Bounded()}
}
