//go:build linux

package sandbox

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const (
	defaultCPUTimeSec = 120
	defaultMemBytes   = 512 * 1024 * 1024 // 512MB
	defaultMaxFDs     = 256
)

// Dangerous syscalls to deny via seccomp.
var deniedSyscalls = []uint32{
	unix.SYS_MOUNT,
	unix.SYS_UMOUNT2,
	unix.SYS_REBOOT,
	unix.SYS_SWAPON,
	unix.SYS_SWAPOFF,
	unix.SYS_KEXEC_LOAD,
	unix.SYS_INIT_MODULE,
	unix.SYS_FINIT_MODULE,
	unix.SYS_DELETE_MODULE,
	unix.SYS_PIVOT_ROOT,
	unix.SYS_PTRACE,
}

type linuxSandbox struct {
	cfg    Config
	tmpDir string
}

// newPlatform tries to create a namespace+seccomp sandbox.
// Returns an error if capabilities are insufficient so the factory falls back.
func newPlatform(cfg Config) (Sandbox, error) {
	if !hasNamespaceCapability() {
		return nil, fmt.Errorf("linux sandbox: need root or CAP_SYS_ADMIN for namespaces")
	}

	dir, err := os.MkdirTemp("", "wt-sandbox-*")
	if err != nil {
		return nil, fmt.Errorf("create sandbox tmpdir: %w", err)
	}
	log.Printf("linux sandbox: created tmpdir=%s isolation=%s", dir, cfg.Isolation)
	return &linuxSandbox{cfg: cfg, tmpDir: dir}, nil
}

func hasNamespaceCapability() bool {
	if os.Geteuid() == 0 {
		return true
	}
	// Check CAP_SYS_ADMIN via capget. Use VERSION_1 which needs only one
	// CapUserData struct (VERSION_3 requires [2]CapUserData — passing a single
	// struct corrupts the stack because the kernel writes past the end).
	// VERSION_1 covers caps 0-31 which includes CAP_SYS_ADMIN (cap 21).
	var hdr unix.CapUserHeader
	var data unix.CapUserData
	hdr.Version = unix.LINUX_CAPABILITY_VERSION_1
	hdr.Pid = 0 // current process
	if err := unix.Capget(&hdr, &data); err == nil {
		if data.Effective&(1<<unix.CAP_SYS_ADMIN) != 0 {
			return true
		}
	}
	// Check unprivileged user namespaces (works without root on most modern distros)
	if val, err := os.ReadFile("/proc/sys/kernel/unprivileged_userns_clone"); err == nil {
		return strings.TrimSpace(string(val)) == "1"
	}
	return false
}

func (s *linuxSandbox) Exec(ctx context.Context, name string, args []string) (*exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = s.tmpDir
	cmd.Env = s.buildEnv()
	cmd.SysProcAttr = s.sysProcAttr()
	return cmd, nil
}

// PostStart applies resource limits to the sandboxed process via prlimit.
func (s *linuxSandbox) PostStart(pid int) error {
	for _, rl := range s.rlimits() {
		lim := unix.Rlimit{Cur: rl.value, Max: rl.value}
		if err := unix.Prlimit(pid, rl.resource, &lim, nil); err != nil {
			log.Printf("linux sandbox: prlimit(%d, %d, %d) failed: %v", pid, rl.resource, rl.value, err)
		}
	}
	if len(s.cfg.Deny) > 0 {
		log.Printf("linux sandbox: deny paths not yet supported (deferred to v0.8.x)")
	}
	return nil
}

func (s *linuxSandbox) Destroy() error {
	return os.RemoveAll(s.tmpDir)
}

func (s *linuxSandbox) buildEnv() []string {
	return []string{
		"PATH=/usr/bin:/bin",
		"HOME=" + s.tmpDir,
		"TMPDIR=" + s.tmpDir,
	}
}

func (s *linuxSandbox) sysProcAttr() *syscall.SysProcAttr {
	flags := s.cloneFlags()

	attr := &syscall.SysProcAttr{
		Cloneflags: flags,
	}

	// When not root, use user namespaces for unprivileged isolation.
	// Map current uid/gid to root inside the namespace.
	if os.Geteuid() != 0 && flags != 0 {
		attr.Cloneflags |= syscall.CLONE_NEWUSER
		attr.UidMappings = []syscall.SysProcIDMap{{
			ContainerID: 0,
			HostID:      os.Getuid(),
			Size:        1,
		}}
		attr.GidMappings = []syscall.SysProcIDMap{{
			ContainerID: 0,
			HostID:      os.Getgid(),
			Size:        1,
		}}
	}

	return attr
}

