package egg

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
)

func TestPreviewBrowserShimsRemainInspectableRegularState(t *testing.T) {
	old := config.ReleaseChannel
	config.ReleaseChannel = "preview"
	t.Cleanup(func() { config.ReleaseChannel = old })
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".release-channel"), []byte("preview\n"), 0600); err != nil {
		t.Fatal(err)
	}
	shims := filepath.Join(dir, "eggs", "fixture", "shims")
	if err := os.MkdirAll(shims, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"open", "xdg-open"} {
		if err := installBrowserShimAlias(shims, name, "#!/bin/sh\nexit 0\n"); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(filepath.Join(shims, name))
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			t.Fatal("shim is not an executable regular file", err)
		}
	}
	if err := config.ValidateStateDirectory(dir); err != nil {
		t.Fatal("Wingthing generated uninspectable state", err)
	}
}
