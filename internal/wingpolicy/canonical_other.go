//go:build !darwin

package wingpolicy

func canonicalPathCase(path string) string {
	return path
}
