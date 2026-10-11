package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/ehrlich-b/wingthing/internal/runinspect"
	"github.com/spf13/cobra"
)

func runInspectCmd() *cobra.Command {
	var snapshot, outputDir string
	var jsonOutput bool
	var previewBytes int
	cmd := &cobra.Command{
		Use:   "run-inspect --snapshot PATH",
		Short: "Explain a completed offline state snapshot without contacting a wing",
		Long:  "Read a hash-verified completed snapshot inside the current workspace. No default state path, socket or provider is opened. Suggested actions are data for manual review. Output is metadata only unless --preview-bytes is requested.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			workspace, err := os.Getwd()
			if err != nil {
				return err
			}
			path, err := runinspect.ValidateDirectory(workspace, snapshot)
			if err != nil {
				return err
			}
			var destination string
			var outputRoot *os.Root
			if outputDir != "" {
				destination, err = runinspect.ValidateDirectory(workspace, outputDir)
				if err != nil {
					return err
				}
				outputRoot, err = os.OpenRoot(destination)
				if err != nil {
					return errors.New("output directory is unavailable")
				}
				defer outputRoot.Close()
				if err := validateInspectOutput(workspace, path, destination, outputRoot); err != nil {
					return err
				}
			}
			report, err := runinspect.Inspect(cmd.Context(), path, runinspect.Options{WorkspaceRoot: workspace, PreviewBytes: previewBytes})
			if err != nil {
				return err
			}
			var buffer bytes.Buffer
			name := "run-inspect.txt"
			if jsonOutput {
				name = "run-inspect.json"
				encoder := json.NewEncoder(&buffer)
				encoder.SetIndent("", "  ")
				err = encoder.Encode(report)
			} else {
				err = runinspect.RenderText(&buffer, report)
			}
			if err != nil {
				return err
			}
			if destination == "" {
				_, err = io.Copy(cmd.OutOrStdout(), &buffer)
				return err
			}
			if err := validateInspectOutput(workspace, path, destination, outputRoot); err != nil {
				return err
			}
			// Write through the retained root, even if the pathname is replaced
			// after this check. Reopening destination would lose the fence.
			file, err := outputRoot.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				return errors.New("report destination already exists or is unavailable")
			}
			_, writeErr := io.Copy(file, &buffer)
			closeErr := file.Close()
			if writeErr != nil || closeErr != nil {
				_ = outputRoot.Remove(name)
				return errors.Join(writeErr, closeErr)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&snapshot, "snapshot", "", "completed-copy or fixture directory inside this workspace (required)")
	cmd.Flags().StringVar(&outputDir, "output-dir", "", "existing separate workspace directory for one private report file; default stdout")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "emit the versioned typed JSON report")
	cmd.Flags().IntVar(&previewBytes, "preview-bytes", 0, "escaped untrusted preview input bytes per output, 0–4096 (default metadata only)")
	return cmd
}

func validateInspectOutput(workspace, snapshot, destination string, root *os.Root) error {
	if _, err := runinspect.ValidateDirectory(workspace, destination); err != nil {
		return err
	}
	outputInfo, err := root.Stat(".")
	if err != nil {
		return err
	}
	current, err := os.Lstat(destination)
	if err != nil || !current.IsDir() || !os.SameFile(outputInfo, current) {
		return errors.New("output directory changed during inspection")
	}
	snapshotInfo, err := os.Stat(snapshot)
	if err != nil {
		return err
	}
	for _, pair := range []struct {
		parent os.FileInfo
		child  string
	}{{snapshotInfo, destination}, {outputInfo, snapshot}} {
		overlaps, err := directoryContains(pair.parent, pair.child)
		if err != nil {
			return err
		}
		if overlaps {
			return errors.New("output directory must not overlap the snapshot")
		}
	}
	return nil
}

// Compare each ancestor by identity, including case/spelling aliases on the
// underlying filesystem. filepath.Rel alone only compares path strings.
func directoryContains(parent os.FileInfo, child string) (bool, error) {
	for {
		info, err := os.Stat(child)
		if err != nil {
			return false, err
		}
		if os.SameFile(parent, info) {
			return true, nil
		}
		next := filepath.Dir(child)
		if next == child {
			return false, nil
		}
		child = next
	}
}
