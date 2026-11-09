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
	"github.com/ehrlich-b/wingthing/internal/testsocket"
)

type mirrorTransport func(*http.Request) (*http.Response, error)

func (f mirrorTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestMirrorUpdateCompleteBundleWithoutFallback(t *testing.T) {
	good := []byte("#!/bin/sh\ncase \"$*\" in\n '--version') echo 'wt version v0.149.5';;\n 'mcp connect --help') echo connect;;\n 'serve --help'|'roost start --help') echo --https;;\n 'local-cert status --help') echo status;;\n *) exit 1;;\nesac\n")
	for _, mode := range []string{"healthy", "metadata404", "invalidTag", "binary404", "checksumMismatch", "badContract", "missingManifest", "both404"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("WT_DOWNLOAD_BASE", "https://mirror.invalid/dl")
			old := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = old })
			calls := []string{}
			http.DefaultTransport = mirrorTransport(func(r *http.Request) (*http.Response, error) {
				calls = append(calls, r.URL.String())
				mirror := r.URL.Host == "mirror.invalid"
				status := 200
				body := ""
				switch {
				case strings.HasSuffix(r.URL.Path, "latest.json") || strings.HasSuffix(r.URL.Path, "/latest"):
					body = `{"tag_name":"v0.149.5","assets":[{"name":"` + ReleaseAssetName() + `","browser_download_url":"https://github.invalid/binary"},{"name":"SHA256SUMS","browser_download_url":"https://github.invalid/SHA256SUMS"}]}`
					if mirror && mode == "metadata404" || mode == "both404" {
						status = 404
					}
					if mirror && mode == "invalidTag" {
						body = `{"tag_name":"../../invalid"}`
					}
				case strings.HasSuffix(r.URL.Path, "SHA256SUMS"):
					checksum := fmt.Sprintf("%x", sha256.Sum256(good))
					if mirror && mode == "checksumMismatch" {
						checksum = strings.Repeat("0", 64)
					}
					if mirror && mode == "badContract" {
						checksum = fmt.Sprintf("%x", sha256.Sum256([]byte("#!/bin/sh\nexit 1\n")))
					}
					body = checksum + "  " + ReleaseAssetName() + "\n"
					if mirror && mode == "missingManifest" {
						status = 404
					}
				default:
					body = string(good)
					if mirror && mode == "binary404" {
						status = 404
					}
					if mirror && mode == "badContract" {
						body = "#!/bin/sh\nexit 1\n"
					}
				}
				return &http.Response{StatusCode: status, Status: fmt.Sprint(status), Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})
			dir := testsocket.Dir(t)
			sentinel := filepath.Join(dir, "wt")
			if err := os.WriteFile(sentinel, []byte("existing"), 0755); err != nil {
				t.Fatal(err)
			}
			rel, path, err := PrepareUpdate(context.Background(), "v0.149.4", dir)
			if mode != "healthy" {
				if err != ErrReleaseUnavailable || path != "" {
					t.Fatalf("path=%q err=%v", path, err)
				}
			} else {
				if err != nil || rel.TagName != "v0.149.5" {
					t.Fatalf("rel=%+v err=%v", rel, err)
				}
				content, e := os.ReadFile(path)
				if e != nil || string(content) != string(good) {
					t.Fatalf("download=%q err=%v", content, e)
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			original, _ := os.ReadFile(sentinel)
			if string(original) != "existing" {
				t.Fatal("existing binary touched")
			}
			files, _ := os.ReadDir(dir)
			if len(files) != 1 {
				t.Fatalf("temporary download leaked: %v", files)
			}
			fallback := false
			for _, call := range calls {
				if !strings.HasPrefix(call, "https://mirror.invalid/dl/") {
					fallback = true
				}
			}
			if fallback {
				t.Fatalf("fallback=%t calls=%v", fallback, calls)
			}
		})
	}
}

func TestMirrorLatestChannelIsolation(t *testing.T) {
	oldChannel := config.ReleaseChannel
	oldTransport := http.DefaultTransport
	t.Cleanup(func() { config.ReleaseChannel = oldChannel; http.DefaultTransport = oldTransport })
	t.Setenv("WT_DOWNLOAD_BASE", "https://mirror.invalid/dl")
	for _, channel := range []string{"stable", "preview"} {
		config.ReleaseChannel = channel
		calls := []string{}
		http.DefaultTransport = mirrorTransport(func(r *http.Request) (*http.Response, error) {
			calls = append(calls, r.URL.String())
			body := `{"tag_name":"v0.149.5"}`
			if channel == "preview" {
				body = `[{"tag_name":"v0.149.5"},{"tag_name":"v0.149.5-preview.1","prerelease":true}]`
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		rel, err := LatestRelease(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if channel == "preview" && (!strings.Contains(rel.TagName, "-preview.") || !strings.HasSuffix(calls[0], "/preview.json")) {
			t.Fatalf("preview selected %+v from %v", rel, calls)
		}
		if channel == "stable" && (strings.Contains(rel.TagName, "-preview.") || !strings.HasSuffix(calls[0], "/latest.json")) {
			t.Fatalf("stable selected %+v from %v", rel, calls)
		}
	}
}

func TestMirrorPreviewMissingFeedNeverFallsBack(t *testing.T) {
	oldChannel, oldTransport := config.ReleaseChannel, http.DefaultTransport
	t.Cleanup(func() { config.ReleaseChannel = oldChannel; http.DefaultTransport = oldTransport })
	config.ReleaseChannel = "preview"
	t.Setenv("WT_DOWNLOAD_BASE", "https://mirror.invalid/dl")
	calls := 0
	http.DefaultTransport = mirrorTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != "https://mirror.invalid/dl/preview.json" {
			t.Fatalf("unexpected request: %s", r.URL)
		}
		return &http.Response{StatusCode: 404, Status: "404 Not Found", Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	if _, err := LatestRelease(context.Background()); err != ErrPreviewUnavailable {
		t.Fatalf("missing preview feed: %v", err)
	}
	if _, path, err := PrepareUpdate(context.Background(), "v0.149.4-preview.1", testsocket.Dir(t)); err != ErrPreviewUnavailable || path != "" {
		t.Fatalf("path=%q err=%v", path, err)
	}
	if calls != 2 || !strings.Contains(ErrPreviewUnavailable.Error(), "/dl/preview.json") || !strings.Contains(ErrPreviewUnavailable.Error(), "update --file") {
		t.Fatalf("calls=%d error=%v", calls, ErrPreviewUnavailable)
	}
}

func TestMirrorUpdateNeverDowngradesNewerRelease(t *testing.T) {
	t.Setenv("WT_DOWNLOAD_BASE", "https://mirror.invalid/dl")
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	requests := []string{}
	http.DefaultTransport = mirrorTransport(func(r *http.Request) (*http.Response, error) {
		requests = append(requests, r.URL.String())
		if !strings.HasSuffix(r.URL.Path, "latest.json") && !strings.HasSuffix(r.URL.Path, "/latest") {
			t.Fatal("older release assets were fetched")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"tag_name":"v0.149.5"}`))}, nil
	})
	dir := testsocket.Dir(t)
	_, path, err := PrepareUpdate(context.Background(), "v0.150.0", dir)
	if err != ErrReleaseUnavailable || path != "" || len(requests) != 1 {
		t.Fatalf("path=%q err=%v requests=%v", path, err, requests)
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 0 {
		t.Fatal("older download left a file")
	}
}
