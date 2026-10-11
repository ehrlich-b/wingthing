package sandbox

import "path/filepath"

// CanonicalPath is the native profile builder's path identity, including missing
// leaves below a symlinked ancestor. It reads metadata and never creates paths.
func CanonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(real), nil
	}

	current := abs
	var suffix []string
	for {
		parent := filepath.Dir(current)
		if parent == current {
			return abs, nil
		}
		suffix = append([]string{filepath.Base(current)}, suffix...)
		current = parent
		if real, err := filepath.EvalSymlinks(current); err == nil {
			parts := append([]string{filepath.Clean(real)}, suffix...)
			return filepath.Join(parts...), nil
		}
	}
}
