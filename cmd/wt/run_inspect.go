package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ehrlich-b/wingthing/internal/runinspect"
	"github.com/spf13/cobra"
)

func runInspectCmd() *cobra.Command {
	var snapshot, outputDir string
	var jsonOutput bool
	var previewBytes int
	cmd := &cobra.Command{
		Use:   "inspect --snapshot PATH",
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
			if outputDir != "" {
				destination, err = runinspect.ValidateDirectory(workspace, outputDir)
				if err != nil {
					return err
				}
				if directoryContains(path, destination) || directoryContains(destination, path) {
					return errors.New("output directory must not overlap the snapshot")
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
			root, err := os.OpenRoot(destination)
			if err != nil {
				return errors.New("output directory is unavailable")
			}
			defer root.Close()
			file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				return errors.New("report destination already exists or is unavailable")
			}
			_, writeErr := io.Copy(file, &buffer)
			closeErr := file.Close()
			if writeErr != nil || closeErr != nil {
				_ = root.Remove(name)
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

func directoryContains(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
