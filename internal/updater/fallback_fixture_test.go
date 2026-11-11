package updater

import (
	"context"
	"crypto/sha256"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Real TLS fixtures exercise both clients without curl stubs or public traffic.
func TestReleaseFallbackLocalServers(t *testing.T) {
	good := []byte("#!/bin/sh\ncase \"$*\" in\n '--version') echo 'wt version v0.150.0';;\n 'mcp connect --help') echo connect;;\n 'serve --help'|'roost start --help') echo --https;;\n 'local-cert status --help') echo status;;\n *) exit 1;;\nesac\n")
	for _, mode := range []string{"mirror-up", "mirror-down", "binary-missing", "checksum-mismatch", "bad-contract", "both-down"} {
		for _, client := range []string{"installer", "self-update"} {
			t.Run(mode+"/"+client, func(t *testing.T) {
				var mu sync.Mutex
				var calls []string
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					calls = append(calls, r.URL.Path)
					mu.Unlock()
					mirror := strings.HasPrefix(r.URL.Path, "/dl/")
					if mode == "both-down" || mirror && mode == "mirror-down" || mirror && mode == "binary-missing" && strings.HasSuffix(r.URL.Path, ReleaseAssetName()) {
						http.NotFound(w, r)
						return
					}
					binary := good
					if mirror && mode == "bad-contract" {
						binary = []byte("#!/bin/sh\nexit 1\n")
					}
					switch r.URL.Path {
					case "/dl/latest.json", "/repos/ehrlich-b/wingthing/releases/latest":
						fmt.Fprint(w, `{"tag_name":"v0.150.0","draft":false,"prerelease":false}`)
					case "/dl/v0.150.0/SHA256SUMS", "/releases/download/v0.150.0/SHA256SUMS":
						digest := fmt.Sprintf("%x", sha256.Sum256(binary))
						if mirror && mode == "checksum-mismatch" {
							digest = strings.Repeat("0", 64)
						}
						fmt.Fprintf(w, "%s  %s\n", digest, ReleaseAssetName())
					case "/dl/v0.150.0/" + ReleaseAssetName(), "/releases/download/v0.150.0/" + ReleaseAssetName():
						_, _ = w.Write(binary)
					default:
						t.Errorf("unexpected request: %s", r.URL.Path)
						http.NotFound(w, r)
					}
				}))
				defer server.Close()
				t.Setenv("WT_DOWNLOAD_BASE", server.URL+"/dl")
				t.Setenv("WT_GITHUB_METADATA_URL", server.URL+"/repos/ehrlich-b/wingthing/releases/latest")
				t.Setenv("WT_GITHUB_DOWNLOAD_BASE", server.URL+"/releases/download")
				root := t.TempDir()
				installed := filepath.Join(root, "wt")
				if err := os.WriteFile(installed, []byte("existing"), 0755); err != nil {
					t.Fatal(err)
				}
				if client == "installer" {
					cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
					certPath := filepath.Join(root, "ca.pem")
					if err := os.WriteFile(certPath, cert, 0600); err != nil {
						t.Fatal(err)
					}
					cmd := exec.Command("sh", "../../scripts/install.sh")
					cmd.Env = append(os.Environ(), "WT_INSTALL_DIR="+root, "CURL_CA_BUNDLE="+certPath, "NO_PROXY=127.0.0.1,localhost")
					output, err := cmd.CombinedOutput()
					data, readErr := os.ReadFile(installed)
					if readErr != nil {
						t.Fatal(readErr)
					}
					if mode == "both-down" {
						if err == nil || string(data) != "existing" || !strings.Contains(string(output), ErrReleaseUnavailable.Error()) {
							t.Fatalf("err=%v output=%s installed=%q", err, output, data)
						}
					} else if err != nil || string(data) != string(good) {
						t.Fatalf("err=%v output=%s installed=%q", err, output, data)
					}
				} else {
					old := http.DefaultTransport
					http.DefaultTransport = server.Client().Transport
					t.Cleanup(func() { http.DefaultTransport = old })
					rel, path, err := PrepareUpdate(context.Background(), "v0.149.5", root)
					if mode == "both-down" {
						if err != ErrReleaseUnavailable || path != "" {
							t.Fatalf("path=%q err=%v", path, err)
						}
					} else {
						data, readErr := os.ReadFile(path)
						if err != nil || readErr != nil || rel.TagName != "v0.150.0" || string(data) != string(good) {
							t.Fatalf("release=%+v path=%q err=%v read=%v", rel, path, err, readErr)
						}
					}
					if data, err := os.ReadFile(installed); err != nil || string(data) != "existing" {
						t.Fatalf("prepare touched existing install: %q, %v", data, err)
					}
					// The same source order also applies to version-only checks.
					_, err = LatestRelease(context.Background())
					if (mode == "both-down") != (err == ErrReleaseUnavailable) || mode != "both-down" && err != nil {
						t.Fatalf("latest release: %v", err)
					}
				}
				mu.Lock()
				defer mu.Unlock()
				if len(calls) == 0 || calls[0] != "/dl/latest.json" {
					t.Fatalf("mirror was not first: %v", calls)
				}
				fallback := false
				for _, path := range calls {
					fallback = fallback || strings.HasPrefix(path, "/repos/")
				}
				if fallback != (mode != "mirror-up") {
					t.Fatalf("unexpected fallback: %v", calls)
				}
				t.Logf("local HTTPS requests: %v", calls)
			})
		}
	}
}
