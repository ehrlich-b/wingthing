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

	"github.com/google/uuid"
)

type appleSandbox struct {
	cfg  Config
	name string
}

// newPlatform tries to create an Apple Containers sandbox.
// Returns an error if the container CLI is not available.
func newPlatform(cfg Config) (Sandbox, error) {
	if _, err := exec.LookPath("container"); err != nil {
		return nil, fmt.Errorf("apple containers not available: %w", err)
	}

	name := "wt-" + uuid.New().String()

	initArgs := []string{"init", "--name", name}
	for _, m := range buildMounts(cfg) {
		initArgs = append(initArgs, "--mount", m)
	}
	if hasNetwork(cfg.Isolation) {
		initArgs = append(initArgs, "--network")
	}

	out, err := exec.Command("container", initArgs...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("container init: %s: %w", string(out), err)
	}

	log.Printf("apple container created: %s (isolation=%s)", name, cfg.Isolation)
	return &appleSandbox{cfg: cfg, name: name}, nil
}

func (s *appleSandbox) Exec(ctx context.Context, name string, args []string) (*exec.Cmd, error) {
	execArgs := []string{"exec", s.name, "--"}
	execArgs = append(execArgs, name)
	execArgs = append(execArgs, args...)

	var cancel context.CancelFunc
	if s.cfg.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, s.cfg.Timeout)
		_ = cancel // caller owns the cmd lifecycle; context handles TTL
	}

	cmd := exec.CommandContext(ctx, "container", execArgs...)
	return cmd, nil
}

func (s *appleSandbox) PostStart(pid int) error {
	return nil // Apple Containers handles isolation at the container level
}

func (s *appleSandbox) Destroy() error {
	if out, err := exec.Command("container", "stop", s.name).CombinedOutput(); err != nil {
		return fmt.Errorf("container stop %s: %s: %w", s.name, string(out), err)
	}
	if out, err := exec.Command("container", "rm", s.name).CombinedOutput(); err != nil {
		return fmt.Errorf("container rm %s: %s: %w", s.name, string(out), err)
	}
	log.Printf("apple container destroyed: %s", s.name)
	return nil
}

// buildMounts returns mount flag values based on isolation level and config.
// When deny paths are set and a mount source is a parent of a denied path,
// the mount is expanded to individual child dirs minus denied ones.
func buildMounts(cfg Config) []string {
	var mounts []string
	for _, m := range cfg.Mounts {
		ro := m.ReadOnly || cfg.Isolation == Strict

		// Check if any deny path is under this mount source
		expanded := expandMountDeny(m.Source, m.Target, cfg.Deny)
		if len(expanded) > 0 {
			for _, em := range expanded {
				spec := em.source + ":" + em.target
				if ro {
					spec += ":ro"
				}
				mounts = append(mounts, spec)
			}
		} else {
			spec := m.Source + ":" + m.Target
			if ro {
				spec += ":ro"
			}
			mounts = append(mounts, spec)
		}
	}
	return mounts
}

type expandedMount struct {
	source string
	target string
}

// expandMountDeny checks if any deny path is a child of source. If so, it enumerates
// the immediate children of source and returns mounts for each non-denied child.
func expandMountDeny(source, target string, deny []string) []expandedMount {
	if len(deny) == 0 {
		return nil
	}

	// Resolve source for comparison
	absSource, err := filepath.Abs(source)
	if err != nil {
		return nil
	}

	// Check if any deny path is under this source
	hasDeny := false
	denySet := make(map[string]bool)
	for _, d := range deny {
		absD, err := filepath.Abs(d)
		if err != nil {
			continue
		}
		if strings.HasPrefix(absD, absSource+"/") || absD == absSource {
			hasDeny = true
			denySet[absD] = true
		}
	}
	if !hasDeny {
		return nil
	}

	// Enumerate immediate children of source, skip denied ones
	entries, err := os.ReadDir(absSource)
	if err != nil {
		return nil
	}

	var result []expandedMount
	for _, e := range entries {
		childPath := filepath.Join(absSource, e.Name())
		if denySet[childPath] {
			continue
		}
		childTarget := filepath.Join(target, e.Name())
		result = append(result, expandedMount{source: childPath, target: childTarget})
	}
	return result
}

// hasNetwork returns whether the isolation level permits network access.
func hasNetwork(level Level) bool {
	return level >= Network
}

// containerName returns the container name for testing.
func (s *appleSandbox) containerName() string {
	return s.name
}

// buildExecArgs constructs the full argument list for a container exec call.
func buildExecArgs(containerName, name string, args []string) []string {
	execArgs := []string{"exec", containerName, "--", name}
	return append(execArgs, args...)
}

// containerNamePrefix is the prefix used for all Apple Container sandbox names.
const containerNamePrefix = "wt-"

// validContainerName checks that a container name has the expected format.
func validContainerName(name string) bool {
	if !strings.HasPrefix(name, containerNamePrefix) {
		return false
	}
	suffix := name[len(containerNamePrefix):]
	_, err := uuid.Parse(suffix)
	return err == nil
}
