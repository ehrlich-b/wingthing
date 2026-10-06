//go:build darwin

package sandbox

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

type seatbeltSandbox struct {
	cfg     Config
	profile string
	tmpDir  string
}

// newPlatform creates a sandbox-exec (Seatbelt) sandbox.
// sandbox-exec is built into macOS and requires no installation.
func newPlatform(cfg Config) (Sandbox, error) {
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		return nil, fmt.Errorf("sandbox-exec not found: %w", err)
	}

	profile, err := buildCheckedProfile(cfg)
	if err != nil {
		return nil, err
	}

	dir, err := os.MkdirTemp("", "wt-sandbox-*")
	if err != nil {
		return nil, fmt.Errorf("create sandbox tmpdir: %w", err)
	}

	log.Printf("seatbelt sandbox: created tmpdir=%s network=%s", dir, cfg.NetworkNeed)
	log.Printf("seatbelt profile:\n%s", profile)
	return &seatbeltSandbox{cfg: cfg, profile: profile, tmpDir: dir}, nil
}

func (s *seatbeltSandbox) Exec(ctx context.Context, name string, args []string) (*exec.Cmd, error) {
	if s.cfg.Trace {
		return nil, fmt.Errorf("trace mode requires strace (Linux only)")
	}
	execArgs := []string{"-p", s.profile, name}
	execArgs = append(execArgs, args...)
	cmd := exec.CommandContext(ctx, "sandbox-exec", execArgs...)
	cmd.Dir = s.tmpDir
	return cmd, nil
}

func (s *seatbeltSandbox) PostStart(pid int) error {
	return nil
}

func (s *seatbeltSandbox) DiagLog() string  { return "" }
func (s *seatbeltSandbox) TraceLog() string { return "" }

func (s *seatbeltSandbox) Destroy() error {
	return os.RemoveAll(s.tmpDir)
}

// buildProfile returns profile text, or an empty string for unsafe paths.
// Production uses buildCheckedProfile to report the error before spawning.
func buildProfile(cfg Config) string {
	profile, _ := buildSeatbeltProfile(cfg)
	return profile
}

