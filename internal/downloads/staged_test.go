package downloads

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/testsocket"
)

var fixtureTags = []string{"v0.147.0", "v0.149.5"}

func stagedFixture(t *testing.T) string {
	t.Helper()
	root := testsocket.Dir(t)
	for _, tag := range fixtureTags {
		dir := filepath.Join(root, tag)
		if err := os.Mkdir(dir, 0755); err != nil {
			t.Fatal(err)
		}
		var manifest strings.Builder
		for _, name := range deployAssets {
			data := []byte("fixture " + tag + " " + name)
			if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&manifest, "%x  %s\n", sha256.Sum256(data), name)
		}
		if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(manifest.String()), 0644); err != nil {
			t.Fatal(err)
		}
	}
	tag := fixtureTags[len(fixtureTags)-1]
	assets := []map[string]string{}
	for _, name := range append(append([]string{}, deployAssets...), "SHA256SUMS") {
		assets = append(assets, map[string]string{"name": name, "browser_download_url": strings.TrimSpace(defaultBase) + "/" + tag + "/" + name})
	}
	feed, _ := json.Marshal(map[string]any{"tag_name": tag, "draft": false, "prerelease": false, "assets": assets})
	if err := os.WriteFile(filepath.Join(root, "latest.json"), feed, 0644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestStagedCompleteRelease(t *testing.T) {
	// Ambient client overrides cannot change the production deployment contract.
	t.Setenv("WT_DOWNLOAD_BASE", "https://override.invalid/dl")
	if err := CheckStaged(stagedFixture(t)); err != nil {
		t.Fatal(err)
	}
}

func TestStagedRejectsIncompleteAndCorruptReleases(t *testing.T) {
	latest := fixtureTags[len(fixtureTags)-1]
	cases := []struct {
		name, path, contents string
		remove               bool
	}{
		{"missing-feed", "latest.json", "", true},
		{"malformed-feed", "latest.json", "{", false},
		{"old-tag", "latest.json", "v0.147.0", false},
		{"draft", "latest.json", "draft", false},
		{"preview", "latest.json", "prerelease", false},
		{"missing-stable-flag", "latest.json", "missing-flag", false},
		{"wrong-assets", "latest.json", "wrong-assets", false},
		{"trailing-feed", "latest.json", "trailing", false},
		{"missing-manifest", latest + "/SHA256SUMS", "", true},
		{"missing-binary", latest + "/wt-linux-arm64", "", true},
		{"corrupt-binary", latest + "/wt-darwin-amd64", "corrupt", false},
		{"empty-binary", latest + "/wt-darwin-arm64", "", false},
		{"bad-manifest", latest + "/SHA256SUMS", "bad", false},
		{"duplicate-entry", latest + "/SHA256SUMS", "duplicate", false},
		{"incomplete-manifest", latest + "/SHA256SUMS", "incomplete", false},
		{"missing-historical-release", fixtureTags[0] + "/wt-linux-amd64", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := stagedFixture(t)
			path := filepath.Join(root, tc.path)
			if tc.remove {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			} else {
				data := []byte(tc.contents)
				original, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if tc.path == "latest.json" && tc.contents != "{" {
					var feed map[string]any
					if err := json.Unmarshal(original, &feed); err != nil {
						t.Fatal(err)
					}
					switch tc.contents {
					case "v0.147.0":
						feed["tag_name"] = tc.contents
					case "draft", "prerelease":
						feed[tc.contents] = true
					case "missing-flag":
						delete(feed, "draft")
					case "wrong-assets":
						feed["assets"] = []any{}
					}
					data, _ = json.Marshal(feed)
					if tc.contents == "trailing" {
						data = append(data, []byte(" {}")...)
					}
				} else if tc.contents == "duplicate" {
					data = append(original, strings.SplitAfter(string(original), "\n")[0]...)
				} else if tc.contents == "incomplete" {
					data = []byte(strings.SplitAfter(string(original), "\n")[0])
				}
				if err := os.WriteFile(path, data, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if err := CheckStaged(root); err == nil {
				t.Fatal("invalid staged release accepted")
			}
		})
	}
}

func TestStagedRefusesSymlinks(t *testing.T) {
	for _, name := range []string{".", "latest.json", fixtureTags[0], fixtureTags[1] + "/SHA256SUMS", fixtureTags[1] + "/wt-linux-amd64"} {
		t.Run(name, func(t *testing.T) {
			root := stagedFixture(t)
			path := filepath.Join(root, name)
			saved := filepath.Join(testsocket.Dir(t), "saved")
			if err := os.Rename(path, saved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(saved, path); err != nil {
				t.Fatal(err)
			}
			if err := CheckStaged(root); err == nil {
				t.Fatal("symlink accepted")
			}
		})
	}
}

func TestStagedNextReleaseAndBinaryChecksums(t *testing.T) {
	root := stagedFixture(t)
	old := fixtureTags[1]
	next := "v12.345.678"
	if err := os.Rename(filepath.Join(root, old), filepath.Join(root, next)); err != nil {
		t.Fatal(err)
	}
	feed, err := os.ReadFile(filepath.Join(root, "latest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "latest.json"), []byte(strings.ReplaceAll(string(feed), old, next)), 0644); err != nil {
		t.Fatal(err)
	}
	for _, tag := range []string{fixtureTags[0], next} {
		path := filepath.Join(root, tag, "SHA256SUMS")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(data), "  wt-", " *wt-")), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := CheckStaged(root); err != nil {
		t.Fatal(err)
	}
}
