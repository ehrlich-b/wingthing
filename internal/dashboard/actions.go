package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
)

type commandRunner func(context.Context, []string, remotepkg.IO) error

func backgroundCommand(ctx context.Context, command commandRunner, args []string, streams remotepkg.IO) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("session action panic: %v", recovered)
		}
	}()
	err = command(ctx, args, streams)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		message := "timed out"
		if len(args) > 0 && args[0] == "--remote" {
			message += "; the remote may still complete"
		}
		return fmt.Errorf("%s: %w", message, ctx.Err())
	}
	return err
}

// Use a child for the existing attach/spawn paths. Their input goroutines live
// only as long as the child, including when the agent exits without a detach.
func runCommand(ctx context.Context, args []string, streams remotepkg.IO) error {
	if streams.In == nil {
		if err := validateRemoteAction(ctx, args, streams); err != nil {
			return err
		}
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	claude := len(args) > 0 && args[0] == "claude" || len(args) >= 3 && args[len(args)-3] == "claude" && args[len(args)-2] == "--cwd"
	command := cliCommand(ctx, executable, args, streams)
	if streams.In == nil {
		return runActionProcess(ctx, command.Start, command.Wait, func(sig syscall.Signal) error {
			return syscall.Kill(-command.Process.Pid, sig)
		})
	}
	if !claude {
		var diagnostic actionDiagnostic
		if streams.ErrOut != nil {
			command.Stderr = io.MultiWriter(streams.ErrOut, &diagnostic)
		}
		if err := command.Run(); err != nil {
			return fmt.Errorf("%w; Enter retries through fresh resolution. %s", err, diagnostic.text)
		}
		return nil
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return err
	}
	defer reader.Close()
	defer writer.Close()
	command.ExtraFiles = []*os.File{writer}
	command.Env = append(os.Environ(), "WT_LAUNCH_DIAGNOSTIC_FD=3")
	err = command.Run()
	_ = writer.Close()
	_ = reader.SetReadDeadline(time.Now().Add(time.Second))
	wire, readErr := io.ReadAll(io.LimitReader(reader, 4097))
	var d control.LaunchDiagnostic
	if err != nil && readErr == nil && len(wire) <= 4096 && len(wire) > 0 && json.Unmarshal(wire, &d) == nil {
		return &launchActionError{cause: err, diagnostic: d.Bounded()}
	}
	return err
}

// Retain only a small tail of attach diagnostics for the restored navigator.
// Terminal/provider output continues directly to its original destination.
type actionDiagnostic struct{ text string }

func (d *actionDiagnostic) Write(data []byte) (int, error) {
	const limit = 4096
	n := len(data)
	if len(data) > limit {
		data = data[len(data)-limit:]
	}
	d.text += string(data)
	if len(d.text) > limit {
		d.text = d.text[len(d.text)-limit:]
	}
	return n, nil
}

func qualifyLaunchAction(err error, machine string) error {
	var failure *launchActionError
	if machine != "local" && errors.As(err, &failure) && failure.diagnostic.Session != "" {
		copyFailure := *failure
		copyFailure.diagnostic.Session = machine + ":" + copyFailure.diagnostic.Session
		copyFailure.diagnostic = copyFailure.diagnostic.Bounded()
		return &copyFailure
	}
	return err
}

type launchActionError struct {
	cause      error
	diagnostic control.LaunchDiagnostic
}

func (e *launchActionError) Error() string { return control.FormatLaunchDiagnostic(e.diagnostic) }
func (e *launchActionError) Unwrap() error { return e.cause }

func cliCommand(ctx context.Context, executable string, args []string, streams remotepkg.IO) *exec.Cmd {
	if streams.In == nil && len(args) > 0 && args[0] == "--remote" {
		args = append([]string{"--remote-batch"}, args...)
	}
	command := exec.Command(executable, args...)
	if streams.In == nil {
		// Background actions isolate the CLI, SSH, and transport descendants.
		command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	} else {
		// Attach must stay in the foreground process group to read the TTY.
		command = exec.CommandContext(ctx, executable, args...)
	}
	command.WaitDelay = 100 * time.Millisecond
	command.Stdin, command.Stdout, command.Stderr = streams.In, streams.Out, streams.ErrOut
	return command
}

// Give the CLI a chance to propagate cancellation and reap SSH before killing
// any remaining group members. Always join Wait, including on dashboard exit.
func runActionProcess(ctx context.Context, start, wait func() error, signalGroup func(syscall.Signal) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- wait() }()
	exited := false
	select {
	case err := <-done:
		if ctx.Err() == nil {
			return err
		}
		exited = true
	case <-ctx.Done():
	}
	signal := func(sig syscall.Signal) error {
		err := signalGroup(sig)
		if errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return err
	}
	termErr := signal(syscall.SIGTERM)
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	if !exited {
		select {
		case <-done:
			exited = true
		case <-timer.C:
		}
	}
	// The CLI may exit before a transport descendant does.
	killErr := signal(syscall.SIGKILL)
	if !exited {
		<-done
	}
	return errors.Join(ctx.Err(), termErr, killErr)
}