// buildSeatbeltProfile generates a Seatbelt (.sb) profile from sandbox config.
// Uses allow-default with specific deny rules. SBPL gives precedence to
// later rules, so ordering matters: deny-write rules must come after
// mount allows to prevent the mount allow from overriding them.
func buildSeatbeltProfile(cfg Config) (string, error) {
	var pathErr error
	resolvePath := func(path string) (string, error) {
		abs, err := canonicalSandboxPath(path)
		if err == nil {
			err = validateSeatbeltPath(path)
		}
		if err == nil {
			err = validateSeatbeltPath(abs)
		}
		if err != nil && pathErr == nil {
			pathErr = fmt.Errorf("Seatbelt profile path %q: %w", path, err)
		}
		return abs, err
	}
	var sb strings.Builder
	sb.WriteString("(version 1)\n")
	sb.WriteString("(allow default)\n")

	// Network rules based on NetworkNeed (derived from domain list).
	// SBPL supports port filtering via (remote tcp "*:PORT") but NOT per-IP
	// or per-domain ("host must be * or localhost"). DNS on macOS goes through
	// /private/var/run/mDNSResponder (Unix socket), not UDP 53.
	if cfg.ProxyPort > 0 {
		// The proxy URL uses 127.0.0.1, and the host-side proxy resolves CONNECT
		// destinations after domain filtering. Agent-side DNS would bypass it.
		sb.WriteString("(deny network*)\n")
		fmt.Fprintf(&sb, "(allow network-outbound (remote tcp \"localhost:%d\"))\n", cfg.ProxyPort)
	} else {
		switch cfg.NetworkNeed {
		case NetworkNone:
			sb.WriteString("(deny network*)\n")
		case NetworkLocal:
			sb.WriteString("(deny network*)\n")
		case NetworkHTTPS:
			sb.WriteString("(deny network*)\n")
			sb.WriteString("(allow network-outbound (literal \"/private/var/run/mDNSResponder\") (remote tcp \"*:443\" \"*:80\"))\n")
		case NetworkFull:
			// no deny — full network access
		}
	}
	// Explicit loopback services remain reachable alongside the domain proxy.
	// Agent/provider defaults are resolved into LocalPorts by the caller.
	for _, port := range cfg.LocalPorts {
		fmt.Fprintf(&sb, "(allow network-outbound (remote ip \"localhost:%d\"))\n", port)
	}

	// Allow outbound connections to specific Unix sockets (e.g. tool sockets).
	// Must come after any network deny rule so the allow takes precedence.
	for _, sock := range cfg.AllowSockets {
		abs, err := resolvePath(sock)
		if err != nil {
			continue
		}
		fmt.Fprintf(&sb, "(allow network-outbound (literal %q))\n", abs)
	}

	// Resolve the SSH directory once for the deny exceptions emitted below.
	sshDir, _ := os.UserHomeDir()
	if sshDir != "" {
		sshDir, _ = resolvePath(filepath.Join(sshDir, ".ssh"))
	}

	// Mount-based filesystem write isolation.
	// Deny writes to $HOME, then allow mount paths via most-specific-wins.
	// Agent config dirs use regex instead of subpath so that files like
	// ~/.claude.json (adjacent to ~/.claude/) are also writable.
	// Note: ro:/ is implicit — (allow default) already grants read access
	// to the entire filesystem. Only writable mounts trigger write isolation.
	hasWritableMounts := false
	for _, m := range cfg.Mounts {
		if !m.ReadOnly {
			hasWritableMounts = true
			break
		}
	}
	if hasWritableMounts {
		home, _ := os.UserHomeDir()
		if home != "" {
			home, _ = resolvePath(home)
		}
		if home != "" {
			fmt.Fprintf(&sb, "(deny file-write* (subpath %q))\n", home)
			for _, m := range cfg.Mounts {
				if m.ReadOnly {
					continue
				}
				abs, err := resolvePath(m.Source)
				if err != nil {
					continue
				}
				if m.UseRegex {
					// Regex covers both the directory and adjacent files with the same prefix.
					// e.g. ~/.claude/ AND ~/.claude.json — subpath only covers the directory.
					fmt.Fprintf(&sb, "(allow file-write* (regex #\"^%s\"))\n", sbplRegexEscape(abs))
				} else {
					fmt.Fprintf(&sb, "(allow file-write* (subpath %q))\n", abs)
				}
			}
		}
		// Allow keychain writes so agents can persist OAuth tokens.
		// Matches Apple's application.sb — keychain ops go through securityd
		// but need file-write to ~/Library/Keychains/.
		keychains := filepath.Join(home, "Library", "Keychains")
		fmt.Fprintf(&sb, "(allow file-write* (subpath %q))\n", keychains)
		// Always allow writes to system tmp dirs (resolve symlinks for macOS /tmp -> /private/tmp)
		tmpDir := os.TempDir()
		if real, err := resolvePath(tmpDir); err == nil {
			tmpDir = real
		}
		fmt.Fprintf(&sb, "(allow file-write* (subpath %q))\n", tmpDir)
		sb.WriteString("(allow file-write* (subpath \"/private/tmp\"))\n")
	}

	// Other eggs' environments carry capabilities. Deny inspection of other
	// processes while preserving self-inspection needed by node and python.
	if cfg.DenyOtherProcessInfo {
		sb.WriteString("(deny process-info* (target others))\n")
	}
	// Seal controller paths after all general mount, temp and socket allows.
	// Both literal and subpath filters are needed for missing paths and trees.
	for _, path := range cfg.ControlDenyPaths {
		abs, err := resolvePath(path)
		if err != nil {
			continue
		}
		fmt.Fprintf(&sb, "(deny file-read* file-write* network-outbound (literal %q))\n", abs)
		fmt.Fprintf(&sb, "(deny file-read* file-write* network-outbound (subpath %q))\n", abs)
	}
	// Reopen only this session's bridges and tool socket after control denies.
	// Ordinary explicit denies below still take precedence over these exceptions.
	for _, bridge := range cfg.ControlBridges {
		abs, err := resolvePath(bridge.Source)
		if err != nil {
			continue
		}
		fmt.Fprintf(&sb, "(allow file-read* (literal %q))\n", abs)
		fmt.Fprintf(&sb, "(allow file-read* (subpath %q))\n", abs)
		if !bridge.ReadOnly {
			fmt.Fprintf(&sb, "(allow file-write* (literal %q))\n", abs)
		}
	}
	if cfg.ControlSocket != "" {
		if abs, err := resolvePath(cfg.ControlSocket); err == nil {
			fmt.Fprintf(&sb, "(allow network-outbound (literal %q))\n", abs)
		}
	}

	// Deny paths — block reads, writes, and Unix-socket connections to specific
	// paths. A filesystem deny alone does not stop connect(2) to an already-open
	// Unix socket on macOS; network-outbound must name the socket path as well.
	// These rules
	// must follow mount allows so a writable mount cannot reopen an explicitly
	// denied path. The narrow known_hosts exception follows the denies, unless
	// that exact file was explicitly denied too.
	for _, d := range cfg.Deny {
		abs, err := resolvePath(d)
		if err != nil {
			continue
		}
		// Seatbelt's subpath filter covers descendants, but not creation of the
		// exact path when that path does not exist yet. The literal filter closes
		// that gap; both rules are required for a directory deny.
		fmt.Fprintf(&sb, "(deny file-read* file-write* (literal %q))\n", abs)
		fmt.Fprintf(&sb, "(deny file-read* file-write* (subpath %q))\n", abs)
		fmt.Fprintf(&sb, "(deny network-outbound (literal %q))\n", abs)
		fmt.Fprintf(&sb, "(deny network-outbound (subpath %q))\n", abs)
		// Allow reading ~/.ssh/known_hosts so SSH can verify host keys
		// without prompting (prompts interleave with agent PTY output).
		// Skipped if known_hosts is also explicitly denied.
		if abs == sshDir && !containsDenyPath(cfg.Deny, filepath.Join(abs, "known_hosts")) {
			fmt.Fprintf(&sb, "(allow file-read* (literal %q))\n", filepath.Join(abs, "known_hosts"))
		}
	}

	// Deny-write paths — block writes only, reads allowed.
	// Emitted AFTER mount allows so they take precedence in SBPL evaluation.
	for _, d := range cfg.DenyWrite {
		abs, err := resolvePath(d)
		if err != nil {
			continue
		}
		fmt.Fprintf(&sb, "(deny file-write* (literal %q))\n", abs)
	}

	// Protected write targets — host-owned state the agent must never write.
	// Emitted last so no earlier rule can reopen them. Like Deny, the literal
	// covers creating a missing target and the subpath covers descendants.
	// buildCheckedProfile still refuses any allow rule that overlaps a target.
	for _, t := range cfg.ProtectedWriteTargets {
		abs, err := resolvePath(t)
		if err != nil {
			continue // pathErr refuses the entire profile below
		}
		fmt.Fprintf(&sb, "(deny file-write* (literal %q))\n", abs)
		fmt.Fprintf(&sb, "(deny file-write* (subpath %q))\n", abs)
	}

	if pathErr != nil {
		return "", pathErr
	}
	return sb.String(), nil
}

