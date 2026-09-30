//go:build !linux && !darwin

package main

import (
	"errors"
	"os"
)

func openSessionFileNoFollow(_, _ string) (*os.File, error) {
	return nil, errors.New("safe session file access is unsupported on this platform")
}
