//go:build windows

package agent

import (
	"os/exec"
	"time"
)

func configureProcessTree(cmd *exec.Cmd) {
	cmd.WaitDelay = 5 * time.Second
}
