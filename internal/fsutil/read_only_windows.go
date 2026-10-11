//go:build windows

package fsutil

import "os"

func openReadOnlyNonblocking(path string) (*os.File, error) { return os.Open(path) }
