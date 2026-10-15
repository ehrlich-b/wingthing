package updater

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/downloads"
)

var ErrReleaseUnavailable = errors.New("Unable to download a verified wt release; please try again later.")

// MetadataURLs contains only the product's channel feed.
func MetadataURLs() []string { return []string{ReleaseMetadataURL()} }

var ErrPreviewUnavailable = errors.New("Preview release feed /dl/preview.json is unavailable; use update --file with a reviewed local preview package.")

func unavailableRelease() error {
	if config.Channel() == "preview" {
		return ErrPreviewUnavailable
	}
	return ErrReleaseUnavailable
}

func fetchRelease(ctx context.Context, endpoint string) (GhRelease, error) {
	if err := ValidateReleaseAssetURL(endpoint); err != nil {
		return GhRelease{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return GhRelease{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := ReleaseHTTPClient(5 * time.Second).Do(req)
	if err != nil {
		return GhRelease{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return GhRelease{}, fmt.Errorf("release metadata: %s", resp.Status)
	}
	rel, err := DecodeChannelRelease(resp.Body)
	if err != nil {
		return GhRelease{}, err
	}
	if !downloads.ValidTag(rel.TagName) {
		return GhRelease{}, errors.New("invalid release tag")
	}
	base := downloads.Base() + "/" + rel.TagName + "/"
	rel.Assets = []GhAsset{{Name: ReleaseAssetName(), BrowserDownloadURL: base + ReleaseAssetName()}, {Name: "SHA256SUMS", BrowserDownloadURL: base + "SHA256SUMS"}}
	return rel, nil
}

// LatestRelease returns a channel-appropriate version without downloading assets.
func LatestRelease(ctx context.Context) (GhRelease, error) {
	for _, endpoint := range MetadataURLs() {
		rel, err := fetchRelease(ctx, endpoint)
		if err == nil {
			return rel, nil
		}
		if ctx.Err() != nil {
			return GhRelease{}, ctx.Err()
		}
	}
	return GhRelease{}, unavailableRelease()
}

// PrepareUpdate downloads and verifies an entire bundle from one source before
// touching lifecycle state. The returned temporary file is beside the executable
// so the existing atomic replacement and daemon restart flow stays intact.
func PrepareUpdate(ctx context.Context, current, dir string) (GhRelease, string, error) {
	for _, endpoint := range MetadataURLs() {
		rel, err := fetchRelease(ctx, endpoint)
		if err == nil && olderRelease(rel.TagName, current) {
			err = errors.New("release feed predates installed version")
		}
		if err == nil && rel.TagName == current {
			return rel, "", nil
		}
		if err == nil {
			path, err := prepareBundle(ctx, rel, dir)
			if err == nil {
				return rel, path, nil
			}
		}
		if ctx.Err() != nil {
			return GhRelease{}, "", ctx.Err()
		}
	}
	return GhRelease{}, "", unavailableRelease()
}

func prepareBundle(ctx context.Context, rel GhRelease, dir string) (path string, runErr error) {
	name := ReleaseAssetName()
	binaryURL, sumsURL := "", ""
	for _, a := range rel.Assets {
		if a.Name == name {
			binaryURL = a.BrowserDownloadURL
		}
		if a.Name == "SHA256SUMS" {
			sumsURL = a.BrowserDownloadURL
		}
	}
	if err := ValidateReleaseAssetURL(binaryURL); err != nil {
		return "", err
	}
	if err := ValidateReleaseAssetURL(sumsURL); err != nil {
		return "", err
	}
	expected, err := FetchReleaseChecksum(ctx, sumsURL, name)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, binaryURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := ReleaseHTTPClient(5 * time.Minute).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("release binary: %s", resp.Status)
	}
	f, err := os.CreateTemp(dir, "."+config.BinaryName()+"-update-*")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	defer func() {
		if f != nil {
			_ = f.Close()
		}
		if runErr != nil {
			_ = os.Remove(tmp)
		}
	}()
	if err = f.Chmod(0755); err != nil {
		return "", err
	}
	digest := sha256.New()
	written, err := io.Copy(io.MultiWriter(f, digest), io.LimitReader(resp.Body, (256<<20)+1))
	if err != nil {
		return "", err
	}
	if written > 256<<20 {
		return "", errors.New("binary exceeds 256 MiB")
	}
	if err = f.Sync(); err != nil {
		return "", err
	}
	err = f.Close()
	f = nil
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(expected, fmt.Sprintf("%x", digest.Sum(nil))) {
		return "", errors.New("release checksum mismatch")
	}
	if err := validateReleaseBinary(ctx, tmp, rel.TagName); err != nil {
		return "", err
	}
	return tmp, nil
}

// A delayed mirror feed must never roll back a newer installed release.
// Development suffixes retain their numeric release core for this check.
func olderRelease(candidate, current string) bool {
	a, _, _ := strings.Cut(strings.TrimPrefix(candidate, "v"), "-")
	b, _, _ := strings.Cut(strings.TrimPrefix(current, "v"), "-")
	av, bv := strings.Split(a, "."), strings.Split(b, ".")
	if len(av) != 3 || len(bv) != 3 {
		return false
	}
	for i := range av {
		x, e1 := strconv.ParseUint(av[i], 10, 64)
		y, e2 := strconv.ParseUint(bv[i], 10, 64)
		if e1 != nil || e2 != nil {
			return false
		}
		if x != y {
			return x < y
		}
	}
	return false
}
