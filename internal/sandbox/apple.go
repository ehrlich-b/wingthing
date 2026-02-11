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
	log.Printf("seatbelt sandbox: created tmpdir=%s isolation=%s", dir, cfg.Isolation)
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
// Uses allow-default with specific deny rules. SBPL uses most-specific-wins,
// so deny paths override broader allows, and mount allows override broader denies.
func buildProfile(cfg Config) string {
	var sb strings.Builder
	sb.WriteString("(version 1)\n")
	sb.WriteString("(allow default)\n")

	// Network isolation for strict/standard.
	// When AllowOutbound is set, skip network deny entirely — cloud agents
	// need full network (API calls, TLS, DNS via mDNS, update checks).
	if !hasNetwork(cfg.Isolation) && !cfg.AllowOutbound {
		sb.WriteString("(deny network*)\n")
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
	if len(cfg.Mounts) > 0 {
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

	return sb.String()
}

// hasNetwork returns whether the isolation level permits network access.
func hasNetwork(level Level) bool {
	return level >= Network
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
