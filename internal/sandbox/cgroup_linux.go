//go:build linux

package sandbox

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// cgroupManager manages a cgroups v2 sub-cgroup for an egg session.
// Provides real memory (RSS) and PID tree limits — prlimit RLIMIT_AS
// only limits virtual address space, and RLIMIT_NPROC is per-user not per-tree.
type cgroupManager struct {
	path string // e.g. /sys/fs/cgroup/user.slice/.../wt-egg-<session-id>
}

// newCgroupManager creates a cgroup v2 sub-cgroup with the given limits.
// Returns (nil, nil) if cgroups v2 is unavailable or permissions are insufficient —
// the caller falls back to prlimit-only enforcement.
func newCgroupManager(sessionID string, memLimit uint64, pidLimit uint32) (*cgroupManager, error) {
	if memLimit == 0 && pidLimit == 0 {
		return nil, nil
	}

	// Check cgroups v2 availability
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
		log.Printf("linux sandbox: cgroups v2 not available, falling back to prlimit-only")
		return nil, nil
	}

	ownPath, err := readOwnCgroup()
	if err != nil {
		log.Printf("linux sandbox: cannot read own cgroup: %v, falling back to prlimit-only", err)
		return nil, nil
	}

	parentPath := filepath.Join("/sys/fs/cgroup", ownPath)
	cgroupName := "wt-egg-" + sessionID
	cgroupPath := filepath.Join(parentPath, cgroupName)

	// Create sub-cgroup directory
	if err := os.MkdirAll(cgroupPath, 0755); err != nil {
		log.Printf("linux sandbox: cannot create cgroup %s: %v, falling back to prlimit-only", cgroupPath, err)
		return nil, nil
	}

	// Enable controllers in parent's subtree_control
	controllers := []string{}
	if memLimit > 0 {
		controllers = append(controllers, "+memory")
	}
	if pidLimit > 0 {
		controllers = append(controllers, "+pids")
	}
	if err := enableControllers(parentPath, controllers); err != nil {
		// Clean up the directory we just created
		os.Remove(cgroupPath)
		log.Printf("linux sandbox: cannot enable controllers: %v, falling back to prlimit-only", err)
		return nil, nil
	}

	// Set limits
	if memLimit > 0 {
		memPath := filepath.Join(cgroupPath, "memory.max")
		if err := os.WriteFile(memPath, []byte(fmt.Sprintf("%d", memLimit)), 0644); err != nil {
			os.Remove(cgroupPath)
			log.Printf("linux sandbox: cannot set memory.max: %v, falling back to prlimit-only", err)
			return nil, nil
		}
	}
	if pidLimit > 0 {
		pidPath := filepath.Join(cgroupPath, "pids.max")
		if err := os.WriteFile(pidPath, []byte(fmt.Sprintf("%d", pidLimit)), 0644); err != nil {
			os.Remove(cgroupPath)
			log.Printf("linux sandbox: cannot set pids.max: %v, falling back to prlimit-only", err)
			return nil, nil
		}
	}

	log.Printf("linux sandbox: cgroup created at %s (memory=%d pids=%d)", cgroupPath, memLimit, pidLimit)
	return &cgroupManager{path: cgroupPath}, nil
}

// AddPID moves a process into this cgroup.
func (c *cgroupManager) AddPID(pid int) error {
	if c == nil {
		return nil
	}
	procsPath := filepath.Join(c.path, "cgroup.procs")
	return os.WriteFile(procsPath, []byte(fmt.Sprintf("%d", pid)), 0644)
}

// Destroy removes the cgroup. All processes must have exited first.
func (c *cgroupManager) Destroy() error {
	if c == nil {
		return nil
	}
	return os.Remove(c.path)
}

// parseCgroupV2Path extracts the cgroup v2 path from /proc/self/cgroup content.
// v2 entries have the format "0::<path>". Returns error if no v2 entry found.
func parseCgroupV2Path(content string) (string, error) {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "0::") {
			return line[3:], nil
		}
	}
	return "", fmt.Errorf("no cgroup v2 entry found")
}

// readOwnCgroup reads /proc/self/cgroup and returns the v2 path.
func readOwnCgroup() (string, error) {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", fmt.Errorf("read /proc/self/cgroup: %w", err)
	}
	return parseCgroupV2Path(string(data))
}

// enableControllers writes to cgroup.subtree_control to enable controllers.
// Handles EBUSY: if the parent has direct member processes, moves our process
// to a "wt-daemon" leaf cgroup first, then retries.
func enableControllers(parentPath string, controllers []string) error {
	if len(controllers) == 0 {
		return nil
	}
	payload := strings.Join(controllers, " ")
	controlPath := filepath.Join(parentPath, "cgroup.subtree_control")

	err := os.WriteFile(controlPath, []byte(payload), 0644)
	if err == nil {
		return nil
	}

	// EBUSY: parent has direct processes — move self to a leaf cgroup first.
	// cgroups v2 "no internal processes" rule: a cgroup with controllers enabled
	// in subtree_control cannot have processes in it directly.
	if !strings.Contains(err.Error(), "device or resource busy") {
		return err
	}

	daemonPath := filepath.Join(parentPath, "wt-daemon")
	if err := os.MkdirAll(daemonPath, 0755); err != nil {
		return fmt.Errorf("create wt-daemon cgroup: %w", err)
	}
	procsPath := filepath.Join(daemonPath, "cgroup.procs")
	if err := os.WriteFile(procsPath, []byte(fmt.Sprintf("%d", os.Getpid())), 0644); err != nil {
		return fmt.Errorf("move self to wt-daemon: %w", err)
	}

	// Retry enabling controllers
	return os.WriteFile(controlPath, []byte(payload), 0644)
}
