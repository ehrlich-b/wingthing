package updater

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/downloads"
	"github.com/ehrlich-b/wingthing/internal/testsocket"
)

func TestPrepareUpdateBindsAdvertisedVersion(t *testing.T) {
	for _, channel := range []string{"stable", "preview"} {
		t.Run(channel, func(t *testing.T) {
			oldChannel, oldTransport := config.ReleaseChannel, http.DefaultTransport
			config.ReleaseChannel = channel
			t.Cleanup(func() { config.ReleaseChannel = oldChannel; http.DefaultTransport = oldTransport })
			t.Setenv("WT_DOWNLOAD_BASE", "https://mirror.invalid/dl")
			current, advertised := "v0.149.4", "v0.149.5"
			if channel == "preview" {
				current, advertised = current+"-preview.1", advertised+"-preview.2"
			}
			for _, tc := range []struct {
				name, binaryName, embedded, identityChannel string
				accepted                                    bool
			}{
				{"matching", config.BinaryName(), advertised, channel, true},
				{"older", config.BinaryName(), current, channel, false},
				{"newer", config.BinaryName(), "v0.150.0", channel, false},
				{"tag-prefix", config.BinaryName(), advertised + "0", channel, false},
				{"tag-suffix", config.BinaryName(), advertised + ".dirty", channel, false},
				{"extra-output", config.BinaryName(), advertised + "\nextra", channel, false},
				{"wrong-executable", "other-wt", advertised, channel, false},
				{"opposite-channel", "wt-preview", "v0.149.5-preview.2", "preview", channel == "preview"},
				{"stable-channel", "wt", "v0.149.5", "stable", channel == "stable"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					binary := []byte(fmt.Sprintf(`#!/bin/sh
case "$*" in
 '--version') printf '%%s\n' '%s version %s';;
 '--expected-channel preview channel --json') echo '{"release_channel":"%s","executable":"%s"}';;
 'mcp connect --help') echo connect;;
 'serve --help'|'roost start --help') echo --https;;
 'local-cert status --help') echo status;;
 *) exit 1;;
esac
`, tc.binaryName, tc.embedded, tc.identityChannel, tc.binaryName))
					requests := []string{}
					http.DefaultTransport = mirrorTransport(func(r *http.Request) (*http.Response, error) {
						requests = append(requests, r.URL.String())
						var body string
						switch r.URL.String() {
						case ReleaseMetadataURL(), downloads.GitHubMetadataURL():
							body = fmt.Sprintf(`{"tag_name":%q}`, advertised)
							if channel == "preview" {
								body = fmt.Sprintf(`[{"tag_name":%q,"prerelease":true}]`, advertised)
							}
						case "https://mirror.invalid/dl/" + advertised + "/SHA256SUMS", downloads.GitHubBase() + "/" + advertised + "/SHA256SUMS":
							// The checksum is correct even for the wrong version.
							body = fmt.Sprintf("%x  %s\n", sha256.Sum256(binary), ReleaseAssetName())
						case "https://mirror.invalid/dl/" + advertised + "/" + ReleaseAssetName(), downloads.GitHubBase() + "/" + advertised + "/" + ReleaseAssetName():
							body = string(binary)
						default:
							t.Fatalf("unexpected release request: %s", r.URL)
						}
						return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
					})
					dir := testsocket.Dir(t)
					installed := filepath.Join(dir, config.BinaryName())
					if err := os.WriteFile(installed, []byte("installed binary"), 0755); err != nil {
						t.Fatal(err)
					}
					rel, path, err := PrepareUpdate(context.Background(), current, dir)
					if tc.accepted {
						if err != nil || path == "" || rel.TagName != advertised {
							t.Fatalf("matching bundle: release=%+v path=%q err=%v", rel, path, err)
						}
						if got, err := os.ReadFile(path); err != nil || string(got) != string(binary) {
							t.Fatalf("prepared binary = %q, %v", got, err)
						}
						if err := os.Remove(path); err != nil {
							t.Fatal(err)
						}
					} else if err != unavailableRelease() || path != "" {
						t.Errorf("mismatched bundle accepted: advertised=%q embedded=%q path=%q err=%v", advertised, tc.embedded, path, err)
					}
					if got, err := os.ReadFile(installed); err != nil || string(got) != "installed binary" {
						t.Fatalf("installed binary changed: %q, %v", got, err)
					}
					if files, err := os.ReadDir(dir); err != nil || len(files) != 1 {
						t.Errorf("temporary download leaked: %v, %v", files, err)
					}
					wantRequests := 3
					if !tc.accepted && channel == "stable" {
						wantRequests = 6
					}
					if len(requests) != wantRequests {
						t.Fatalf("bundle requests = %v", requests)
					}
				})
			}
		})
	}
}
