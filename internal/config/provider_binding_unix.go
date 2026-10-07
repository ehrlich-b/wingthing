//go:build darwin || linux

package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"

	"github.com/ehrlich-b/wingthing/internal/protectedfile"
)

// The binding is opened without following a final symlink and without
// blocking on a FIFO swapped in after Lstat. The opened file must be the same
// private, single-link regular file owned by this OS account.
func readProviderHomeBinding(path string, limit int64) ([]byte, error) {
	f, err := protectedfile.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info := f.Info
	if info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("must not be accessible to group or others (use mode 0400 or 0600)")
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("must not exceed %d bytes", limit)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("must not exceed %d bytes", limit)
	}
	return data, nil
}

// A dangling link is not a missing path: creating the protected path would
// create its unknown target, so its identity cannot be verified.
func statProviderPathIdentity(path string) (providerPathID, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		if _, lerr := os.Lstat(path); lerr == nil {
			return providerPathID{}, errors.New("is a link whose target does not exist")
		}
		return providerPathID{}, err
	} else if err != nil {
		return providerPathID{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return providerPathID{}, errors.New("has no device and inode identity")
	}
	return providerPathID{dev: uint64(stat.Dev), ino: uint64(stat.Ino)}, nil
}

func validateProviderHomeDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return errors.New("must name an existing directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() {
		return errors.New("must name a directory owned by this OS account")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return errors.New("must name a directory not writable by group or others")
	}
	return nil
}
