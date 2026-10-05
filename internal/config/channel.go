package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ReleaseChannel is set at build time. Renaming a stable binary never opts it
// into preview, and environment variables cannot switch an installed channel.
var ReleaseChannel = "stable"

const channelMarker = ".release-channel"

func Channel() string { return ReleaseChannel }

func BinaryName() string {
	if Channel() == "preview" {
		return "wt-preview"
	}
	return "wt"
}

func ChannelLabel() string {
	if Channel() == "preview" {
		return "Wingthing Preview"
	}
	return "Wingthing"
}

// PreviewProviderHome gives every entry point the same lexical credential
// namespace, including paths below macOS /tmp aliases. Validation is separate.
func PreviewProviderHome(stateDir string) string {
	return canonicalConfiguredPath(filepath.Join(stateDir, "provider-home"), "")
}

func CanonicalProviderPath(path string) string {
	return canonicalConfiguredPath(path, "")
}

// ProviderHomeBinding is the optional host-selected file inside preview state
// that names an existing provider data home. It only relocates the credential
// namespace this state already uses; it grants no authority and carries no auth.
const ProviderHomeBinding = ".provider-home"

const providerHomeBindingLimit = 4096

// Tests replace this so overlap checks never inspect a real account's files.
var previewAccountHome = func() (string, error) {
	account, err := user.LookupId(strconv.Itoa(os.Getuid()))
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(account.HomeDir) {
		return "", errors.New("OS-account home directory is not absolute")
	}
	return account.HomeDir, nil
}

// ResolvePreviewProviderHome returns the provider data home for preview state.
// A missing binding selects exactly PreviewProviderHome(stateDir). A present
// binding is used only when fully valid; otherwise it fails closed and never
// falls back to the default namespace.
func ResolvePreviewProviderHome(stateDir string) (home string, bound bool, err error) {
	if Channel() != "preview" {
		return "", false, errors.New("provider data home bindings are preview-only")
	}
	path := filepath.Join(stateDir, ProviderHomeBinding)
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return PreviewProviderHome(stateDir), false, nil
	} else if err != nil {
		return "", true, fmt.Errorf("inspect preview provider-home binding %q: %w", path, err)
	}
	data, err := readProviderHomeBinding(path, providerHomeBindingLimit)
	if err != nil {
		return "", true, fmt.Errorf("preview provider-home binding %q: %w", path, err)
	}
	home, err = validateProviderHomeBinding(stateDir, data)
	if err != nil {
		return "", true, fmt.Errorf("preview provider-home binding %q: %w", path, err)
	}
	return home, true, nil
}

func validateProviderHomeBinding(stateDir string, data []byte) (string, error) {
	text := strings.TrimSuffix(string(data), "\n")
	if text == "" || !utf8.ValidString(text) || strings.IndexFunc(text, unicode.IsControl) >= 0 {
		return "", errors.New("must contain exactly one path line")
	}
	if !filepath.IsAbs(text) || filepath.Clean(text) != text {
		return "", errors.New("must name an absolute clean path")
	}
	accountHome, err := previewAccountHome()
	if err != nil {
		return "", fmt.Errorf("cannot verify the OS-account stable and provider directories: %w", err)
	}
	state := canonicalConfiguredPath(stateDir, "")
	protected := map[string]string{"selected preview state": state}
	for _, name := range []string{".wingthing", ".claude", ".claude.json"} {
		protected["OS-account "+name] = canonicalConfiguredPath(filepath.Join(accountHome, name), "")
	}
	overlaps := func(left, right string) bool {
		if providerHomeFoldsCase {
			left, right = strings.ToLower(left), strings.ToLower(right)
		}
		return configuredPathsOverlap(left, right)
	}
	// Refuse lexical overlap before inspecting the named path at all.
	for label, path := range protected {
		if overlaps(path, text) {
			return "", fmt.Errorf("must not overlap %s %q", label, path)
		}
	}
	resolved, err := filepath.EvalSymlinks(text)
	if err != nil {
		return "", errors.New("must name an existing directory")
	}
	if resolved != text {
		return "", fmt.Errorf("must name the canonical path %q, not an alias", resolved)
	}
	if err := requireExactProviderHomeSpelling(resolved); err != nil {
		return "", err
	}
	for label, path := range protected {
		if overlaps(path, resolved) {
			return "", fmt.Errorf("must not overlap %s %q", label, path)
		}
	}
	if err := requireNoPhysicalProviderHomeOverlap(resolved, protected); err != nil {
		return "", err
	}
	if err := validateProviderHomeDirectory(resolved); err != nil {
		return "", err
	}
	return resolved, nil
}

