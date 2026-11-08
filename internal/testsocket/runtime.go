package testsocket

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// RuntimeDir creates disposable protected state outside the sandbox's writable
// temporary trees. An explicit fixture mount takes precedence over the caller's
// XDG_RUNTIME_DIR. Ordinary socket fixtures should continue to use Dir.
func RuntimeDir(t testing.TB, base string) string {
	t.Helper()
	base, err := runtimeBase(base)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(base, "wt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func runtimeBase(base string) (string, error) {
	if base == "" {
		base = os.Getenv("XDG_RUNTIME_DIR")
	}
	if !filepath.IsAbs(base) {
		return "", fmt.Errorf("protected fixtures require an absolute runtime directory (set XDG_RUNTIME_DIR or the fixture root)")
	}
	base, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", err
	}
	for _, tmp := range []string{"/tmp", os.TempDir()} {
		tmp, err = filepath.EvalSymlinks(tmp)
		if err != nil {
			return "", err
		}
		rel, err := filepath.Rel(tmp, base)
		if err != nil {
			return "", err
		}
		if rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("protected fixture root %q is inside temporary write directory %q", base, tmp)
		}
	}
	return base, nil
}
