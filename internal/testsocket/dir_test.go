package testsocket

import (
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestDirUsesShortBaseWhenTempDirIsLong(t *testing.T) {
	longTemp := filepath.Join(Dir(t), strings.Repeat("x", 80))
	if err := os.MkdirAll(longTemp, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", longTemp)
	var dir string
	t.Run("fixture", func(t *testing.T) {
		dir = Dir(t)
		if len(filepath.Join(dir, "agent.sock")) > 103 {
			t.Fatalf("socket path is too long: %s", dir)
		}
		info, err := os.Stat(dir)
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatalf("directory is not private: %v %v", info, err)
		}
	})
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("directory was not removed: %v", err)
	}
}

// Even a short TMPDIR must not select a different fixture root or prefix.
func TestDirIgnoresShortTempDir(t *testing.T) {
	ambient := Dir(t)
	t.Setenv("TMPDIR", ambient)
	physicalTmp, err := filepath.EvalSymlinks("/tmp")
	if err != nil {
		t.Fatal(err)
	}
	var first string
	t.Run("fixture", func(t *testing.T) {
		first = Dir(t)
		second := Dir(t)
		for _, dir := range []string{first, second} {
			if filepath.Dir(dir) != physicalTmp || !regexp.MustCompile(`^wt[0-9]+$`).MatchString(filepath.Base(dir)) {
				t.Fatalf("fixture root depends on ambient paths or uses the wrong prefix: %s", dir)
			}
			listener, err := net.Listen("unix", filepath.Join(dir, "fixture.sock"))
			if err != nil {
				t.Fatal(err)
			}
			if err := listener.Close(); err != nil {
				t.Fatal(err)
			}
		}
		if first == second {
			t.Fatal("fixture roots are shared")
		}
	})
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatalf("fixture root survived cleanup: %v", err)
	}
}
