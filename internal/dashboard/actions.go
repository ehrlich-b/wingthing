package dashboard

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
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
	return command(ctx, args, streams)
}

// Use a child for the existing attach/spawn paths. Their input goroutines live
// only as long as the child, including when the agent exits without a detach.
func runCommand(ctx context.Context, args []string, streams remotepkg.IO) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, executable, args...)
	command.WaitDelay = 100 * time.Millisecond
	command.Stdin, command.Stdout, command.Stderr = streams.In, streams.Out, streams.ErrOut
	return command.Run()
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
