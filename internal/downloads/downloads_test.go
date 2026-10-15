package downloads

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDownloadHandlerAllowlistAndSymlinks(t *testing.T) {
	root := t.TempDir()
	t.Setenv("WT_DOWNLOAD_DIR", root)
	if err := os.Mkdir(filepath.Join(root, "v0.149.5"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"latest.json", "v0.149.5/wt-linux-amd64"} {
		if err := os.WriteFile(filepath.Join(root, path), []byte("fixture"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	secret := filepath.Join(t.TempDir(), "unrelated")
	_ = os.WriteFile(secret, []byte("private fixture"), 0600)
	_ = os.Symlink(secret, filepath.Join(root, "v0.149.5/SHA256SUMS"))
	for _, tc := range []struct {
		path string
		want int
	}{
		{"/dl/latest.json", 200}, {"/dl/v0.149.5/wt-linux-amd64", 200},
		{"/dl/", 404}, {"/dl/v0.149.5/", 404}, {"/dl/../unrelated", 404},
		{"/dl/v0.149.5/SHA256SUMS", 404}, {"/dl/v0.149.5/PROVENANCE.md", 404},
		{"/dl/v0.149.5/missing", 404}, {"/dl/v0.149.5/.git", 404},
	} {
		w := httptest.NewRecorder()
		Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if w.Code != tc.want {
			t.Errorf("%s=%d want %d", tc.path, w.Code, tc.want)
		}
		if strings.Contains(w.Body.String(), "private fixture") {
			t.Fatal("symlink target exposed")
		}
	}
	w := httptest.NewRecorder()
	Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/dl/latest.json", nil))
	if w.Code != 405 {
		t.Fatalf("POST=%d", w.Code)
	}
	t.Setenv("WT_DOWNLOAD_DIR", "")
	w = httptest.NewRecorder()
	Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/dl/latest.json", nil))
	if w.Code != 404 {
		t.Fatal("unset root enabled downloads")
	}
}

func TestDownloadInstallerMirrorOnlyAndAtomicity(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("installer supports Linux and Darwin")
	}
	good := []byte("#!/bin/sh\necho 'wt version connect --https status'\n")
	for _, mode := range []string{"healthy", "metadata404", "binary404", "badChecksum", "both404"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			install := filepath.Join(root, "install")
			_ = os.Mkdir(bin, 0755)
			_ = os.Mkdir(install, 0755)
			script := filepath.Join(root, "install.sh")
			_ = os.WriteFile(script, []byte(Installer()), 0755)
			binary := filepath.Join(root, "binary")
			_ = os.WriteFile(binary, good, 0755)
			sums := filepath.Join(root, "sums")
			name := fmt.Sprintf("wt-%s-%s", runtime.GOOS, runtime.GOARCH)
			_ = os.WriteFile(sums, []byte(fmt.Sprintf("%x  %s\n", sha256.Sum256(good), name)), 0600)
			existing := filepath.Join(install, "wt")
			_ = os.WriteFile(existing, []byte("existing"), 0755)
			calls := filepath.Join(root, "calls")
			curl := `#!/bin/sh
out=""; url=""
while [ "$#" -gt 0 ]; do case "$1" in -o) out=$2; shift 2;; *) url=$1; shift;; esac; done
printf '%s\n' "$url" >> "$CALLS"
mirror=false; case "$url" in https://mirror.invalid/*) mirror=true;; esac
if [ "$MODE" = both404 ]; then exit 22; fi
case "$url" in
 */latest.json|*/releases/latest)
  if $mirror && [ "$MODE" = metadata404 ]; then exit 22; fi
  printf '%s\n' '{"tag_name":"v0.149.5"}' > "$out";;
 */SHA256SUMS|*/checksums.txt)
  if $mirror && [ "$MODE" = badChecksum ]; then printf '%064d  %s\n' 0 "$ASSET_NAME" > "$out"; else cp "$SUMS" "$out"; fi;;
 *) if $mirror && [ "$MODE" = binary404 ]; then exit 22; fi; cp "$FIXTURE_BINARY" "$out";;
esac
`
			_ = os.WriteFile(filepath.Join(bin, "curl"), []byte(curl), 0755)
			cmd := exec.Command("sh", script)
			cmd.Env = append(os.Environ(), "HOME="+root, "WT_INSTALL_DIR="+install, "WT_DOWNLOAD_BASE=https://mirror.invalid/dl", "MODE="+mode, "CALLS="+calls, "SUMS="+sums, "FIXTURE_BINARY="+binary, "ASSET_NAME="+name, "PATH="+bin+":"+os.Getenv("PATH"))
			output, err := cmd.CombinedOutput()
			installed, _ := os.ReadFile(existing)
			if mode != "healthy" {
				want := "Unable to download a verified wt release; please try again later.\n"
				if err == nil || string(installed) != "existing" || string(output) != want {
					t.Fatalf("err=%v output=%q installed=%q", err, output, installed)
				}
			} else if err != nil || string(installed) != string(good) {
				t.Fatalf("err=%v output=%q installed=%q", err, output, installed)
			}
			requests, _ := os.ReadFile(calls)
			fallback := false
			for _, request := range strings.Fields(string(requests)) {
				if !strings.HasPrefix(request, "https://mirror.invalid/dl/") {
					fallback = true
				}
			}
			if fallback {
				t.Fatalf("fallback=%t requests=%s", fallback, requests)
			}
			files, _ := os.ReadDir(install)
			if len(files) != 1 {
				t.Fatalf("temporary install leaked: %v", files)
			}
		})
	}
}

func TestDownloadGeneratedInstallerMatchesTemplate(t *testing.T) {
	t.Setenv("WT_DOWNLOAD_BASE", "")
	for _, target := range []string{"scripts/install.sh", "internal/relay/install.sh"} {
		body, err := os.ReadFile(filepath.Join("../..", target))
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != Installer() {
			t.Fatalf("%s is stale; run make generate-installer", target)
		}
	}
}

func TestDownloadSharedSettingAndSafeInstaller(t *testing.T) {
	t.Setenv("WT_DOWNLOAD_BASE", "https://downloads.example/product/")
	if Base() != "https://downloads.example/product" || !strings.Contains(Installer(), "https://downloads.example/product") {
		t.Fatal("server installer and update base disagree")
	}
	for _, value := range []string{"http://downloads.example", "https://user:secret@example.test", "https://example.test/path?query=1", "https://example.test/$(echo unsafe)"} {
		t.Setenv("WT_DOWNLOAD_BASE", value)
		if Base() != "" || strings.Contains(Installer(), value) {
			t.Fatal("unsafe base rendered into shell")
		}
	}
}
