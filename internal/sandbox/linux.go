//go:build linux

package sandbox

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// No default resource limits — only apply when explicitly configured.
// V8/Bun/Node need 1GB+ virtual address space for JIT CodeRange alone,
// and interactive sessions shouldn't have a CPU time limit.

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
	var cmd *exec.Cmd

	// Collect writable mount paths for write isolation.
	var writablePaths []string
	for _, m := range s.cfg.Mounts {
		if !m.ReadOnly {
			writablePaths = append(writablePaths, m.Source)
		}
	}

	needsWrapper := len(s.cfg.Deny) > 0 || len(writablePaths) > 0
	if needsWrapper {
		// Wrap through _sandbox_init to apply deny paths (tmpfs overmounts)
		// and write isolation (HOME read-only + writable sub-mounts).
		// The wrapper runs as root in the namespace (needs CAP_SYS_ADMIN for mount),
		// then drops to real UID via nested user namespace before exec'ing the agent.
		exe, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("resolve executable for sandbox wrapper: %w", err)
		}
		uid := os.Getuid()
		gid := os.Getgid()
		wrapArgs := []string{"_deny_init",
			"--uid", fmt.Sprintf("%d", uid),
			"--gid", fmt.Sprintf("%d", gid),
		}
		for _, d := range s.cfg.Deny {
			wrapArgs = append(wrapArgs, "--deny", d)
		}
		home, _ := os.UserHomeDir()
		if home != "" {
			wrapArgs = append(wrapArgs, "--home", home)
		}
		for _, p := range writablePaths {
			wrapArgs = append(wrapArgs, "--writable", p)
		}
		wrapArgs = append(wrapArgs, "--")
		wrapArgs = append(wrapArgs, name)
		wrapArgs = append(wrapArgs, args...)
		cmd = exec.CommandContext(ctx, exe, wrapArgs...)
	} else {
		cmd = exec.CommandContext(ctx, name, args...)
	}

	cmd.Dir = s.tmpDir
	cmd.Env = s.buildEnv()
	attr := s.sysProcAttr()
	if needsWrapper {
		// Don't put wrapper in PID namespace — it needs host /proc to
		// write uid_map for agent's CLONE_NEWUSER. The wrapper creates
		// the PID namespace when spawning the agent instead.
		attr.Cloneflags &^= syscall.CLONE_NEWPID
	}
	cmd.SysProcAttr = attr
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
	if os.Geteuid() != 0 && flags != 0 {
		attr.Cloneflags |= syscall.CLONE_NEWUSER
		uid := os.Getuid()
		gid := os.Getgid()

		needsRoot := len(s.cfg.Deny) > 0 || len(s.cfg.Mounts) > 0
		if needsRoot {
			// Wrapper needs CAP_SYS_ADMIN for mounts → map to UID 0.
			// The wrapper drops to real UID via nested user namespace
			// before exec'ing the agent.
			attr.UidMappings = []syscall.SysProcIDMap{{
				ContainerID: 0,
				HostID:      uid,
				Size:        1,
			}}
			attr.GidMappings = []syscall.SysProcIDMap{{
				ContainerID: 0,
				HostID:      gid,
				Size:        1,
			}}
		} else {
			// No wrapper — map to real uid/gid so agents don't see root.
			attr.UidMappings = []syscall.SysProcIDMap{{
				ContainerID: uid,
				HostID:      uid,
				Size:        1,
			}}
			attr.GidMappings = []syscall.SysProcIDMap{{
				ContainerID: gid,
				HostID:      gid,
				Size:        1,
			}}
		}
	}

	return attr
}

// cloneFlags returns namespace clone flags based on isolation level.
func (s *linuxSandbox) cloneFlags() uintptr {
	var flags uintptr
	switch s.cfg.Isolation {
	case Strict:
		flags = syscall.CLONE_NEWNS | syscall.CLONE_NEWPID | syscall.CLONE_NEWNET
	case Standard:
		flags = syscall.CLONE_NEWNS | syscall.CLONE_NEWPID | syscall.CLONE_NEWNET
	case Network:
		flags = syscall.CLONE_NEWNS | syscall.CLONE_NEWPID
	case Privileged:
		return 0
	default:
		flags = syscall.CLONE_NEWNS | syscall.CLONE_NEWPID
	}
	// Strip network namespace for agents that need network access.
	// Linux can't do port-level filtering in userns without iptables,
	// so HTTPS and Full both get full network. Local gets it too (localhost).
	if s.cfg.NetworkNeed >= NetworkLocal {
		flags &^= syscall.CLONE_NEWNET
	}
	return flags
}

// rlimits returns resource limits for the sandboxed process.
// Only applies limits when explicitly configured — no defaults.
func (s *linuxSandbox) rlimits() []rlimitPair {
	var pairs []rlimitPair
	if s.cfg.CPULimit > 0 {
		pairs = append(pairs, rlimitPair{unix.RLIMIT_CPU, uint64(s.cfg.CPULimit.Seconds())})
	}
	if s.cfg.MemLimit > 0 {
		pairs = append(pairs, rlimitPair{unix.RLIMIT_AS, s.cfg.MemLimit})
	}
	if s.cfg.MaxFDs > 0 {
		pairs = append(pairs, rlimitPair{unix.RLIMIT_NOFILE, uint64(s.cfg.MaxFDs)})
	}
	return pairs
}

type rlimitPair struct {
	resource int
	value    uint64
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