func actionArgs(act action, remotes map[string]config.Remote) ([]string, error) {
	if act.kind == runAttach {
		return runAttachArgs(act.run), nil
	}
	if act.attach {
		ref := act.row.ID
		if act.row.machine != "local" {
			ref = act.row.machine + ":" + ref
		}
		args := []string{"attach"}
		if act.readOnly {
			args = append(args, "--read-only")
		}
		if act.takeover {
			args = append(args, "--takeover")
		}
		return append(args, "--", ref), nil
	}
	var args []string
	if act.row.machine != "local" {
		remote, exists := remotes[act.row.machine]
		if !exists {
			return nil, fmt.Errorf("unknown remote %q", act.row.machine)
		}
		args = remotepkg.ProfileArgs(remote)
	}
	switch act.kind {
	case rename:
		args = append(args, "session", "rename", "--", act.row.ID, act.name)
	case confirmStop:
		args = append(args, "session", "kill", "--", act.row.ID)
	case newCWD:
		if act.agent == "" || act.agent == "shell" {
			args = append(args, "terminal", "--cwd", act.cwd)
		} else if act.agent == "claude" {
			args = append(args, "claude", "--cwd", act.cwd)
		} else {
			if strings.HasPrefix(act.agent, "-") {
				return nil, fmt.Errorf("invalid agent %q", act.agent)
			}
			args = append(args, "egg", act.agent, "--cwd", act.cwd)
		}
	default:
		return nil, fmt.Errorf("unknown dashboard action")
	}
	return args, nil
}

// Dashboard argv has a canonical literal transport prefix. Recheck its saved
// profile and the full ID immediately before every remote rename/stop, so even
// retained rows cannot fall through to a display name or colliding local ID.
func validateRemoteAction(ctx context.Context, args []string, streams remotepkg.IO) error {
	if len(args) == 0 || args[0] != "--remote" {
		return nil
	}
	route := config.Remote{}
	i := 0
	for i+1 < len(args) {
		switch args[i] {
		case "--remote":
			route.SSHTarget = args[i+1]
		case "--remote-binary":
			route.WTBinary = args[i+1]
		case "--remote-state":
			route.WingthingDir = args[i+1]
		default:
			goto command
		}
		i += 2
	}
command:
	if len(args[i:]) < 4 || args[i] != "session" || (args[i+1] != "rename" && args[i+1] != "kill") || args[i+2] != "--" {
		return nil
	}
	id := args[i+3]
	dir, err := config.StateDir()
	if err != nil {
		return fmt.Errorf("cannot validate remote session: %w", err)
	}
	profiles, err := config.LoadRemotes(dir)
	if err != nil {
		return fmt.Errorf("cannot validate remote session: %w", err)
	}
	for _, name := range eggclient.SortedRemoteNames(profiles) {
		current := profiles[name]
		if current.SSHTarget == route.SSHTarget && current.Binary() == route.Binary() && current.WingthingDir == route.WingthingDir {
			return eggclient.ValidateRemoteSession(ctx, name, current, id, streams)
		}
	}
	return fmt.Errorf("cannot validate remote session %s on %s: saved profile changed or was removed; reopen wt before acting", id, route.SSHTarget)
}
