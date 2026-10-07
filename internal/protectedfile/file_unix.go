//go:build darwin || linux

package protectedfile

import (
	"fmt"
	"os"
	"syscall"
)

// NONBLOCK prevents a substituted FIFO from blocking before fstat rejects it.
func Open(path string) (*File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open protected file without following links", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(fd), path)
	info, dev, ino, err := inspect(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	return &File{File: f, Info: info, Dev: dev, Ino: ino}, nil
}

func inspect(f *os.File) (os.FileInfo, uint64, uint64, error) {
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
	if int(stat.Uid) != os.Getuid() {
		return refuse("must be owned by this OS account")
	}
	return info, uint64(stat.Dev), uint64(stat.Ino), nil
}
