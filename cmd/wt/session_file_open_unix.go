//go:build linux || darwin

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// openSessionFileNoFollow opens a path beneath root without following any
// symlink component. Policy checks happen before this call; component-by-
// component openat keeps a rename from changing what those checks authorize.
func openSessionFileNoFollow(root, relative string) (*os.File, error) {
	parts := strings.Split(filepath.Clean(relative), string(filepath.Separator))
	if filepath.IsAbs(relative) || len(parts) == 0 {
		return nil, errors.New("file path must be relative")
	}
	before, err := os.Lstat(root)
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("file root is unavailable")
	}
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	rootFile := os.NewFile(uintptr(fd), root)
	after, statErr := rootFile.Stat()
	if statErr != nil || !os.SameFile(before, after) {
		_ = rootFile.Close()
		return nil, errors.New("file root changed while opening it")
	}
	fd = int(rootFile.Fd())
	for index, part := range parts {
		if part == "" || part == "." || part == ".." {
			_ = rootFile.Close()
			return nil, errors.New("invalid file path component")
		}
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
		if index < len(parts)-1 {
			flags |= unix.O_DIRECTORY
		} else {
			// A blocking open of a FIFO waits for a writer before the caller
			// can reject it as non-regular. Open the leaf nonblocking, verify
			// its type, then restore ordinary blocking I/O for regular files.
			flags |= unix.O_NONBLOCK
		}
		next, openErr := unix.Openat(fd, part, flags, 0)
		_ = rootFile.Close()
		if openErr != nil {
			return nil, openErr
		}
		fd = next
		rootFile = os.NewFile(uintptr(fd), part)
	}
	info, err := rootFile.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = rootFile.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("only regular files can be opened")
	}
	if err := unix.SetNonblock(int(rootFile.Fd()), false); err != nil {
		_ = rootFile.Close()
		return nil, err
	}
	return rootFile, nil
}
