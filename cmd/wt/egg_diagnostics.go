package main

import (
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"

	"github.com/ehrlich-b/wingthing/internal/daemonctl"
)

const failedEggLogBytes = 64 << 10

// Preserve a bounded startup tail before reaping an abandoned child. No
// environment, credential files or provider transcript is read here.
func preserveEggFailure(dir string, failure error) (string, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	var tail []byte
	file, err := root.OpenFile("egg.log", os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err == nil {
		info, statErr := file.Stat()
		if statErr != nil || !info.Mode().IsRegular() {
			_ = file.Close()
			return "", errors.New("egg diagnostic log is not a regular file")
		}
		if info.Size() > failedEggLogBytes {
			_, err = file.Seek(-failedEggLogBytes, io.SeekEnd)
		}
		if err == nil {
			tail, err = io.ReadAll(io.LimitReader(file, failedEggLogBytes))
		}
		_ = file.Close()
		if err != nil {
			return "", err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	reason := fmt.Sprint(failure)
	if len(reason) > 4096 {
		reason = reason[:4096]
	}
	tail = append(tail, []byte("\nEgg startup failure: "+reason+"\n")...)
	path := filepath.Join(dir, "egg.failed.log")
	if err := daemonctl.WriteAtomicMetadataFile(path, tail, 0600); err != nil {
		return "", err
	}
	return path, nil
}
