package localtls

import (
	"context"
	"crypto/sha1" // macOS and Windows trust-store identifiers use SHA-1 fingerprints.
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Runner exists so trust-store behavior can be tested without mutating the
// machine running the tests.
type Runner interface {
	Run(context.Context, string, ...string) error
}

const trustMarkerVersion = "verified-v2"

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) error {
	cmd := trustExecCommand(ctx, name, args...)
	return cmd.Run()
}

func trustExecCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}

// TrustStore installs and removes only the public CA certificate. The CA and
// leaf private-key paths are intentionally not accepted by trustCommand.
type TrustStore struct {
	GOOS    string
	HomeDir string
	Runner  Runner
}

func SystemTrustStore() TrustStore {
	home, _ := os.UserHomeDir()
	return TrustStore{GOOS: runtime.GOOS, HomeDir: home, Runner: execRunner{}}
}

// Install adds the public root to the current user's trust store. The returned
// bool reports whether a command ran; a matching, verified marker makes this
// idempotent. Markers written before platform verification was added are
// deliberately migrated by running the install ceremony once more.
func (t TrustStore) Install(ctx context.Context, m *Material) (bool, error) {
	trusted, err := markerMatches(m, t.GOOS)
	if err != nil {
		return false, err
	}
	if trusted {
		return false, nil
	}
	if err := t.prepare(ctx); err != nil {
		return false, err
	}
	name, args, err := trustCommand(t.GOOS, "install", m, t.HomeDir)
	if err != nil {
		return false, err
	}
	if t.Runner == nil {
		return false, fmt.Errorf("local trust runner is not configured")
	}
	if err := t.Runner.Run(ctx, name, args...); err != nil {
		return false, fmt.Errorf("install Wingthing public localhost CA in %s user trust store: %w", t.GOOS, err)
	}
	verifyName, verifyArgs, err := trustVerificationCommand(t.GOOS, m, t.HomeDir)
	if err != nil {
		return false, err
	}
	if err := t.Runner.Run(ctx, verifyName, verifyArgs...); err != nil {
		return false, fmt.Errorf("verify Wingthing localhost certificate in %s user trust store: %w", t.GOOS, err)
	}
	marker := trustMarkerVersion + "\n" + t.GOOS + "\n" + m.Fingerprint + "\n"
	if err := os.WriteFile(m.MarkerPath, []byte(marker), 0600); err != nil {
		return false, fmt.Errorf("record local CA trust: %w", err)
	}
	if err := os.Chmod(m.MarkerPath, 0600); err != nil {
		return false, fmt.Errorf("secure local CA trust marker: %w", err)
	}
	return true, nil
}

// Remove deletes this CA's public certificate from the current user's trust
// store. It deliberately leaves the on-disk key material in place so a running
// server is not broken and a later install does not create an orphaned root.
func (t TrustStore) Remove(ctx context.Context, m *Material) (bool, error) {
	trusted, err := markerIdentifies(m, t.GOOS)
	if err != nil {
		return false, err
	}
	if !trusted {
		return false, nil
	}
	commands, err := trustRemovalCommands(t.GOOS, m, t.HomeDir)
	if err != nil {
		return false, err
	}
	if t.Runner == nil {
		return false, fmt.Errorf("local trust runner is not configured")
	}
	for _, command := range commands {
		if err := t.Runner.Run(ctx, command.name, command.args...); err != nil {
			return false, fmt.Errorf("remove Wingthing public localhost CA from %s user trust store: %w", t.GOOS, err)
		}
	}
	if err := os.Remove(m.MarkerPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("remove local CA trust marker: %w", err)
	}
	return true, nil
}

func (t TrustStore) Trusted(m *Material) (bool, error) {
	return markerMatches(m, t.GOOS)
}

func (t TrustStore) prepare(ctx context.Context) error {
	if t.GOOS != "linux" {
		return nil
	}
	if t.HomeDir == "" {
		return fmt.Errorf("locate Linux user trust store: home directory is empty")
	}
	dbDir := filepath.Join(t.HomeDir, ".pki", "nssdb")
	if err := os.MkdirAll(dbDir, 0700); err != nil {
		return fmt.Errorf("create Chromium NSS trust store: %w", err)
	}
	if _, err := os.Stat(filepath.Join(dbDir, "cert9.db")); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect Chromium NSS trust store: %w", err)
	}
	if t.Runner == nil {
		return fmt.Errorf("local trust runner is not configured")
	}
	if err := t.Runner.Run(ctx, "certutil", "-N", "--empty-password", "-d", "sql:"+dbDir); err != nil {
		return fmt.Errorf("initialize Chromium NSS trust store with certutil (install libnss3-tools or nss-tools if missing): %w", err)
	}
	return nil
}

func markerMatches(m *Material, goos string) (bool, error) {
	parts, err := readMarker(m)
	if err != nil {
		return false, err
	}
	return len(parts) == 3 && parts[0] == trustMarkerVersion && parts[1] == goos && parts[2] == m.Fingerprint, nil
}

