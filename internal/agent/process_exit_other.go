//go:build !darwin && !linux && !windows

package agent

import (
	"errors"
	"os"
)

func processExited(_ *os.Process) (bool, error) {
	// Without a non-reaping exit probe, preserve the provider's exit error.
	return false, errors.ErrUnsupported
}
