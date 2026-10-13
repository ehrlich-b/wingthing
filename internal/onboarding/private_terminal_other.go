//go:build !darwin && !linux

package onboarding

import (
	"errors"
	"os"
)

func ownsTerminal(info os.FileInfo) bool { return false }

func PrivateTerminal(in, out, errOut *os.File) error {
	return errors.New("provider login terminal verification is unavailable on this platform")
}
