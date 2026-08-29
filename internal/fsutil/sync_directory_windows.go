//go:build windows

package fsutil

import (
	"fmt"
	"os"
)

func SyncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open directory for sync: %w", err)
	}
	info, statErr := directory.Stat()
	closeErr := directory.Close()
	if statErr != nil {
		return fmt.Errorf("inspect directory for sync: %w", statErr)
	}
	if !info.IsDir() {
		return fmt.Errorf("sync directory: %s is not a directory", path)
	}
	if closeErr != nil {
		return fmt.Errorf("close directory after sync validation: %w", closeErr)
	}
	return nil
}
