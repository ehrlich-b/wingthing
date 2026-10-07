//go:build darwin

package wingpolicy

import (
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// F_GETPATH returns the on-disk spelling, including on case-insensitive APFS.
func canonicalPathCase(path string) string {
	f, err := os.OpenFile(path, os.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return path
	}
	defer f.Close()
	var buf [1024]byte // Darwin MAXPATHLEN, required by F_GETPATH.
	_, _, errno := unix.Syscall(unix.SYS_FCNTL, f.Fd(), unix.F_GETPATH, uintptr(unsafe.Pointer(&buf[0])))
	if errno != 0 {
		return path
	}
	return unix.ByteSliceToString(buf[:])
}
