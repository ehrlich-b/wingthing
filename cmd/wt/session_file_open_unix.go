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
	if !filepath.IsAbs(root) {
		return nil, errors.New("file root must be absolute")
	}
	// O_NOFOLLOW on an absolute path protects only its final component. Walk
	// the authorized root from the filesystem root so its ancestors are bound
	// by descriptors too, without resolving a newly introduced symlink.
	directoryFlags := unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	fd, err := unix.Open(string(filepath.Separator), directoryFlags, 0)
	if err != nil {
		return nil, err
	}
	rootFile := os.NewFile(uintptr(fd), root)
	for _, part := range strings.Split(strings.TrimPrefix(filepath.Clean(root), string(filepath.Separator)), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		next, openErr := unix.Openat(int(rootFile.Fd()), part, directoryFlags, 0)
		_ = rootFile.Close()
		if openErr != nil {
			return nil, openErr
		}
		rootFile = os.NewFile(uintptr(next), part)
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
	var stat unix.Stat_t
	err = unix.Fstat(int(rootFile.Fd()), &stat)
	if err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = rootFile.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("only regular files can be opened")
	}
	if stat.Nlink > 1 {
		_ = rootFile.Close()
		return nil, errors.New("files with multiple hardlinks cannot be downloaded or exported")
	}
	if err := unix.SetNonblock(int(rootFile.Fd()), false); err != nil {
		_ = rootFile.Close()
		return nil, err
	}
	return rootFile, nil
}
