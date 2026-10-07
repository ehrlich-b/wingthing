package egg

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const providerReadFlags = unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC

// Pin the home from the filesystem root, refusing links in its ancestors too.
// Every subsequent operation walks relative to these same descriptors.
func openProviderHome(home string) (*os.File, error) {
	if !filepath.IsAbs(home) {
		return nil, errors.New("absolute provider home required")
	}
	fd, err := unix.Open("/", providerReadFlags|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	root := os.NewFile(uintptr(fd), "/")
	defer root.Close()
	return openProviderDirectory(root, strings.TrimPrefix(home, "/"), false)
}

func openProviderDirectory(root *os.File, relative string, create bool) (*os.File, error) {
	if filepath.IsAbs(relative) {
		return nil, errors.New("provider path must be relative")
	}
	parts := strings.Split(relative, string(filepath.Separator))
	for _, part := range parts {
		if part == ".." {
			return nil, errors.New("provider path must stay inside its home")
		}
	}
	fd, err := unix.Openat(int(root.Fd()), ".", providerReadFlags|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	defer func() {
		if fd >= 0 {
			_ = unix.Close(fd)
		}
	}()
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		next, err := unix.Openat(fd, part, providerReadFlags|unix.O_DIRECTORY, 0)
		if err == unix.ENOENT && create {
			if err = unix.Mkdirat(fd, part, 0700); err != nil && err != unix.EEXIST {
				return nil, err
			}
			next, err = unix.Openat(fd, part, providerReadFlags|unix.O_DIRECTORY, 0)
		}
		if err != nil {
			return nil, &os.PathError{Op: "open provider directory without following links", Path: filepath.Join(root.Name(), relative), Err: err}
		}
		_ = unix.Close(fd)
		fd = next
	}
	file := os.NewFile(uintptr(fd), filepath.Join(root.Name(), relative))
	fd = -1
	return file, nil
}

func openProviderRegularFile(root *os.File, relative string) (*os.File, error) {
	dir, err := openProviderDirectory(root, filepath.Dir(relative), false)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	return openProviderLeaf(dir, filepath.Base(relative))
}

func openProviderLeaf(dir *os.File, name string) (*os.File, error) {
	if filepath.Base(name) != name || name == "." || name == ".." {
		return nil, errors.New("invalid provider filename")
	}
	path := filepath.Join(dir.Name(), name)
	fd, err := unix.Openat(int(dir.Fd()), name, providerReadFlags, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open provider file without following links", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(fd), path)
	var stat unix.Stat_t
	if err = unix.Fstat(fd, &stat); err != nil {
		file.Close()
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		file.Close()
		return nil, fmt.Errorf("provider file %s must be regular with exactly one link", path)
	}
	return file, nil
}
