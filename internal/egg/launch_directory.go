package egg

import (
	"fmt"
	"os"
	"path/filepath"
)

// CheckLaunchDirectory checks existing components of a directory that launch
// prepares with MkdirAll. Missing components are allowed; nothing is created.
func CheckLaunchDirectory(path string) error {
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		info, err := os.Stat(current)
		if err == nil {
			if !info.IsDir() {
				return fmt.Errorf("required directory component is not a directory: %s", current)
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect required directory %s: %w", current, err)
		}
		if filepath.Dir(current) == current {
			return nil
		}
	}
}

func prepareLaunchDirectory(path string, prepare bool) error {
	if !prepare {
		return CheckLaunchDirectory(path)
	}
	return os.MkdirAll(path, 0700)
}
