package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	"github.com/ehrlich-b/wingthing/internal/updater"
	"github.com/spf13/cobra"
)

func updateCmd() *cobra.Command {
	var localFile, checksumFile string
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update wt to the latest release",
		RunE: func(cmd *cobra.Command, args []string) (runErr error) {
			if err := updater.ValidateUpdateTarget(); err != nil {
				return err
			}
			if localFile != "" {
				return updater.UpdatePreviewFile(cmd.Context(), localFile, checksumFile)
			}
			fmt.Printf("current version: %s\n", version)

			// Fetch latest release.
			req, err := http.NewRequestWithContext(cmd.Context(), http.MethodGet, updater.ReleaseMetadataURL(), nil)
			if err != nil {
				return fmt.Errorf("create latest release request: %w", err)
			}
			req.Header.Set("Accept", "application/vnd.github+json")
			resp, err := updater.ReleaseHTTPClient(30 * time.Second).Do(req)
			if err != nil {
				return fmt.Errorf("fetch latest release: %w", err)
			}
			defer cmdutil.CloseWithLog("GitHub release response", resp.Body)

			if resp.StatusCode == http.StatusNotFound {
				return fmt.Errorf("no releases found — tag a release first")
			}
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("github API error: %s", resp.Status)
			}

			rel, err := updater.DecodeChannelRelease(resp.Body)
			if err != nil {
				return fmt.Errorf("parse release: %w", err)
			}

			if rel.TagName == version {
				fmt.Println("already up to date")
				return nil
			}

			// Find the matching binary and its release checksum manifest.
			wantName := updater.ReleaseAssetName()
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
			if err := updater.ValidateReleaseAssetURL(downloadURL); err != nil {
				return fmt.Errorf("binary asset URL: %w", err)
			}
			if err := updater.ValidateReleaseAssetURL(sumsURL); err != nil {
				return fmt.Errorf("checksum asset URL: %w", err)
			}

			expected, err := updater.FetchReleaseChecksum(cmd.Context(), sumsURL, wantName)
			if err != nil {
				return fmt.Errorf("verify release manifest: %w", err)
			}

			fmt.Printf("downloading %s...\n", rel.TagName)

			// Download binary
			dlReq, err := http.NewRequestWithContext(cmd.Context(), http.MethodGet, downloadURL, nil)
			if err != nil {
				return fmt.Errorf("download request: %w", err)
			}
			dlResp, err := updater.ReleaseHTTPClient(5 * time.Minute).Do(dlReq)
			if err != nil {
				return fmt.Errorf("download: %w", err)
			}
			defer cmdutil.CloseWithLog("release download response", dlResp.Body)

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

			f, err := os.CreateTemp(filepath.Dir(exe), "."+config.BinaryName()+"-update-*")
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
					if err := cmdutil.RemoveIfExists(tmp); err != nil {
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
			if err := updater.ValidateReleaseBinary(cmd.Context(), tmp); err != nil {
				return fmt.Errorf("release contract: %w", err)
			}

			// Serialize the atomic replacement with daemon start/stop. Without this
			// lock, a concurrent start can race between daemon inspection and the
			// rename, leaving an old process running with misleading new metadata.
			lifecycleLock, err := updater.AcquireUpdateLifecycleLock()
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
			daemonState, err := updater.DaemonStateForUpdate()
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
				kind := string(daemonState.Kind)
				fmt.Printf("restarting %s daemon (pid %d)...\n", kind, daemonState.Pid)
				if err := daemonctl.StopDaemonAndWait(daemonState.Pid, daemonState.Kind, 5*time.Second); err != nil {
					return fmt.Errorf("updated to %s but could not restart daemon: %w; run 'wt %s stop' and 'wt %s start' manually", rel.TagName, err, kind, kind)
				}
				if daemonState.Kind == daemonctl.RoostDaemon {
					if err := cmdutil.RemoveFiles(daemonctl.RoostPidPath(), daemonctl.RoostArgsPath()); err != nil {
						return fmt.Errorf("remove stopped roost metadata: %w", err)
					}
				} else {
					if err := cmdutil.RemoveFiles(daemonctl.WingPidPath(), daemonctl.WingArgsPath(), daemonctl.WingStatusPath()); err != nil {
						return fmt.Errorf("remove stopped wing metadata: %w", err)
					}
				}
				if err := lifecycleLock.Close(); err != nil {
					return fmt.Errorf("release daemon lifecycle lock: %w", err)
				}
				lockHeld = false

				child := exec.Command(exe, daemonState.StartArgs...)
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
	cmd.Flags().StringVar(&localFile, "file", "", "preview only: install a checksummed local preview artifact")
	cmd.Flags().StringVar(&checksumFile, "checksum-file", "", "SHA256SUMS manifest for --file (default: beside artifact)")
	return cmd
}