// markerIdentifies accepts the original two-field marker as well as the
// verified marker so an upgrade can still remove a previously installed root.
func markerIdentifies(m *Material, goos string) (bool, error) {
	parts, err := readMarker(m)
	if err != nil {
		return false, err
	}
	if len(parts) == 2 {
		return parts[0] == goos && parts[1] == m.Fingerprint, nil
	}
	return len(parts) == 3 && parts[0] == trustMarkerVersion && parts[1] == goos && parts[2] == m.Fingerprint, nil
}

func readMarker(m *Material) ([]string, error) {
	b, err := os.ReadFile(m.MarkerPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read local CA trust marker: %w", err)
	}
	return strings.Fields(string(b)), nil
}

func trustCommand(goos, action string, m *Material, homeDir string) (string, []string, error) {
	sha1Sum := sha1.Sum(m.CACert.Raw)
	sha1Fingerprint := strings.ToUpper(hex.EncodeToString(sha1Sum[:]))
	nickname := "Wingthing Localhost CA " + m.Fingerprint[:12]
	switch goos {
	case "darwin":
		if homeDir == "" {
			return "", nil, fmt.Errorf("locate macOS login keychain: home directory is empty")
		}
		keychain := filepath.Join(homeDir, "Library", "Keychains", "login.keychain-db")
		switch action {
		case "install":
			// A policy-scoped `-p ssl` entry can be recorded without being treated
			// as an anchor. The CA itself is critically constrained to
			// localhost and loopback IPs, so install the root using the proven
			// login-keychain form used by local-development CA tooling.
			return "security", []string{"add-trusted-cert", "-r", "trustRoot", "-k", keychain, m.CACertPath}, nil
		}
	case "windows":
		switch action {
		case "install":
			return "certutil.exe", []string{"-user", "-addstore", "-f", "Root", m.CACertPath}, nil
		case "remove":
			return "certutil.exe", []string{"-user", "-delstore", "Root", sha1Fingerprint}, nil
		}
	case "linux":
		if homeDir == "" {
			return "", nil, fmt.Errorf("locate Linux user trust store: home directory is empty")
		}
		database := "sql:" + filepath.Join(homeDir, ".pki", "nssdb")
		switch action {
		case "install":
			return "certutil", []string{"-A", "-d", database, "-n", nickname, "-t", "C,,", "-i", m.CACertPath}, nil
		case "remove":
			return "certutil", []string{"-D", "-d", database, "-n", nickname}, nil
		}
	default:
		return "", nil, fmt.Errorf("automatic browser trust is not yet supported on %s; the generated public CA is %s", goos, m.CACertPath)
	}
	return "", nil, fmt.Errorf("unknown trust-store action %q", action)
}

type trustStoreCommand struct {
	name string
	args []string
}

func trustRemovalCommands(goos string, m *Material, homeDir string) ([]trustStoreCommand, error) {
	if goos == "darwin" {
		if homeDir == "" {
			return nil, fmt.Errorf("locate macOS login keychain: home directory is empty")
		}
		sha1Sum := sha1.Sum(m.CACert.Raw)
		sha1Fingerprint := strings.ToUpper(hex.EncodeToString(sha1Sum[:]))
		keychain := filepath.Join(homeDir, "Library", "Keychains", "login.keychain-db")
		return []trustStoreCommand{
			{name: "security", args: []string{"remove-trusted-cert", m.CACertPath}},
			{name: "security", args: []string{"delete-certificate", "-Z", sha1Fingerprint, keychain}},
		}, nil
	}
	name, args, err := trustCommand(goos, "remove", m, homeDir)
	if err != nil {
		return nil, err
	}
	return []trustStoreCommand{{name: name, args: args}}, nil
}

func trustVerificationCommand(goos string, m *Material, homeDir string) (string, []string, error) {
	sha1Sum := sha1.Sum(m.CACert.Raw)
	sha1Fingerprint := strings.ToUpper(hex.EncodeToString(sha1Sum[:]))
	nickname := "Wingthing Localhost CA " + m.Fingerprint[:12]
	switch goos {
	case "darwin":
		if homeDir == "" {
			return "", nil, fmt.Errorf("locate macOS login keychain: home directory is empty")
		}
		keychain := filepath.Join(homeDir, "Library", "Keychains", "login.keychain-db")
		return "security", []string{"verify-cert", "-c", m.CertPath, "-p", "ssl", "-n", "localhost", "-k", keychain, "-L", "-q"}, nil
	case "windows":
		return "certutil.exe", []string{"-user", "-store", "Root", sha1Fingerprint}, nil
	case "linux":
		if homeDir == "" {
			return "", nil, fmt.Errorf("locate Linux user trust store: home directory is empty")
		}
		database := "sql:" + filepath.Join(homeDir, ".pki", "nssdb")
		return "certutil", []string{"-L", "-d", database, "-n", nickname}, nil
	default:
		return "", nil, fmt.Errorf("automatic browser trust verification is not yet supported on %s", goos)
	}
}
