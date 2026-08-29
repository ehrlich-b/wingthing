package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/ehrlich-b/wingthing/internal/fsutil"
	"github.com/spf13/cobra"
)

const githubRepo = "ehrlich-b/wingthing"

type ghRelease struct {
	TagName string    `json:"tag_name"`
	Assets  []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type daemonUpdateState struct {
	pid       int
	kind      daemonKind
	startArgs []string
}

// daemonStateForUpdate snapshots and validates the restart command while the
// lifecycle lock is held. In particular, an update must not stop a live daemon
// and only then discover that its saved restart metadata is missing or corrupt.
func daemonStateForUpdate() (*daemonUpdateState, error) {
	pid, kind, err := readDaemon()
	if err != nil {
		if errors.Is(err, errNoDaemonRunning) {
			return nil, nil
		}
		return nil, err
	}
	argsPath := wingArgsPath()
	if kind == roostDaemon {
		argsPath = roostArgsPath()
	}
	saved, err := os.ReadFile(argsPath)
	if err != nil {
		return nil, fmt.Errorf("read saved %s daemon args: %w", kind, err)
	}
	startArgs, err := daemonRestartArgs(saved, kind)
	if err != nil {
		return nil, fmt.Errorf("validate saved %s daemon args: %w", kind, err)
	}
	return &daemonUpdateState{pid: pid, kind: kind, startArgs: startArgs}, nil
}

func daemonRestartArgs(saved []byte, kind daemonKind) ([]string, error) {
	foregroundArgs, err := parseSavedDaemonArgs(saved, kind)
	if err != nil {
		return nil, err
	}
	startArgs := make([]string, 0, len(foregroundArgs)-1)
	for _, arg := range foregroundArgs {
		if arg != "--foreground" {
			startArgs = append(startArgs, arg)
		}
	}
	return startArgs, nil
}

func updateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Update wt to the latest release",
		RunE: func(cmd *cobra.Command, args []string) (runErr error) {
			fmt.Printf("current version: %s\n", version)

			// Fetch latest release.
			req, err := http.NewRequestWithContext(cmd.Context(), http.MethodGet, fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", githubRepo), nil)
			if err != nil {
				return fmt.Errorf("create latest release request: %w", err)
			}
			req.Header.Set("Accept", "application/vnd.github+json")
			resp, err := releaseHTTPClient(30 * time.Second).Do(req)
			if err != nil {
				return fmt.Errorf("fetch latest release: %w", err)
			}
			defer closeWithLog("GitHub release response", resp.Body)

			if resp.StatusCode == http.StatusNotFound {
				return fmt.Errorf("no releases found — tag a release first")
			}
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("github API error: %s", resp.Status)
			}

			rel, err := decodeGitHubRelease(resp.Body)
			if err != nil {
				return fmt.Errorf("parse release: %w", err)
			}

			if rel.TagName == version {
				fmt.Println("already up to date")
				return nil
			}

			// Find the matching binary and its release checksum manifest.
			wantName := fmt.Sprintf("wt-%s-%s", runtime.GOOS, runtime.GOARCH)
			var downloadURL string
			var sumsURL string
			for _, a := range rel.Assets {
				if a.Name == wantName {
					downloadURL = a.BrowserDownloadURL
				}
				if a.Name == "SHA256SUMS" {
					sumsURL = a.BrowserDownloadURL
				}
			}
			if downloadURL == "" {
				available := make([]string, len(rel.Assets))
				for i, a := range rel.Assets {
					available[i] = a.Name
				}
				return fmt.Errorf("no binary for %s/%s in release %s (available: %s)",
					runtime.GOOS, runtime.GOARCH, rel.TagName, strings.Join(available, ", "))
			}
			if sumsURL == "" {
				return fmt.Errorf("release %s has no SHA256SUMS manifest; refusing an unverified update", rel.TagName)
			}
			if err := validateReleaseAssetURL(downloadURL); err != nil {
				return fmt.Errorf("binary asset URL: %w", err)
			}
			if err := validateReleaseAssetURL(sumsURL); err != nil {
				return fmt.Errorf("checksum asset URL: %w", err)
			}

			expected, err := fetchReleaseChecksum(cmd.Context(), sumsURL, wantName)
			if err != nil {
				return fmt.Errorf("verify release manifest: %w", err)
			}

			fmt.Printf("downloading %s...\n", rel.TagName)

			// Download binary
			dlReq, err := http.NewRequestWithContext(cmd.Context(), http.MethodGet, downloadURL, nil)
			if err != nil {
				return fmt.Errorf("download request: %w", err)
			}
			dlResp, err := releaseHTTPClient(5 * time.Minute).Do(dlReq)
			if err != nil {
				return fmt.Errorf("download: %w", err)
			}
			defer closeWithLog("release download response", dlResp.Body)

			if dlResp.StatusCode != http.StatusOK {
				return fmt.Errorf("download failed: %s", dlResp.Status)
			}

			// Write to a uniquely named file next to the current binary. Keeping it
			// on the same filesystem makes the final rename atomic; CreateTemp also
			// avoids following a predictable pre-created symlink.
			exe, err := os.Executable()
			if err != nil {
				return fmt.Errorf("find executable: %w", err)
			}

			f, err := os.CreateTemp(filepath.Dir(exe), ".wt-update-*")
			if err != nil {
				return fmt.Errorf("create temp file: %w", err)
			}
			tmp := f.Name()
			defer func() {
				if f != nil {
					if err := f.Close(); err != nil {
						runErr = errors.Join(runErr, fmt.Errorf("close update temporary file: %w", err))
					}
				}
				if tmp != "" {
					if err := removeIfExists(tmp); err != nil {
						runErr = errors.Join(runErr, fmt.Errorf("remove update temporary file: %w", err))
					}
				}
			}()
			if err := f.Chmod(0755); err != nil {
				return fmt.Errorf("make downloaded binary executable: %w", err)
			}

			digest := sha256.New()
			const maxBinaryBytes = 256 << 20
			written, copyErr := io.Copy(io.MultiWriter(f, digest), io.LimitReader(dlResp.Body, maxBinaryBytes+1))
			if copyErr != nil {
				return fmt.Errorf("write binary: %w", copyErr)
			}
			if written > maxBinaryBytes {
				return fmt.Errorf("downloaded binary exceeds %d bytes", maxBinaryBytes)
			}
			if err := f.Sync(); err != nil {
				return fmt.Errorf("sync downloaded binary: %w", err)
			}
			if err := f.Close(); err != nil {
				f = nil
				return fmt.Errorf("close downloaded binary: %w", err)
			}
			f = nil
			actual := fmt.Sprintf("%x", digest.Sum(nil))
			if !strings.EqualFold(actual, expected) {
				return fmt.Errorf("checksum mismatch for %s", wantName)
			}
			if err := validateReleaseBinary(cmd.Context(), tmp); err != nil {
				return fmt.Errorf("release contract: %w", err)
			}

			// Serialize the atomic replacement with daemon start/stop. Without this
			// lock, a concurrent start can race between daemon inspection and the
			// rename, leaving an old process running with misleading new metadata.
			lifecycleLock, err := acquireDaemonLifecycleLock()
			if err != nil {
				return err
			}
			lockHeld := true
			defer func() {
				if lockHeld {
					if err := lifecycleLock.Close(); err != nil {
						runErr = errors.Join(runErr, fmt.Errorf("release daemon lifecycle lock: %w", err))
					}
				}
			}()
			daemonState, err := daemonStateForUpdate()
			if err != nil {
				return fmt.Errorf("inspect running daemon before update: %w", err)
			}

			// Atomic replace
			if err := os.Rename(tmp, exe); err != nil {
				return fmt.Errorf("replace binary: %w", err)
			}
			tmp = ""
			if err := fsutil.SyncDirectory(filepath.Dir(exe)); err != nil {
				return fmt.Errorf("persist binary replacement: %w", err)
			}

			fmt.Printf("updated to %s\n", rel.TagName)

			// Restart a running daemon. Keep the lifecycle lock until the old
			// process is gone and its metadata has been removed, then release it
			// before invoking the normal daemonizing start path. Any competing
			// start after release wins the same lock and the loser sees a live PID;
			// neither can create a duplicate listener.
			if daemonState != nil {
				kind := string(daemonState.kind)
				fmt.Printf("restarting %s daemon (pid %d)...\n", kind, daemonState.pid)
				if err := stopDaemonAndWait(daemonState.pid, daemonState.kind, 5*time.Second); err != nil {
					return fmt.Errorf("updated to %s but could not restart daemon: %w; run 'wt %s stop' and 'wt %s start' manually", rel.TagName, err, kind, kind)
				}
				if daemonState.kind == roostDaemon {
					if err := removeFiles(roostPidPath(), roostArgsPath()); err != nil {
						return fmt.Errorf("remove stopped roost metadata: %w", err)
					}
				} else {
					if err := removeFiles(wingPidPath(), wingArgsPath(), wingStatusPath()); err != nil {
						return fmt.Errorf("remove stopped wing metadata: %w", err)
					}
				}
				if err := lifecycleLock.Close(); err != nil {
					return fmt.Errorf("release daemon lifecycle lock: %w", err)
				}
				lockHeld = false

				child := exec.Command(exe, daemonState.startArgs...)
				child.Stdout = os.Stdout
				child.Stderr = os.Stderr
				if err := child.Run(); err != nil {
					fmt.Printf("warning: failed to restart %s: %v\n", kind, err)
					fmt.Printf("run 'wt %s start' manually to restart\n", kind)
				}
			} else if err := lifecycleLock.Close(); err != nil {
				return fmt.Errorf("release daemon lifecycle lock: %w", err)
			} else {
				lockHeld = false
			}

			return nil
		},
	}
}

