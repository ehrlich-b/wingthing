package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
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
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			rel, tmp, err := updater.PrepareUpdate(cmd.Context(), version, filepath.Dir(exe))
			if err != nil {
				return err
			}
			fmt.Printf("current version: %s\n", version)
			if tmp == "" {
				fmt.Println("already up to date")
				return nil
			}
			defer func() {
				if tmp != "" {
					runErr = errors.Join(runErr, cmdutil.RemoveIfExists(tmp))
				}
			}()
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
