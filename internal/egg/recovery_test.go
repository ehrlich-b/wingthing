package egg

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRecoveryExitDistinguishesShutdownFromDeliberateExit(t *testing.T) {
	for _, tc := range []struct {
		name               string
		code               int
		cancelled, stopped bool
	}{
		{"exit", 0, false, true}, {"shutdown", 0, true, false}, {"crash", -1, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := recordSessionProcessExit(dir, tc.code, tc.cancelled); err != nil {
				t.Fatal(err)
			}
			_, err := os.Stat(filepath.Join(dir, DeliberateStopFile))
			if (err == nil) != tc.stopped {
				t.Fatalf("stop marker: %v", err)
			}
		})
	}
}
