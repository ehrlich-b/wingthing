package fsutil

import (
	"errors"
	"os"
)

// OpenBoundedRegular opens only an existing regular file, read-only, checking
// both sides of open for replacement and refusing oversized inputs. On Unix a
// nonblocking open prevents a raced FIFO/device from hanging diagnostics.
func OpenBoundedRegular(path string, limit int64) (*os.File, error) {
	before, err := os.Stat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() > limit {
		return nil, errors.New("input must be an existing bounded regular file")
	}
	file, err := openReadOnlyNonblocking(path)
	if err != nil {
		return nil, errors.New("input cannot be opened read-only")
	}
	after, err := file.Stat()
	if err != nil || !after.Mode().IsRegular() || after.Size() > limit || !os.SameFile(before, after) {
		_ = file.Close()
		return nil, errors.New("input changed during read-only open")
	}
	return file, nil
}
