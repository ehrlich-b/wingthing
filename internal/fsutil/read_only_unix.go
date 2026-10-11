//go:build !windows

package fsutil

import (
	"os"
	"syscall"
)

func openReadOnlyNonblocking(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
