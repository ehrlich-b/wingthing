package sandbox

import (
	"path/filepath"
	"strings"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/protectedfile"
)

// PhysicalDenyPaths also seals bind aliases visible in the host mount table.
// A missing or unreadable identity/mount table refuses the launch.
func PhysicalDenyPaths(paths []string) ([]string, error) {
	canonical := CanonicalDenyPaths(paths)
	var masks []string
	jail := false
	for _, path := range canonical {
		if path == "/" {
			jail = true
			continue
		}
		masks = append(masks, path)
	}
	aliases, err := protectedfile.Aliases(masks)
	if err != nil {
		return nil, err
	}
	if jail {
		aliases = append(aliases, "/")
	}
	return CanonicalDenyPaths(aliases), nil
}

// CanonicalDenyPaths preserves both a deny's declared name and its resolved
// target. A jail can turn a symlink into a separate bind mount, so masking only
// one name would leave the other readable or writable. Missing suffixes are
// resolved through their existing ancestors too. Denies inside an alias's
// source are also projected into its synthetic target before the jail is built.
func CanonicalDenyPaths(paths []string, aliases ...Mount) []string {
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
	canonical := result
	for _, alias := range aliases {
		if alias.Target == "" || alias.Target == alias.Source {
			continue
		}
		source := config.CanonicalProviderPath(alias.Source)
		for _, path := range canonical {
			if path == "/" {
				continue // jail marker, not a mask of the alias's contents
			}
			relative, err := filepath.Rel(source, path)
			if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				continue
			}
			name := filepath.Join(alias.Target, relative)
			if !seen[name] {
				seen[name] = true
				result = append(result, name)
			}
		}
	}
	return result
}
