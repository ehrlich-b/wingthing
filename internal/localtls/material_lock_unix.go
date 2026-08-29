//go:build darwin || linux

package localtls

import (
	"os"
	"syscall"
)

func lockMaterialFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
}