func waitForProcessExit(process *os.Process, timeout time.Duration) bool {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := process.Signal(syscall.Signal(0)); err != nil {
			return true
		}
		select {
		case <-deadline.C:
			return false
		case <-ticker.C:
		}
	}
}

func releaseHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if !strings.EqualFold(req.URL.Scheme, "https") {
				return fmt.Errorf("refusing release redirect to non-HTTPS URL")
			}
			if len(via) >= 10 {
				return fmt.Errorf("too many release redirects")
			}
			return nil
		},
	}
}

func validateReleaseAssetURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if !strings.EqualFold(u.Scheme, "https") || u.Host == "" {
		return fmt.Errorf("must be an absolute HTTPS URL")
	}
	return nil
}

func decodeGitHubRelease(body io.Reader) (ghRelease, error) {
	const maxReleaseMetadataBytes = 1 << 20
	data, err := io.ReadAll(io.LimitReader(body, maxReleaseMetadataBytes+1))
	if err != nil {
		return ghRelease{}, err
	}
	if len(data) > maxReleaseMetadataBytes {
		return ghRelease{}, fmt.Errorf("release metadata exceeds %d bytes", maxReleaseMetadataBytes)
	}
	var release ghRelease
	if err := json.Unmarshal(data, &release); err != nil {
		return ghRelease{}, err
	}
	return release, nil
}

