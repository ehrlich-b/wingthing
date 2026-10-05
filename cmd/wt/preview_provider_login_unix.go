//go:build darwin || linux

package main

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func previewProviderLoginOwns(info os.FileInfo) bool {
	if info == nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Getuid())
}

func previewProviderLoginTerminal(in, out, errOut *os.File) error {
	if os.Getuid() != os.Geteuid() {
		return errors.New("provider login refuses a changed effective user")
	}
	var input os.FileInfo
	for _, stream := range []*os.File{in, out, errOut} {
		if stream == nil || !term.IsTerminal(int(stream.Fd())) {
			return errors.New("provider login requires interactive stdin, stdout and stderr in a private user terminal; pipes or captured agent execution are refused")
		}
		info, err := stream.Stat()
		if err != nil || !previewProviderLoginOwns(info) {
			return errors.New("provider login requires a terminal owned by this user")
		}
		if input == nil {
			input = info
		} else if !os.SameFile(input, info) {
			return errors.New("provider login requires all streams to use the same terminal")
		}
		foreground, err := unix.IoctlGetInt(int(stream.Fd()), unix.TIOCGPGRP)
		if err != nil || foreground != unix.Getpgrp() {
			return errors.New("provider login requires the foreground controlling terminal")
		}
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("provider login cannot open its controlling terminal: %w", err)
	}
	defer tty.Close()
	foreground, err := unix.IoctlGetInt(int(tty.Fd()), unix.TIOCGPGRP)
	if err != nil || foreground != unix.Getpgrp() {
		return errors.New("provider login requires its own foreground controlling terminal")
	}
	return nil
}
