package testsocket

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDirUsesShortBaseWhenTempDirIsLong(t *testing.T) {
	longTemp := filepath.Join(t.TempDir(), strings.Repeat("x", 80))
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
