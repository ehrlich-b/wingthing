package wingpolicy

import (
	"path/filepath"
	"runtime"
)

// WritablePolicyRoot returns the most specific writable root covering path,
// after applying the compiled session filesystem policy's read-only and deny
// rules. The rule paths must already be resolved and canonicalized.
func WritablePolicyRoot(path string, writableRoots, readOnlyRoots, deny, denyWrite []string) (string, bool) {
	path = CanonicalPolicyPath(path)
	best := ""
	for _, root := range writableRoots {
		if SessionPolicyContains(root, path) && len(root) > len(best) {
			best = root
		}
	}
	if best == "" {
		return "", false
	}
	for _, root := range readOnlyRoots {
		if SessionPolicyContains(root, path) && len(root) >= len(best) {
			return "", false
		}
	}
	for _, rule := range deny {
		// deny:/ selects the Linux mount jail, which mounts explicit rw/ro
		// roots back in. On other platforms it is an effective deny.
		if runtime.GOOS == "linux" && filepath.Clean(rule) == string(filepath.Separator) {
			continue
		}
		if SessionPolicyContains(rule, path) {
			return "", false
		}
	}
	for _, rule := range denyWrite {
		if SessionPolicyContains(rule, path) {
			return "", false
		}
	}
	return best, true
}

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
