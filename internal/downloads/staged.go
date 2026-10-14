package downloads

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var stableTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

var deployAssets = []string{"wt-linux-amd64", "wt-linux-arm64", "wt-darwin-amd64", "wt-darwin-arm64"}

// CheckStaged refuses an incomplete release tree before deployment or packaging.
func CheckStaged(root string) error {
	if err := requireDirectory(root); err != nil {
		return err
	}
	feed, err := regularFile(filepath.Join(root, "latest.json"))
	if err != nil {
		return err
	}
	defer feed.Close()
	var release struct {
		TagName    string `json:"tag_name"`
		Draft      *bool  `json:"draft"`
		Prerelease *bool  `json:"prerelease"`
		Assets     []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	decoder := json.NewDecoder(io.LimitReader(feed, 1<<20))
	if err := decoder.Decode(&release); err != nil {
		return fmt.Errorf("invalid latest.json: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("invalid trailing latest.json data")
	}
	latest := release.TagName
	if !stableTag.MatchString(latest) || release.Draft == nil || *release.Draft || release.Prerelease == nil || *release.Prerelease {
		return fmt.Errorf("latest.json must select stable %s", latest)
	}
	seen := make(map[string]bool)
	base := strings.TrimSpace(defaultBase)
	for _, asset := range release.Assets {
		if !isDeployAsset(asset.Name) && asset.Name != "SHA256SUMS" || seen[asset.Name] || asset.URL != base+"/"+latest+"/"+asset.Name {
			return fmt.Errorf("invalid latest.json asset %q", asset.Name)
		}
		seen[asset.Name] = true
	}
	if len(seen) != len(deployAssets)+1 {
		return fmt.Errorf("incomplete latest.json assets")
	}
	if err := checkStagedTag(filepath.Join(root, latest)); err != nil {
		return err
	}
	// Discover retained releases from the staged tree, never from source pins.
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == ".gitkeep" || entry.Name() == "latest.json" {
			continue
		}
		if !stableTag.MatchString(entry.Name()) {
			return fmt.Errorf("unexpected staged release %q", entry.Name())
		}
		if err := checkStagedTag(filepath.Join(root, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func isDeployAsset(name string) bool {
	for _, asset := range deployAssets {
		if name == asset {
			return true
		}
	}
	return false
}

func requireDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("missing release directory %s", path)
	}
	if !info.IsDir() {
		return fmt.Errorf("release directory must not be a symlink: %s", path)
	}
	return nil
}

func regularFile(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("missing regular release file %s", path)
	}
	return os.Open(path)
}

var checksumLine = regexp.MustCompile(`^([0-9a-f]{64}) [ *](wt-(linux|darwin)-(amd64|arm64))$`)

func checkStagedTag(directory string) error {
	if err := requireDirectory(directory); err != nil {
		return err
	}
	sums, err := regularFile(filepath.Join(directory, "SHA256SUMS"))
	if err != nil {
		return err
	}
	content, err := io.ReadAll(io.LimitReader(sums, (1<<20)+1))
	sums.Close()
	if err != nil || len(content) > 1<<20 {
		return fmt.Errorf("invalid SHA256SUMS in %s", directory)
	}
	seen := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSuffix(string(content), "\n"), "\n") {
		match := checksumLine.FindStringSubmatch(line)
		if match == nil || seen[match[2]] {
			return fmt.Errorf("invalid SHA256SUMS in %s", directory)
		}
		name := match[2]
		seen[name] = true
		file, err := regularFile(filepath.Join(directory, name))
		if err != nil {
			return err
		}
		hash := sha256.New()
		size, err := io.Copy(hash, file)
		file.Close()
		if err != nil || size == 0 || hex.EncodeToString(hash.Sum(nil)) != match[1] {
			return fmt.Errorf("release checksum mismatch for %s/%s", directory, name)
		}
	}
	if len(seen) != len(deployAssets) {
		return fmt.Errorf("incomplete SHA256SUMS in %s", directory)
	}
	return nil
}
