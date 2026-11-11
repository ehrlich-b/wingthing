package downloads

import (
	"strings"
	"testing"
)

func TestDownloadEndpointsMirrorAndGitHub(t *testing.T) {
	t.Setenv("WT_DOWNLOAD_BASE", "")
	t.Setenv("WT_GITHUB_METADATA_URL", "")
	t.Setenv("WT_GITHUB_DOWNLOAD_BASE", "")
	if Base() != "https://wingthing.ai/dl" || GitHubMetadataURL() != "https://api.github.com/repos/ehrlich-b/wingthing/releases/latest" || GitHubBase() != "https://github.com/ehrlich-b/wingthing/releases/download" {
		t.Fatal("unexpected release endpoints")
	}
	for _, endpoint := range []string{Base(), GitHubMetadataURL(), GitHubBase()} {
		if !strings.Contains(Installer(), endpoint) {
			t.Errorf("installer missing %s", endpoint)
		}
	}
	for _, setting := range []string{"WT_GITHUB_METADATA_URL", "WT_GITHUB_DOWNLOAD_BASE"} {
		t.Run(setting, func(t *testing.T) {
			for _, value := range []string{"http://example.test", "https://user:secret@example.test", "https://example.test/?query=1", "https://example.test/$(unsafe)"} {
				t.Setenv(setting, value)
				got := GitHubBase()
				if setting == "WT_GITHUB_METADATA_URL" {
					got = GitHubMetadataURL()
				}
				if got != "" {
					t.Fatalf("unsafe override accepted: %s", value)
				}
			}
		})
	}
}
