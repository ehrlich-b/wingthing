package wingpolicy

import (
	"path/filepath"
)

func SessionPolicyContains(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if filepath.IsAbs(root) && filepath.Dir(root) == root {
		return filepath.IsAbs(path) && filepath.VolumeName(path) == filepath.VolumeName(root)
	}
	return IsUnderPaths(path, []string{root})
}

func CanonicalPolicyPath(path string) string {
	path = filepath.Clean(path)
	current := path
	var suffix []string
	for {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			for index := len(suffix) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, suffix[index])
			}
			return filepath.Clean(resolved)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return path
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}