// cloneFlags returns namespace clone flags based on isolation level.
func (s *linuxSandbox) cloneFlags() uintptr {
	switch s.cfg.Isolation {
	case Strict:
		return syscall.CLONE_NEWNS | syscall.CLONE_NEWPID | syscall.CLONE_NEWNET
	case Standard:
		return syscall.CLONE_NEWNS | syscall.CLONE_NEWPID | syscall.CLONE_NEWNET // no network
	case Network:
		return syscall.CLONE_NEWNS | syscall.CLONE_NEWPID // network allowed
	case Privileged:
		return 0
	default:
		return syscall.CLONE_NEWNS | syscall.CLONE_NEWPID
	}
}

// rlimits returns resource limits for the sandboxed process, using config overrides or defaults.
func (s *linuxSandbox) rlimits() []rlimitPair {
	cpuSec := uint64(defaultCPUTimeSec)
	if s.cfg.CPULimit > 0 {
		cpuSec = uint64(s.cfg.CPULimit.Seconds())
	}
	memBytes := uint64(defaultMemBytes)
	if s.cfg.MemLimit > 0 {
		memBytes = s.cfg.MemLimit
	}
	maxFDs := uint64(defaultMaxFDs)
	if s.cfg.MaxFDs > 0 {
		maxFDs = uint64(s.cfg.MaxFDs)
	}
	return []rlimitPair{
		{unix.RLIMIT_CPU, cpuSec},
		{unix.RLIMIT_AS, memBytes},
		{unix.RLIMIT_NOFILE, maxFDs},
	}
}

type rlimitPair struct {
	resource int
	value    uint64
}

// mountPaths prepares bind mount arguments for the mount namespace.
// Returns the list of mount syscall args to execute after clone.
func mountPaths(mounts []Mount, rootDir string) []bindMount {
	var out []bindMount
	for _, m := range mounts {
		target := m.Target
		if !filepath.IsAbs(target) {
			target = filepath.Join(rootDir, target)
		}
		flags := uintptr(unix.MS_BIND | unix.MS_REC)
		if m.ReadOnly {
			flags |= unix.MS_RDONLY
		}
		out = append(out, bindMount{
			source: m.Source,
			target: target,
			flags:  flags,
		})
	}
	return out
}

type bindMount struct {
	source string
	target string
	flags  uintptr
}

// buildSeccompFilter constructs a BPF program that denies dangerous syscalls.
// The filter returns SECCOMP_RET_ERRNO(EPERM) for denied calls and
// SECCOMP_RET_ALLOW for everything else.
func buildSeccompFilter() []unix.SockFilter {
	// BPF program structure:
	// 1. Load syscall number (offsetof(struct seccomp_data, nr))
	// 2. For each denied syscall: compare and jump to deny
	// 3. Allow (default)
	// 4. Deny: return EPERM

	nDenied := len(deniedSyscalls)
	if nDenied == 0 {
		return nil
	}

	// Total instructions: 1 (load) + nDenied (jeq) + 1 (allow) + 1 (deny)
	prog := make([]unix.SockFilter, 0, nDenied+3)

	// Load syscall number: BPF_LD+BPF_W+BPF_ABS, offset 0
	prog = append(prog, unix.SockFilter{
		Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS,
		K:    0, // offsetof(struct seccomp_data, nr)
	})

	// For each denied syscall, jump to deny block if match.
	// Jump targets are relative: jt=jump-to-deny, jf=next-instruction
	for i, nr := range deniedSyscalls {
		jmpToDeny := uint8(nDenied - i) // distance to deny instruction
		prog = append(prog, unix.SockFilter{
			Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K,
			Jt:   jmpToDeny,
			Jf:   0, // fall through to next check
			K:    nr,
		})
	}

	// Allow
	prog = append(prog, unix.SockFilter{
		Code: unix.BPF_RET | unix.BPF_K,
		K:    seccompRetAllow,
	})

	// Deny with EPERM
	prog = append(prog, unix.SockFilter{
		Code: unix.BPF_RET | unix.BPF_K,
		K:    seccompRetErrno | uint32(unix.EPERM),
	})

	return prog
}

const (
	seccompRetAllow = 0x7fff0000
	seccompRetErrno = 0x00050000
)
