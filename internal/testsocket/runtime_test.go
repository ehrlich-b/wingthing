package testsocket

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeDirUsesIsolatedPhysicalRuntimeRoot(t *testing.T) {
	base := RuntimeDir(t, "")
	t.Setenv("XDG_RUNTIME_DIR", base)
	var dir string
	t.Run("owned", func(t *testing.T) {
		dir = RuntimeDir(t, "")
		if filepath.Dir(dir) != base {
			t.Fatalf("fixture escaped selected runtime root: %s", dir)
		}
		if info, err := os.Lstat(dir); err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
			t.Fatalf("runtime fixture is not private and physical: %v %v", info, err)
		}
		t.Setenv("XDG_RUNTIME_DIR", "/tmp")
		if explicit := RuntimeDir(t, base); filepath.Dir(explicit) != base {
			t.Fatalf("explicit fixture root ignored: %s", explicit)
		}
	})
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("runtime fixture was not removed: %v", err)
	}
	for _, invalid := range []string{"/tmp", Dir(t), os.TempDir(), "relative"} {
		if _, err := runtimeBase(invalid); err == nil {
			t.Fatalf("accepted unprotected fixture root %q", invalid)
		}
	}
}
