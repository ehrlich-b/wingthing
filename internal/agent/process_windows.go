//go:build windows

package agent

import (
	"errors"
	"os"
	"os/exec"
	"time"
)

func processSignalGone(err error) bool { return errors.Is(err, os.ErrProcessDone) }

func stopProcessForCleanup(_ *os.Process) (bool, error) {
	return false, errors.ErrUnsupported
}

func processTreeKilled(err error) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == 1
}

func configureProcessTree(cmd *exec.Cmd) {
	cmd.WaitDelay = 5 * time.Second
}
