package egg

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

func ValidatePreviewClaudeBoundary(agent string, command []string, outerBoundary bool) error {
	if config.Channel() == "preview" && runtime.GOOS == "darwin" && agent == "claude" && len(command) == 0 && outerBoundary {
		return fmt.Errorf("macOS preview Claude requires its native sandbox to protect host provider configuration")
	}
	return nil
}

// PreviewClaudeOSContext separates macOS user/Keychain lookup from Claude's
// explicit per-account config directory. It reads account metadata only.
// Absent data directories are supported so setup-guide remains output-only.
func PreviewClaudeOSContext(dataHome string) (osHome, configDir string, err error) {
	if !filepath.IsAbs(dataHome) {
		return "", "", fmt.Errorf("preview provider data home must be absolute")
	}
	account, err := user.LookupId(strconv.Itoa(os.Getuid()))
	if err != nil || account == nil || !filepath.IsAbs(account.HomeDir) {
		return "", "", fmt.Errorf("resolve OS-account home for preview Keychain context: %v", err)
	}
	osHome = config.CanonicalProviderPath(account.HomeDir)
	configDir = config.CanonicalProviderPath(filepath.Join(dataHome, ".claude"))
	for _, original := range hostClaudePaths(osHome) {
		if providerPathsOverlap(original, configDir) {
			return "", "", fmt.Errorf("preview Claude configuration overlaps host configuration")
		}
	}
	return osHome, configDir, nil
}

func hostClaudePaths(osHome string) []string {
	return []string{filepath.Join(osHome, ".claude"), filepath.Join(osHome, ".claude.json")}
}

func providerPathsOverlap(a, b string) bool {
	contains := func(parent, child string) bool {
		rel, err := filepath.Rel(parent, child)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	return contains(config.CanonicalProviderPath(a), config.CanonicalProviderPath(b)) || contains(config.CanonicalProviderPath(b), config.CanonicalProviderPath(a))
}

// GuardPreviewClaudeMounts prevents a more-specific caller mount from reopening
// the host provider configuration hidden by the native sandbox. Broader mounts
// retain their existing authority with these narrower denies applied last.
func GuardPreviewClaudeMounts(mounts []sandbox.Mount, osHome string) ([]string, error) {
	protected := hostClaudePaths(osHome)
	for _, mount := range mounts {
		for _, original := range protected {
			root := config.CanonicalProviderPath(original)
			target := config.CanonicalProviderPath(mount.Source)
			rel, err := filepath.Rel(root, target)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return nil, fmt.Errorf("preview Claude mount overlaps host provider configuration")
			}
		}
	}
	return protected, nil
}

// ApplyPreviewClaudeOSContext is used after the data-home mounts and native
// hook paths have been fixed. Never use OSHome as the history/sandbox home.
func ApplyPreviewClaudeOSContext(env map[string]string, dataHome string) (string, error) {
	osHome, configDir, err := PreviewClaudeOSContext(dataHome)
	if err != nil {
		return "", err
	}
	if selected := env["CLAUDE_CONFIG_DIR"]; selected != "" && config.CanonicalProviderPath(selected) != configDir {
		return "", fmt.Errorf("preview Claude config directory differs from its provider data home")
	}
	if env["CLAUDE_SECURESTORAGE_CONFIG_DIR"] != "" {
		return "", fmt.Errorf("preview Claude secure-storage override is not allowed")
	}
	delete(env, "CFFIXED_USER_HOME")
	// Login and status leave USER absent on Mac. Apply this after all runtime
	// merges so Claude selects the same existing credential account.
	if runtime.GOOS == "darwin" {
		delete(env, "USER")
	}
	env["HOME"] = osHome
	env["CLAUDE_CONFIG_DIR"] = configDir
	env["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"] = "1"
	env["DISABLE_AUTOUPDATER"] = "1"
	return osHome, nil
}