func fetchReleaseChecksum(ctx context.Context, manifestURL, binaryName string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := releaseHTTPClient(30 * time.Second).Do(req)
	if err != nil {
		return "", err
	}
	defer closeWithLog("release checksum response", resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download SHA256SUMS: %s", resp.Status)
	}
	const maxManifestBytes = 1 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxManifestBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxManifestBytes {
		return "", fmt.Errorf("SHA256SUMS exceeds %d bytes", maxManifestBytes)
	}
	return releaseChecksum(data, binaryName)
}

func releaseChecksum(manifest []byte, binaryName string) (string, error) {
	found := ""
	for _, line := range strings.Split(string(manifest), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == binaryName {
			if len(fields[0]) != sha256.Size*2 {
				return "", fmt.Errorf("invalid checksum length for %s", binaryName)
			}
			if _, err := hex.DecodeString(fields[0]); err != nil {
				return "", fmt.Errorf("invalid checksum for %s", binaryName)
			}
			if found != "" {
				return "", fmt.Errorf("SHA256SUMS contains duplicate entries for %s", binaryName)
			}
			found = strings.ToLower(fields[0])
		}
	}
	if found != "" {
		return found, nil
	}
	return "", fmt.Errorf("SHA256SUMS does not contain %s", binaryName)
}

func validateReleaseBinary(ctx context.Context, path string) error {
	checks := []struct {
		args     []string
		contains string
	}{
		{args: []string{"--version"}, contains: "wt version"},
		{args: []string{"mcp", "connect", "--help"}, contains: "connect"},
		{args: []string{"serve", "--help"}, contains: "--https"},
		{args: []string{"roost", "start", "--help"}, contains: "--https"},
		{args: []string{"local-cert", "status", "--help"}, contains: "status"},
	}
	for _, check := range checks {
		checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		output, err := exec.CommandContext(checkCtx, path, check.args...).CombinedOutput()
		contextErr := checkCtx.Err()
		cancel()
		if err != nil {
			if contextErr != nil {
				return fmt.Errorf("%s timed out or was canceled: %w", strings.Join(check.args, " "), contextErr)
			}
			return fmt.Errorf("%s failed: %w", strings.Join(check.args, " "), err)
		}
		if !strings.Contains(string(output), check.contains) {
			return fmt.Errorf("%s output does not contain %q", strings.Join(check.args, " "), check.contains)
		}
	}
	return nil
}
