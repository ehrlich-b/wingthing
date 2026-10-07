package wingpolicy

import (
	"path/filepath"
)

func CanonicalPaths(paths []string) []string {
	canonical := make([]string, 0, len(paths))
	for _, path := range paths {
		canonical = append(canonical, CanonicalSessionPath(path))
	}
	return canonical
}

func CanonicalSessionPath(path string) string {
	cleaned := filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil {
		cleaned = resolved
	}
	return canonicalPathCase(cleaned)
}
