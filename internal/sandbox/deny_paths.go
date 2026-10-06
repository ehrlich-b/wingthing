package sandbox

import (
	"path/filepath"

	"github.com/ehrlich-b/wingthing/internal/config"
)

// CanonicalDenyPaths preserves both a deny's declared name and its resolved
// target. A jail can turn a symlink into a separate bind mount, so masking only
// one name would leave the other readable or writable. Missing suffixes are
// resolved through their existing ancestors too.
func CanonicalDenyPaths(paths []string) []string {
	var result []string
	seen := make(map[string]bool)
	for _, path := range paths {
		if absolute, err := filepath.Abs(path); err == nil {
			path = absolute
		}
		for _, name := range []string{filepath.Clean(path), config.CanonicalProviderPath(path)} {
			if !seen[name] {
				seen[name] = true
				result = append(result, name)
			}
		}
	}
	return result
}
