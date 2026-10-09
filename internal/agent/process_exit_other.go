//go:build !darwin && !linux && !windows

package agent

import (
	"errors"
	"os"
)

func processGroupSignalGone(_ *os.Process, err error) bool { return processSignalGone(err) }

func processExited(_ *os.Process) (bool, error) {
	// Without a non-reaping exit probe, preserve the provider's exit error.
	return false, errors.ErrUnsupported
}

func waitProcessExit(_ *os.Process) error { return errors.ErrUnsupported }

func processStopped(_ *os.Process) (bool, bool, error) {
	return false, false, errors.ErrUnsupported
}