// Go's %q and SBPL differ on some escapes, and regex literals add another
// quoting layer. Refuse ambiguous paths rather than risk changing the policy.
// Parentheses are safe in quoted literals and escaped by sbplRegexEscape.
func validateSeatbeltPath(path string) error {
	if !utf8.ValidString(path) {
		return fmt.Errorf("path must be valid UTF-8")
	}
	for _, c := range path {
		if c == '"' || c == '\\' || !unicode.IsPrint(c) {
			return fmt.Errorf("path contains a quote, backslash, or nonprintable character")
		}
	}
	return nil
}

// buildCheckedProfile builds the profile handed to sandbox-exec and, when the
// host supplied protected write targets, verifies that exact profile text keeps
// every target unwritable. Unsafe paths are refused even without protected
// targets, so a rejected deny cannot silently disappear from the policy.
func buildCheckedProfile(cfg Config) (string, error) {
	if err := ValidateProtectedWriteTargets(cfg.ProtectedWriteTargets); err != nil {
		return "", err
	}
	profile, err := buildSeatbeltProfile(cfg)
	if err != nil {
		if len(cfg.ProtectedWriteTargets) > 0 {
			return "", &ProtectedWriteTargetError{Reason: "final sandbox policy cannot be verified: " + err.Error()}
		}
		return "", err
	}
	if len(cfg.ProtectedWriteTargets) == 0 {
		return profile, nil
	}
	targets := make([]string, 0, len(cfg.ProtectedWriteTargets))
	for _, t := range cfg.ProtectedWriteTargets {
		abs, err := canonicalSandboxPath(t)
		if err != nil {
			return "", &ProtectedWriteTargetError{Target: t, Reason: "resolve protected target: " + err.Error()}
		}
		targets = append(targets, abs)
	}
	// APFS is case-insensitive by default; folding only widens overlap.
	if err := checkProtectedWriteTargets(profile, targets, true); err != nil {
		return "", err
	}
	return profile, nil
}

// canonicalSandboxPath returns the real path that Seatbelt evaluates. Unlike
// filepath.EvalSymlinks, it also canonicalizes paths that do not exist yet by
// resolving their longest existing ancestor and appending the missing suffix.
// This matters on macOS, where /var and /tmp are symlinks into /private.
func canonicalSandboxPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(real), nil
	}

	current := abs
	var suffix []string
	for {
		parent := filepath.Dir(current)
		if parent == current {
			return abs, nil
		}
		suffix = append([]string{filepath.Base(current)}, suffix...)
		current = parent
		if real, err := filepath.EvalSymlinks(current); err == nil {
			parts := append([]string{filepath.Clean(real)}, suffix...)
			return filepath.Join(parts...), nil
		}
	}
}

// containsDenyPath checks if a resolved path is in the deny list (resolving symlinks).
func containsDenyPath(deny []string, target string) bool {
	for _, d := range deny {
		abs, err := canonicalSandboxPath(d)
		if err != nil {
			continue
		}
		if abs == target {
			return true
		}
	}
	return false
}

// sbplRegexEscape escapes regex metacharacters for SBPL (regex #"...") patterns.
func sbplRegexEscape(s string) string {
	var sb strings.Builder
	for _, c := range s {
		switch c {
		case '.', '*', '+', '?', '(', ')', '[', ']', '{', '}', '|', '^', '$', '\\':
			sb.WriteByte('\\')
		}
		sb.WriteRune(c)
	}
	return sb.String()
}
