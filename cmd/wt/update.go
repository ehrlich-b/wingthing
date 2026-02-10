package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"

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

func updateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Update wt to the latest release",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("current version: %s\n", version)

			// Fetch latest release
			req, _ := http.NewRequest("GET", fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", githubRepo), nil)
			req.Header.Set("Accept", "application/vnd.github+json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return fmt.Errorf("fetch latest release: %w", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode == http.StatusNotFound {
				return fmt.Errorf("no releases found — tag a release first")
			}
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("github API error: %s", resp.Status)
			}

			var rel ghRelease
			if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
				return fmt.Errorf("parse release: %w", err)
			}

			if rel.TagName == version {
				fmt.Println("already up to date")
				return nil
			}

			// Find matching binary
			wantName := fmt.Sprintf("wt-%s-%s", runtime.GOOS, runtime.GOARCH)
			var downloadURL string
			for _, a := range rel.Assets {
				if a.Name == wantName {
					downloadURL = a.BrowserDownloadURL
					break
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

			fmt.Printf("downloading %s...\n", rel.TagName)

			// Download binary
			dlResp, err := http.Get(downloadURL)
			if err != nil {
				return fmt.Errorf("download: %w", err)
			}
			defer dlResp.Body.Close()

			if dlResp.StatusCode != http.StatusOK {
				return fmt.Errorf("download failed: %s", dlResp.Status)
			}

			// Write to temp file next to current binary
			exe, err := os.Executable()
			if err != nil {
				return fmt.Errorf("find executable: %w", err)
			}

			tmp := exe + ".tmp"
			f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
			if err != nil {
				return fmt.Errorf("create temp file: %w", err)
			}

			if _, err := io.Copy(f, dlResp.Body); err != nil {
				f.Close()
				os.Remove(tmp)
				return fmt.Errorf("write binary: %w", err)
			}
			f.Close()

			// Atomic replace
			if err := os.Rename(tmp, exe); err != nil {
				os.Remove(tmp)
				return fmt.Errorf("replace binary: %w", err)
			}

			fmt.Printf("updated to %s\n", rel.TagName)
			return nil
		},
	}
}
