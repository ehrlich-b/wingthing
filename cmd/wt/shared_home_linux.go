//go:build linux

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

func prepareSharedAgentHome(home string, relativeDirs []string) error {
	parent := filepath.Dir(home)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		return err
	}
	if !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("shared agent home parent is not a plain directory")
	}
	if err := os.Mkdir(home, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(home)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("shared agent home is not a plain directory")
	}
	for _, relative := range relativeDirs {
		fd, err := openSharedHomeDirectory(home, relative, true)
		if err != nil {
			return fmt.Errorf("prepare %q: %w", relative, err)
		}
		unix.Close(fd)
	}
	return nil
}

func installSharedAgentBinary(source, home, agentName string) error {
	if agentName == "" || filepath.Base(agentName) != agentName || strings.ContainsAny(agentName, `/\\`) {
		return errors.New("invalid shared-host agent name")
	}
	resolved, err := filepath.EvalSymlinks(source)
	if err != nil {
		return err
	}
	input, err := os.Open(resolved)
	if err != nil {
		return err
	}
	defer input.Close()
	var magic [4]byte
	if _, err := io.ReadFull(input, magic[:]); err != nil {
		return err
	}
	if string(magic[:]) != "\x7fELF" {
		return fmt.Errorf("%s is not a self-contained Linux executable; install a native agent binary", resolved)
	}
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		return err
	}

	dirFD, err := openSharedHomeDirectory(home, filepath.Join(".local", "bin"), false)
	if err != nil {
		return err
	}
	defer unix.Close(dirFD)
	temporaryName := ".agent-runtime-" + uuid.NewString()
	temporaryFD, err := unix.Openat(dirFD, temporaryName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer unix.Unlinkat(dirFD, temporaryName, 0)
	temporary := os.NewFile(uintptr(temporaryFD), temporaryName)
	if _, err := io.Copy(temporary, input); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Chmod(0755); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return unix.Renameat(dirFD, temporaryName, dirFD, agentName)
}

func openSharedHomeDirectory(home, relative string, create bool) (int, error) {
	parts := strings.FieldsFunc(filepath.Clean(relative), func(r rune) bool { return r == '/' })
	if filepath.IsAbs(relative) || len(parts) == 0 {
		return -1, errors.New("shared agent directory must be relative")
	}
	fd, err := unix.Open(home, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}
	for _, part := range parts {
		if part == "." || part == ".." || part == "" {
			unix.Close(fd)
			return -1, errors.New("invalid shared agent directory component")
		}
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if errors.Is(openErr, unix.ENOENT) && create {
			if mkdirErr := unix.Mkdirat(fd, part, 0700); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				unix.Close(fd)
				return -1, mkdirErr
			}
			next, openErr = unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		}
		unix.Close(fd)
		if openErr != nil {
			return -1, openErr
		}
		fd = next
	}
	return fd, nil
}
