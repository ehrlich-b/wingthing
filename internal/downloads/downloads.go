package downloads

import (
	_ "embed"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

//go:embed base.txt
var defaultBase string

//go:embed installer.sh.tmpl
var installerTemplate string

// Base is the shared download setting for installers and update checks.
func Base() string {
	return releaseURL("WT_DOWNLOAD_BASE", strings.TrimSpace(defaultBase))
}

func GitHubMetadataURL() string {
	return releaseURL("WT_GITHUB_METADATA_URL", "https://api.github.com/repos/ehrlich-b/wingthing/releases/latest")
}

func GitHubBase() string {
	return releaseURL("WT_GITHUB_DOWNLOAD_BASE", "https://github.com/ehrlich-b/wingthing/releases/download")
}

func releaseURL(setting, fallback string) string {
	base := fallback
	if override := os.Getenv(setting); override != "" {
		base = strings.TrimRight(override, "/")
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(base, "\"'$`\\{} \t\r\n") {
		return ""
	}
	return strings.TrimRight(base, "/")
}
func Installer() string { return strings.ReplaceAll(installerTemplate, "@DOWNLOAD_BASE@", Base()) }

var tagPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-preview\.[A-Za-z0-9._-]+)?$`)

func ValidTag(tag string) bool { return tagPattern.MatchString(tag) }

// Handler serves only release files. An unset directory leaves downloads disabled.
// Directory indexes, arbitrary paths and symlinks are never served.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		root := os.Getenv("WT_DOWNLOAD_DIR")
		name := strings.TrimPrefix(r.URL.Path, "/dl/")
		parts := strings.Split(name, "/")
		allowed := len(parts) == 1 && (name == "latest.json" || name == "preview.json")
		if len(parts) == 2 && ValidTag(parts[0]) {
			switch parts[1] {
			case "wt-linux-amd64", "wt-linux-arm64", "wt-darwin-amd64", "wt-darwin-arm64", "wt-preview-linux-amd64", "wt-preview-linux-arm64", "wt-preview-darwin-amd64", "wt-preview-darwin-arm64", "SHA256SUMS", "checksums.txt":
				allowed = true
			}
		}
		if root == "" || !allowed || !strings.HasPrefix(r.URL.Path, "/dl/") {
			http.NotFound(w, r)
			return
		}
		path := root
		for _, part := range append([]string{""}, parts...) {
			path = filepath.Join(path, part)
			info, err := os.Lstat(path)
			if err != nil || info.Mode()&os.ModeSymlink != 0 {
				http.NotFound(w, r)
				return
			}
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if len(parts) == 1 {
			w.Header().Set("Cache-Control", "no-cache")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		http.ServeFile(w, r, path)
	})
}
