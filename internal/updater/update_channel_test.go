package updater

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
)

func TestPreviewUpdateRefusesStableNamesSymlinksAndHardlinks(t *testing.T) {
	old := config.ReleaseChannel
	config.ReleaseChannel = "preview"
	t.Cleanup(func() { config.ReleaseChannel = old })
	dir := t.TempDir()
	stable := filepath.Join(dir, "wt")
	if err := os.WriteFile(stable, []byte("stable"), 0755); err != nil {
		t.Fatal(err)
	}
	preview := filepath.Join(dir, "wt-preview")
	if err := os.Symlink(stable, preview); err != nil {
		t.Fatal(err)
	}
	if err := validateChannelUpdatePath(preview); err == nil {
		t.Fatal("stable alias replacement accepted")
	}
	if err := os.Remove(preview); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(stable, preview); err != nil {
		t.Fatal(err)
	}
	if err := validateChannelUpdatePath(preview); err == nil {
		t.Fatal("stable hardlink replacement accepted")
	}
	if err := os.Remove(preview); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(preview, []byte("preview"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := validateChannelUpdatePath(preview); err != nil {
		t.Fatal(err)
	}
}

func TestPreviewReleaseSelectionNeverFallsBackToStable(t *testing.T) {
	old := config.ReleaseChannel
	config.ReleaseChannel = "preview"
	t.Cleanup(func() { config.ReleaseChannel = old })
	if strings.Contains(ReleaseMetadataURL(), "/latest") || !strings.HasPrefix(ReleaseAssetName(), "wt-preview-") {
		t.Fatal("stable update namespace reused")
	}
	fixture := `[{"tag_name":"v0.149.0"},{"tag_name":"v0.149.0-preview.1","draft":true,"prerelease":true},{"tag_name":"v0.148.0-preview.2","prerelease":true}]`
	release, err := DecodeChannelRelease(strings.NewReader(fixture))
	if err != nil || release.TagName != "v0.148.0-preview.2" {
		t.Fatalf("release selection: %v %v", release, err)
	}
	if _, err := DecodeChannelRelease(strings.NewReader(`[{"tag_name":"v0.149.0"}]`)); err == nil {
		t.Fatal("fell back to stable release")
	}
}
