package updater

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
	"strings"
	"syscall"
	"time"

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/daemonctl"
)

const githubRepo = "ehrlich-b/wingthing"

type GhRelease struct {
	TagName    string    `json:"tag_name"`
	Assets     []GhAsset `json:"assets"`
	Prerelease bool      `json:"prerelease"`
	Draft      bool      `json:"draft"`
}

type GhAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type DaemonUpdateState struct {
	Pid       int
	Kind      daemonctl.DaemonKind
	StartArgs []string
}

// daemonStateForUpdate snapshots and validates the restart command while the
// lifecycle lock is held. In particular, an update must not stop a live daemon
// and only then discover that its saved restart metadata is missing or corrupt.
func DaemonStateForUpdate() (*DaemonUpdateState, error) {
	if err := validateUpdateState(); err != nil {
		return nil, err
	}
	pid, kind, err := daemonctl.ReadDaemon()
	if err != nil {
		if errors.Is(err, daemonctl.ErrNoDaemonRunning) {
			return nil, nil
		}
		return nil, err
	}
	argsPath := daemonctl.WingArgsPath()
	if kind == daemonctl.RoostDaemon {
		argsPath = daemonctl.RoostArgsPath()
	}
	saved, err := os.ReadFile(argsPath)
	if err != nil {
		return nil, fmt.Errorf("read saved %s daemon args: %w", kind, err)
	}
	startArgs, err := daemonRestartArgs(saved, kind)
	if err != nil {
		return nil, fmt.Errorf("validate saved %s daemon args: %w", kind, err)
	}
	return &DaemonUpdateState{Pid: pid, Kind: kind, StartArgs: startArgs}, nil
}

func daemonRestartArgs(saved []byte, kind daemonctl.DaemonKind) ([]string, error) {
	foregroundArgs, err := daemonctl.ParseSavedDaemonArgs(saved, kind)
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

func ReleaseHTTPClient(timeout time.Duration) *http.Client {
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

func ValidateReleaseAssetURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if !strings.EqualFold(u.Scheme, "https") || u.Host == "" {
		return fmt.Errorf("must be an absolute HTTPS URL")
	}
	return nil
}

func decodeGitHubRelease(body io.Reader) (GhRelease, error) {
	const maxReleaseMetadataBytes = 1 << 20
	data, err := io.ReadAll(io.LimitReader(body, maxReleaseMetadataBytes+1))
	if err != nil {
		return GhRelease{}, err
	}
	if len(data) > maxReleaseMetadataBytes {
		return GhRelease{}, fmt.Errorf("release metadata exceeds %d bytes", maxReleaseMetadataBytes)
	}
	var release GhRelease
	if err := json.Unmarshal(data, &release); err != nil {
		return GhRelease{}, err
	}
	return release, nil
}

func FetchReleaseChecksum(ctx context.Context, manifestURL, binaryName string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := ReleaseHTTPClient(30 * time.Second).Do(req)
	if err != nil {
		return "", err
	}
	defer cmdutil.CloseWithLog("release checksum response", resp.Body)
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

func ValidateReleaseBinary(ctx context.Context, path string) error {
	if config.Channel() == "preview" {
		checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		output, err := exec.CommandContext(checkCtx, path, "--expected-channel", "preview", "channel", "--json").CombinedOutput()
		if err != nil {
			return fmt.Errorf("preview identity: %w", err)
		}
		var identity struct {
			Channel    string `json:"release_channel"`
			Executable string `json:"executable"`
		}
		if err := json.Unmarshal(output, &identity); err != nil {
			return err
		}
		if identity.Channel != "preview" || identity.Executable != "wt-preview" {
			return fmt.Errorf("downloaded binary is not preview")
		}
	}
	checks := []struct {
		args     []string
		contains string
	}{
		{args: []string{"--version"}, contains: config.BinaryName() + " version"},
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
