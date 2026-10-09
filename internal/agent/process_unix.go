//go:build !windows

package agent

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func processSignalGone(err error) bool {
	return errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH)
}

func stopProcessForCleanup(process *os.Process) (bool, error) {
	if err := process.Signal(syscall.SIGSTOP); err != nil {
		return false, err
	}
	// A stopped leader cannot race our kill with a self-originated signal.
	// Unknown or uninterruptible states fail closed: cleanup still kills the
	// group, but its exit must not be rewritten as success.
	deadline := time.Now().Add(time.Second)
	for {
		stopped, exited, err := processStopped(process)
		if err != nil || exited || stopped {
			return stopped && !exited, err
		}
		if time.Now().After(deadline) {
			return false, errors.New("provider did not stop for cleanup")
		}
		time.Sleep(time.Millisecond)
	}
}

func processTreeKilled(err error) bool {
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return false
	}
	status, ok := exit.Sys().(syscall.WaitStatus)
	return ok && status.Signaled() && status.Signal() == syscall.SIGKILL
}

func configureProcessTree(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Headless agent CLIs routinely spawn background servers. Kill the whole
		// process group so cancellation cannot leave a paid model call running.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 5 * time.Second
}