type providerPathID struct{ dev, ino uint64 }

// providerPathIdentity reports a path's physical identity from metadata only.
// Tests replace it to model aliases such as macOS firmlinks.
var providerPathIdentity = statProviderPathIdentity

// providerPathChain returns the identities of path's deepest existing
// ancestor-or-self followed by each of its ancestors, and whether path itself
// exists. A missing suffix is where a protected path may later be created.
func providerPathChain(path string) ([]providerPathID, bool, error) {
	var chain []providerPathID
	exists := true
	for current := path; ; {
		id, err := providerPathIdentity(current)
		if err == nil {
			chain = append(chain, id)
		} else if len(chain) == 0 && errors.Is(err, os.ErrNotExist) {
			exists = false
		} else {
			return nil, false, fmt.Errorf("cannot verify the physical identity of %q: %w", current, err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			if len(chain) == 0 {
				return nil, false, fmt.Errorf("cannot verify the physical identity of %q", path)
			}
			return chain, exists, nil
		}
		current = parent
	}
}

// Lexical and symlink checks cannot see aliases that resolve to the same
// directory under another name. Compare physical identities so the home is
// neither inside a protected path nor an ancestor of one, including a
// protected path that does not exist yet. Each chain follows the path's
// lexical ancestors, so an alias is caught only where it shares an identity
// with one of those ancestors.
func requireNoPhysicalProviderHomeOverlap(home string, protected map[string]string) error {
	homeChain, exists, err := providerPathChain(home)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("must name an existing directory")
	}
	contains := func(chain []providerPathID, id providerPathID) bool {
		for _, candidate := range chain {
			if candidate == id {
				return true
			}
		}
		return false
	}
	for label, path := range protected {
		chain, exists, err := providerPathChain(path)
		if err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
		if contains(chain, homeChain[0]) || (exists && contains(homeChain, chain[0])) {
			return fmt.Errorf("must not physically overlap %s %q", label, path)
		}
	}
	return nil
}

func DefaultDir() string {
	home, _ := os.UserHomeDir()
	name := ".wingthing"
	if Channel() == "preview" {
		name = ".wingthing-preview"
	}
	return filepath.Join(home, name)
}

func DefaultRelayURL() string {
	if Channel() == "preview" {
		return "http://localhost:8180"
	}
	return "https://ws.wingthing.ai"
}

func DefaultLocalRelayURL() string {
	if Channel() == "preview" {
		return "http://localhost:8180"
	}
	return "http://localhost:8080"
}

func DefaultListenAddr() string {
	if Channel() == "preview" {
		return "127.0.0.1:8180"
	}
	return ":8080"
}

// StateDir resolves without creating or changing any files. Preview refuses
// overlapping stable paths, including aliases through existing symlinks.
func StateDir() (string, error) {
	if Channel() != "stable" && Channel() != "preview" {
		return "", fmt.Errorf("unsupported release channel %q", Channel())
	}
	dir := os.Getenv("WINGTHING_DIR")
	if Channel() == "preview" && os.Getenv("WINGTHING_PREVIEW_DIR") != "" {
		previewDir := os.Getenv("WINGTHING_PREVIEW_DIR")
		if dir != "" && canonicalConfiguredPath(dir, "") != canonicalConfiguredPath(previewDir, "") {
			return "", errors.New("WINGTHING_DIR and WINGTHING_PREVIEW_DIR select different directories")
		}
		dir = previewDir
	}
	if dir == "" {
		dir = DefaultDir()
	}
	if Channel() == "preview" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		stable := canonicalConfiguredPath(filepath.Join(home, ".wingthing"), home)
		resolved := canonicalConfiguredPath(dir, home)
		if configuredPathsOverlap(stable, resolved) {
			reentry, err := validatePreviewProviderReentry(resolved, home)
			if err != nil {
				return "", err
			}
			if !reentry {
				return "", fmt.Errorf("preview state directory %q overlaps stable state %q", dir, stable)
			}
		}
	}
	return dir, nil
}

