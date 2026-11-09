package testsocket

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeDirUsesIsolatedPhysicalRuntimeRoot(t *testing.T) {
	base := RuntimeDir(t, "")
	t.Setenv("WT_PROVIDER_GUARD_FIXTURE_ROOT", "")
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

func TestRuntimeDirWithoutXDG(t *testing.T) {
	base := RuntimeDir(t, "")
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("WT_PROVIDER_GUARD_FIXTURE_ROOT", base)
	if dir := RuntimeDir(t, ""); filepath.Dir(dir) != base {
		t.Fatalf("container fixture root ignored without XDG_RUNTIME_DIR: %s", dir)
	}

	// Standalone callers need no runtime environment. Their private home is
	// still validated against both temporary write grants before use.
	t.Setenv("WT_PROVIDER_GUARD_FIXTURE_ROOT", "")
	t.Setenv("HOME", base)
	t.Setenv("USERPROFILE", base)
	if dir := RuntimeDir(t, ""); filepath.Dir(dir) != base {
		t.Fatalf("standalone home fallback ignored: %s", dir)
	}
	t.Setenv("HOME", "/tmp")
	t.Setenv("USERPROFILE", "/tmp")
	if _, err := runtimeBase(""); err == nil {
		t.Fatal("accepted home fallback inside temporary write grant")
	}
}
