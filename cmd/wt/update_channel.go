package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/daemonctl"
	"github.com/ehrlich-b/wingthing/internal/fsutil"
)

// Stable's latest feed deliberately excludes GitHub prereleases. Preview has a
// separate prerelease selection and asset namespace; publication is a later act.
func releaseMetadataURL() string {
	if config.Channel() == "preview" {
		return fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=100", githubRepo)
	}
	return fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", githubRepo)
}

func decodeChannelRelease(body io.Reader) (ghRelease, error) {
	if config.Channel() != "preview" {
		return decodeGitHubRelease(body)
	}
	data, err := io.ReadAll(io.LimitReader(body, (1<<20)+1))
	if err != nil {
		return ghRelease{}, err
	}
	if len(data) > 1<<20 {
		return ghRelease{}, errors.New("preview release metadata exceeds 1 MiB")
	}
	var releases []ghRelease
	if err := json.Unmarshal(data, &releases); err != nil {
		return ghRelease{}, err
	}
	for _, release := range releases {
		if release.Prerelease && !release.Draft && strings.HasPrefix(release.TagName, "v") && strings.Contains(release.TagName, "-preview.") {
			return release, nil
		}
	}
	return ghRelease{}, errors.New("no published preview release; use update --file with a reviewed local preview package")
}

func releaseAssetName() string {
	return fmt.Sprintf("%s-%s-%s", config.BinaryName(), runtime.GOOS, runtime.GOARCH)
}

func validateUpdateTarget() error {
	if err := validateUpdateState(); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return validateChannelUpdatePath(exe)
}

func validateUpdateState() error {
	dir, err := config.StateDir()
	if err != nil {
		return err
	}
	return config.ValidateStateDirectory(dir)
}

func acquireUpdateLifecycleLock() (*os.File, error) {
	if err := validateUpdateState(); err != nil {
		return nil, err
	}
	return daemonctl.AcquireDaemonLifecycleLock()
}

func validateChannelUpdatePath(path string) error {
	if config.Channel() != "preview" {
		return nil
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	base := filepath.Base(resolved)
	if base != "wt-preview" && base != releaseAssetName() {
		return fmt.Errorf("preview update refuses executable %q; install it as wt-preview", resolved)
	}
	stable := filepath.Join(filepath.Dir(resolved), "wt")
	previewInfo, err := os.Stat(resolved)
	if err != nil {
		return err
	}
	if stableInfo, err := os.Stat(stable); err == nil && os.SameFile(previewInfo, stableInfo) {
		return fmt.Errorf("preview executable aliases stable wt; refusing replacement")
	}
	return nil
}

func updatePreviewFile(ctx context.Context, source, manifest string) (runErr error) {
	if config.Channel() != "preview" {
		return errors.New("--file updates are available only in preview")
	}
	if manifest == "" {
		manifest = filepath.Join(filepath.Dir(source), "SHA256SUMS")
	}
	data, err := os.ReadFile(manifest)
	if err != nil {
		return err
	}
	if len(data) > 1<<20 {
		return errors.New("SHA256SUMS exceeds 1 MiB")
	}
	expected, err := releaseChecksum(data, releaseAssetName())
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	f, err := os.CreateTemp(filepath.Dir(exe), ".wt-preview-update-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = f.Close(); _ = os.Remove(tmp) }()
	if err := f.Chmod(0755); err != nil {
		return err
	}
	digest := sha256.New()
	written, err := io.Copy(io.MultiWriter(f, digest), io.LimitReader(input, (256<<20)+1))
	if err != nil {
		return err
	}
	if written > 256<<20 {
		return errors.New("binary exceeds 256 MiB")
	}
	if !strings.EqualFold(expected, fmt.Sprintf("%x", digest.Sum(nil))) {
		return errors.New("preview artifact checksum mismatch")
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := validateReleaseBinary(ctx, tmp); err != nil {
		return err
	}
	lock, err := acquireUpdateLifecycleLock()
	if err != nil {
		return err
	}
	defer func() {
		if lock != nil {
			runErr = errors.Join(runErr, lock.Close())
		}
	}()
	state, err := daemonStateForUpdate()
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, exe); err != nil {
		return err
	}
	if err := fsutil.SyncDirectory(filepath.Dir(exe)); err != nil {
		return err
	}
	if state != nil {
		if err := daemonctl.StopDaemonAndWait(state.pid, state.kind, 5*time.Second); err != nil {
			return fmt.Errorf("preview replaced but restart failed: %w", err)
		}
		paths := []string{daemonctl.WingPidPath(), daemonctl.WingArgsPath(), daemonctl.WingStatusPath()}
		if state.kind == daemonctl.RoostDaemon {
			paths = []string{daemonctl.RoostPidPath(), daemonctl.RoostArgsPath()}
		}
		if err := cmdutil.RemoveFiles(paths...); err != nil {
			return err
		}
		if err := lock.Close(); err != nil {
			return err
		}
		lock = nil
		// Defer observes nil to avoid a second close after restart.
		child := exec.CommandContext(ctx, exe, state.startArgs...)
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Run(); err != nil {
			return fmt.Errorf("restart preview daemon: %w", err)
		}
	}
	fmt.Println("updated preview from verified local artifact")
	return nil
}