// A provider launched by preview has HOME inside the state it must reopen for
// its configured MCP. Only that existing, marked parent may re-enter; the
// provider's own HOME/.wingthing is never an adoptable preview directory.
func validatePreviewProviderReentry(root, home string) (bool, error) {
	providerHome := filepath.Join(root, "provider-home")
	if bound, isBound, err := ResolvePreviewProviderHome(root); err != nil {
		return false, err
	} else if isBound {
		providerHome = bound
	}
	if canonicalConfiguredPath(home, "") != providerHome {
		return false, nil
	}
	// Lookup the actual UID explicitly. user.Current may use HOME as a fallback
	// on pure-Go platforms, which would forget the original stable namespace.
	account, err := user.LookupId(strconv.Itoa(os.Getuid()))
	if err != nil {
		return false, fmt.Errorf("preview parent re-entry cannot verify the OS-account stable directory: %w", err)
	}
	if !filepath.IsAbs(account.HomeDir) {
		return false, errors.New("preview parent re-entry requires an absolute OS-account home directory")
	}
	stable := canonicalConfiguredPath(filepath.Join(account.HomeDir, ".wingthing"), "")
	if configuredPathsOverlap(stable, root) {
		return false, fmt.Errorf("preview state directory %q overlaps OS-account stable state %q", root, stable)
	}
	for _, path := range []string{root, providerHome} {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() {
			return false, fmt.Errorf("preview parent re-entry requires an existing directory at %q", path)
		}
	}
	markerPath := filepath.Join(root, channelMarker)
	info, err := os.Lstat(markerPath)
	if err != nil || !info.Mode().IsRegular() {
		return false, fmt.Errorf("preview parent re-entry requires a regular preview marker at %q", markerPath)
	}
	if err := ValidateStateDirectory(root); err != nil {
		return false, err
	}
	return true, nil
}

// ValidateStateDirectory runs before mkdir/chmod, daemon inspection, or auth
// loading. Legacy stable directories remain compatible. Preview only adopts an
// empty directory or a directory already marked preview; it never migrates data.
func ValidateStateDirectory(dir string) error {
	marker, err := os.ReadFile(filepath.Join(dir, channelMarker))
	if err == nil {
		if strings.TrimSpace(string(marker)) != Channel() {
			return fmt.Errorf("state belongs to %q channel, refusing %s access", strings.TrimSpace(string(marker)), Channel())
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read state channel: %w", err)
	} else if Channel() == "preview" {
		entries, readErr := os.ReadDir(dir)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return readErr
		}
		if len(entries) != 0 {
			return fmt.Errorf("preview refuses unmarked nonempty state directory %q; choose a new empty directory", dir)
		}
	}
	if Channel() == "preview" {
		root, err := filepath.EvalSymlinks(dir)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("resolve preview state directory: %w", err)
		}
		root, err = filepath.Abs(root)
		if err != nil {
			return err
		}
		// Provider history/debug links may reference artifacts inside this
		// exact state tree. They may not alias another channel's config, auth,
		// eggs, sockets, or databases. Walk the resolved root even when the
		// selected directory itself is an alias of this preview state.
		return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink == 0 {
				return nil
			}
			target, err := filepath.EvalSymlinks(path)
			if err != nil {
				return fmt.Errorf("resolve preview state symlink %q: %w", path, err)
			}
			relative, err := filepath.Rel(root, target)
			if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				return nil
			}
			// Only actual executable files in the provider binary-link location
			// may refer outside the state tree. Missing/looping links fail above.
			if filepath.Dir(path) == filepath.Join(root, "provider-home", ".local", "bin") {
				info, err := os.Stat(target)
				if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
					return nil
				}
			}
			return fmt.Errorf("preview state symlink %q resolves outside its state tree", path)
		})
	}
	return nil
}

func claimPreviewDirectory(dir string) error {
	if Channel() != "preview" {
		return nil
	}
	path := filepath.Join(dir, channelMarker)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return ValidateStateDirectory(dir)
	}
	if err != nil {
		return err
	}
	_, writeErr := f.WriteString("preview\n")
	return errors.Join(writeErr, f.Close())
}

// The first preview is personal localhost/SSH dogfooding. It cannot enroll in
// an organization or contact a shared/hosted coordinator, even if a config or
// environment variable was copied accidentally. SSH-native control is separate.
func ValidatePreviewRelay(raw string) error {
	if Channel() != "preview" || raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	host := u.Hostname()
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		if u.User == nil && (u.Scheme == "http" || u.Scheme == "https" || u.Scheme == "ws" || u.Scheme == "wss") {
			return nil
		}
	}
	return fmt.Errorf("preview requires a loopback personal coordinator; refusing %q", raw)
}

func ValidatePreviewListenAddr(addr string) error {
	if Channel() != "preview" {
		return nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return nil
	}
	return fmt.Errorf("preview listener must use a loopback address: %q", addr)
}
