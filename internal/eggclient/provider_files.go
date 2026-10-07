package eggclient

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

// Pin each directory from the home down to a provider file. No user-controlled
// directory or leaf link may redirect a host-side read or publication.
func openProviderDirectory(home, relative string, create bool) (*os.File, error) {
	if create {
		if err := os.MkdirAll(home, 0700); err != nil {
			return nil, err
		}
	}
	flags := unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	fd, err := unix.Open(home, flags, 0)
	if err != nil {
		return nil, err
	}
	homeDir := os.NewFile(uintptr(fd), home)
	defer homeDir.Close()
	return openProviderSubdirectory(homeDir, relative, create)
}

func openProviderSubdirectory(home *os.File, relative string, create bool) (*os.File, error) {
	fd, err := unix.FcntlInt(home.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	flags := unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	parts := strings.Split(filepath.Clean(relative), string(filepath.Separator))
	if filepath.IsAbs(relative) {
		unix.Close(fd)
		return nil, errors.New("provider directory must be relative to its home")
	}
	for _, part := range parts {
		if part == "." {
			continue
		}
		if part == ".." || part == "" {
			unix.Close(fd)
			return nil, errors.New("invalid provider directory")
		}
		next, err := unix.Openat(fd, part, flags, 0)
		if errors.Is(err, unix.ENOENT) && create {
			if mkdirErr := unix.Mkdirat(fd, part, 0700); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				unix.Close(fd)
				return nil, mkdirErr
			}
			next, err = unix.Openat(fd, part, flags, 0)
		}
		unix.Close(fd)
		if err != nil {
			return nil, err
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), filepath.Join(home.Name(), relative)), nil
}

func validateProviderFile(stat *unix.Stat_t) error {
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || int(stat.Uid) != os.Getuid() {
		return errors.New("provider file must be an account-owned regular file with one link")
	}
	return nil
}

func readProviderFile(dir *os.File, name string) ([]byte, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open provider file", Path: name, Err: err}
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, err
	}
	if err := validateProviderFile(&stat); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, err
	}
	if err := validateProviderFile(&stat); err != nil {
		return nil, err
	}
	return data, nil
}

func providerFileExists(dir *os.File, name string) (bool, error) {
	var stat unix.Stat_t
	if err := unix.Fstatat(int(dir.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return false, nil
		}
		return false, err
	}
	return true, validateProviderFile(&stat)
}

func writeProviderFile(dir *os.File, name string, data []byte, mode os.FileMode) error {
	if _, err := providerFileExists(dir, name); err != nil {
		return err
	}
	temporary := ".wt-provider-" + uuid.NewString()
	fd, err := unix.Openat(int(dir.Fd()), temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(mode.Perm()))
	if err != nil {
		return fmt.Errorf("stage provider file: %w", err)
	}
	defer unix.Unlinkat(int(dir.Fd()), temporary, 0)
	f := os.NewFile(uintptr(fd), temporary)
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Chmod(mode); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := unix.Renameat(int(dir.Fd()), temporary, int(dir.Fd()), name); err != nil {
		return err
	}
	return dir.Sync()
}
