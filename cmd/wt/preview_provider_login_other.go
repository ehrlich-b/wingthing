//go:build !darwin && !linux

package main

import (
	"errors"
	"os"
)

func previewProviderLoginOwns(info os.FileInfo) bool { return false }

func previewProviderLoginTerminal(in, out, errOut *os.File) error {
	return errors.New("provider login terminal verification is unavailable on this platform")
}
