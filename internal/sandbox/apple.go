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

	dir, err := os.MkdirTemp("", "wt-sandbox-*")
	if err != nil {
		return nil, fmt.Errorf("create sandbox tmpdir: %w", err)
	}

	profile := buildProfile(cfg)
	log.Printf("seatbelt sandbox: created tmpdir=%s network=%s", dir, cfg.NetworkNeed)
	log.Printf("seatbelt profile:\n%s", profile)
	return &seatbeltSandbox{cfg: cfg, profile: profile, tmpDir: dir}, nil
}

func (s *seatbeltSandbox) Exec(ctx context.Context, name string, args []string) (*exec.Cmd, error) {
	execArgs := []string{"-p", s.profile, name}
	execArgs = append(execArgs, args...)
	cmd := exec.CommandContext(ctx, "sandbox-exec", execArgs...)
	cmd.Dir = s.tmpDir
	return cmd, nil
}

func (s *seatbeltSandbox) PostStart(pid int) error {
	return nil
}

func (s *seatbeltSandbox) Destroy() error {
	return os.RemoveAll(s.tmpDir)
}

// buildProfile generates a Seatbelt (.sb) profile from sandbox config.
// Uses allow-default with specific deny rules. SBPL gives precedence to
// later rules, so ordering matters: deny-write rules must come after
// mount allows to prevent the mount allow from overriding them.
func buildProfile(cfg Config) string {
	var sb strings.Builder
	sb.WriteString("(version 1)\n")
	sb.WriteString("(allow default)\n")

	// Network rules based on NetworkNeed (derived from domain list).
	// SBPL supports port filtering via (remote tcp "*:PORT") but NOT per-IP
	// or per-domain ("host must be * or localhost"). DNS on macOS goes through
	// /private/var/run/mDNSResponder (Unix socket), not UDP 53.
	if cfg.ProxyPort > 0 {
		// Domain-filtering proxy active: block ALL direct outbound,
		// only allow connection to the local proxy + DNS.
		sb.WriteString("(deny network*)\n")
		fmt.Fprintf(&sb, "(allow network-outbound (literal \"/private/var/run/mDNSResponder\") (remote tcp \"localhost:%d\"))\n", cfg.ProxyPort)
	} else {
		switch cfg.NetworkNeed {
		case NetworkNone:
			sb.WriteString("(deny network*)\n")
		case NetworkLocal:
			sb.WriteString("(deny network*)\n")
			sb.WriteString("(allow network-outbound (literal \"/private/var/run/mDNSResponder\") (remote ip \"localhost:*\"))\n")
		case NetworkHTTPS:
			sb.WriteString("(deny network*)\n")
			sb.WriteString("(allow network-outbound (literal \"/private/var/run/mDNSResponder\") (remote tcp \"*:443\" \"*:80\"))\n")
		case NetworkFull:
			// no deny — full network access
		}
	}

	// Deny paths — block reads and writes to specific directories.
	// Resolve symlinks because sandbox-exec uses real paths.
	for _, d := range cfg.Deny {
		abs, err := filepath.Abs(d)
		if err != nil {
			continue
		}
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			abs = real
		}
		fmt.Fprintf(&sb, "(deny file-read* file-write* (subpath %q))\n", abs)
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
		if real, err := filepath.EvalSymlinks(home); err == nil {
			home = real
		}
		if home != "" {
			fmt.Fprintf(&sb, "(deny file-write* (subpath %q))\n", home)
			for _, m := range cfg.Mounts {
				if m.ReadOnly {
					continue
				}
				abs, err := filepath.Abs(m.Source)
				if err != nil {
					continue
				}
				if real, err := filepath.EvalSymlinks(abs); err == nil {
					abs = real
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
		// Always allow writes to system tmp dirs (resolve symlinks for macOS /tmp -> /private/tmp)
		tmpDir := os.TempDir()
		if real, err := filepath.EvalSymlinks(tmpDir); err == nil {
			tmpDir = real
		}
		fmt.Fprintf(&sb, "(allow file-write* (subpath %q))\n", tmpDir)
		sb.WriteString("(allow file-write* (subpath \"/private/tmp\"))\n")
	}

	// Deny-write paths — block writes only, reads allowed.
	// Emitted AFTER mount allows so they take precedence in SBPL evaluation.
	for _, d := range cfg.DenyWrite {
		abs, err := filepath.Abs(d)
		if err != nil {
			continue
		}
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			abs = real
		}
		fmt.Fprintf(&sb, "(deny file-write* (literal %q))\n", abs)
	}

	return sb.String()
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

// profileString returns the generated profile for testing.
func (s *seatbeltSandbox) profileString() string {
	return s.profile
}
