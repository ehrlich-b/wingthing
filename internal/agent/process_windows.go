//go:build windows

package agent

import (
	"errors"
	"os/exec"
	"time"
)

func processTreeKilled(err error) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == 1
}

func configureProcessTree(cmd *exec.Cmd) {
	cmd.WaitDelay = 5 * time.Second
}
