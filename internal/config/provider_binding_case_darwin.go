//go:build darwin

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// macOS volumes are usually case- and normalization-insensitive, and
// EvalSymlinks keeps whatever spelling the caller wrote. Binding validation
// therefore folds case for overlap and requires each component's on-disk name.
const providerHomeFoldsCase = true

func requireExactProviderHomeSpelling(path string) error {
	parent := string(filepath.Separator)
	for _, name := range strings.Split(strings.TrimPrefix(path, parent), string(filepath.Separator)) {
		dir, err := os.Open(parent)
		if err != nil {
			return fmt.Errorf("cannot verify the exact spelling of %q: %w", path, err)
		}
		names, err := dir.Readdirnames(-1)
		dir.Close()
		if err != nil {
			return fmt.Errorf("cannot verify the exact spelling of %q: %w", path, err)
		}
		found := false
		for _, entry := range names {
			if entry == name {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("must use the exact on-disk spelling of %q, not a case or normalization alias", filepath.Join(parent, name))
		}
		parent = filepath.Join(parent, name)
	}
	return nil
}
