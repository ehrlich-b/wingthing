//go:build darwin || linux

package protectedfile

import (
	"fmt"
	"os"
	"syscall"
)

// NONBLOCK prevents a substituted FIFO from blocking before fstat rejects it.
func Open(path string) (*File, error) { return open(path, false) }

// OpenPolicy trusts root-installed and account-owned loader YAML, provided
// neither group nor others can write it. Credentials use Open instead.
func OpenPolicy(path string) (*File, error) { return open(path, true) }

func open(path string, policy bool) (*File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		if err == syscall.ENOENT {
			return nil, &os.PathError{Op: "open protected file without following links", Path: path, Err: err}
		}
		return nil, &Error{path, "cannot open without following links: " + err.Error()}
	}
	f := os.NewFile(uintptr(fd), path)
	info, dev, ino, err := inspect(f, policy)
	if err != nil {
		f.Close()
		return nil, err
	}
	return &File{File: f, Info: info, Dev: dev, Ino: ino, policy: policy}, nil
}

func inspect(f *os.File, policy bool) (os.FileInfo, uint64, uint64, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, 0, 0, err
	}
	refuse := func(reason string) (os.FileInfo, uint64, uint64, error) {
		return nil, 0, 0, &Error{f.Name(), reason}
	}
	if !info.Mode().IsRegular() {
		return refuse("must be a regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return refuse("no device/inode identity")
	}
	if stat.Nlink != 1 {
		return refuse(fmt.Sprintf("must have exactly one link; multiple hard links (%d)", stat.Nlink))
	}
	if policy {
		if stat.Uid != 0 && int(stat.Uid) != os.Getuid() {
			return refuse("policy must be owned by root or this OS account")
		}
		if info.Mode().Perm()&0022 != 0 {
			return refuse("policy must not be writable by group or others")
		}
	} else {
		if int(stat.Uid) != os.Getuid() {
			return refuse("must be owned by this OS account")
		}
		if mode := info.Mode().Perm(); mode != 0600 && mode != 0400 {
			return refuse("must have mode 0600 (or 0400 for a read-only credential)")
		}
	}
	return info, uint64(stat.Dev), uint64(stat.Ino), nil
}
