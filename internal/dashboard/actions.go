package dashboard

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
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
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	command := cliCommand(ctx, executable, args, streams)
	if streams.In == nil {
		return runActionProcess(ctx, command.Start, command.Wait, func(sig syscall.Signal) error {
			return syscall.Kill(-command.Process.Pid, sig)
		})
	}
	return command.Run()
}

func cliCommand(ctx context.Context, executable string, args []string, streams remotepkg.IO) *exec.Cmd {
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
	if act.attach {
		ref := act.row.ID
		if act.row.machine != "local" {
			ref = act.row.machine + ":" + ref
		}
		return []string{"attach", "--", ref}, nil
	}
	var args []string
	if act.row.machine != "local" {
		remote, exists := remotes[act.row.machine]
		if !exists {
			return nil, fmt.Errorf("unknown remote %q", act.row.machine)
		}
		args = append(args, "--remote", remote.SSHTarget)
		if remote.WingthingDir != "" {
			args = append(args, "--remote-state", remote.WingthingDir)
		}
	}
	switch act.kind {
	case rename:
		args = append(args, "session", "rename", "--", act.row.ID, act.name)
	case confirmStop:
		args = append(args, "session", "kill", "--", act.row.ID)
	case newCWD:
		if act.agent == "" || act.agent == "shell" {
			args = append(args, "terminal", "--cwd", act.cwd)
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
