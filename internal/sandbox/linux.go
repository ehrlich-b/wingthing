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

// No default resource limits — only apply when explicitly configured.
// V8/Bun/Node need 1GB+ virtual address space for JIT CodeRange alone,
// and interactive sessions shouldn't have a CPU time limit.

// Dangerous syscalls to deny via seccomp (cross-platform, all Linux archs).
var deniedSyscallsCommon = []uint32{
	// Original set: filesystem/module/process
	unix.SYS_MOUNT,
	unix.SYS_UMOUNT2,
	// The modern mount API can rearrange mounts too — move_mount in particular
	// could slide the private /proc aside to reveal what it shadows. The sealed
	// jail's agent has no capability over the mount namespace so these already
	// fail EPERM, but deny them outright as defense-in-depth.
	unix.SYS_MOVE_MOUNT,
	unix.SYS_OPEN_TREE,
	unix.SYS_OPEN_TREE_ATTR, // Linux 6.15: open_tree + mount_setattr in one call
	unix.SYS_FSOPEN,
	unix.SYS_FSCONFIG,
	unix.SYS_FSMOUNT,
	unix.SYS_MOUNT_SETATTR,
	unix.SYS_REBOOT,
	unix.SYS_SWAPON,
	unix.SYS_SWAPOFF,
	unix.SYS_KEXEC_LOAD,
	unix.SYS_INIT_MODULE,
	unix.SYS_FINIT_MODULE,
	unix.SYS_DELETE_MODULE,
	unix.SYS_PIVOT_ROOT,
	unix.SYS_PTRACE,
	// Namespace escape
	unix.SYS_SETNS,
	unix.SYS_UNSHARE,
	// Container escape (Shocker CVE-2014-3519)
	unix.SYS_OPEN_BY_HANDLE_AT,
	// eBPF / perf (privilege escalation surface)
	unix.SYS_BPF,
	unix.SYS_PERF_EVENT_OPEN,
	unix.SYS_USERFAULTFD,
	// Kernel keyring
	unix.SYS_KEYCTL,
	unix.SYS_ADD_KEY,
	unix.SYS_REQUEST_KEY,
	// Misc privilege escalation
	unix.SYS_KCMP,
	unix.SYS_LOOKUP_DCOOKIE,
	unix.SYS_ACCT,
	// Time manipulation
	unix.SYS_CLOCK_SETTIME,
	unix.SYS_SETTIMEOFDAY,
	// Kexec variant
	unix.SYS_KEXEC_FILE_LOAD,
}

type linuxSandbox struct {
	cfg    Config
	tmpDir string
	cgroup *cgroupManager
}

// newPlatform tries to create a namespace+seccomp sandbox.
// Returns an error if capabilities are insufficient so the factory falls back.
func newPlatform(cfg Config) (Sandbox, error) {
	if !hasNamespaceCapability() {
		return nil, fmt.Errorf("linux sandbox: required user and mount namespace operations are unavailable")
	}

	dir, err := os.MkdirTemp("", "wt-sandbox-*")
	if err != nil {
		return nil, fmt.Errorf("create sandbox tmpdir: %w", err)
	}

	// Create cgroup for real memory/PID limits (graceful fallback to prlimit-only)
	var cg *cgroupManager
	if cfg.MemLimit > 0 || cfg.PidLimit > 0 {
		cg, _ = newCgroupManager(cfg.SessionID, cfg.MemLimit, cfg.PidLimit)
	}

	log.Printf("linux sandbox: created tmpdir=%s network=%s cgroup=%v", dir, cfg.NetworkNeed, cg != nil)
	return &linuxSandbox{cfg: cfg, tmpDir: dir, cgroup: cg}, nil
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
	// Check unprivileged user namespaces sysctl (fast reject if explicitly disabled).
	if val, err := os.ReadFile("/proc/sys/kernel/unprivileged_userns_clone"); err == nil {
		if strings.TrimSpace(string(val)) != "1" {
			return false
		}
		// Sysctl says enabled, but AppArmor may still block it (Ubuntu 24.04+,
		// kernel 6.1+ with apparmor_restrict_unprivileged_userns=1).
		// Fall through to probe.
	}
	// Probe the mount operations the sandbox actually depends on. Ubuntu 24.04
	// can allow CLONE_NEWUSER and then transition the child into AppArmor's
	// unprivileged_userns profile, where mount(2) is denied. A namespace-only
	// probe reports a dangerous false positive on those hosts.
	return probeMountNamespace()
}

const mountProbeArg = "_sandbox_mount_probe"

// init handles the mount probe in every binary that imports this package,
// including Go test binaries. The target is restricted to a freshly-created
// directory under the process temp directory so this hidden re-exec path can
// never mount over an arbitrary caller-supplied path.
func init() {
	if len(os.Args) != 3 || os.Args[1] != mountProbeArg {
		return
	}
	if err := runMountProbe(os.Args[2]); err != nil {
		fmt.Fprintf(os.Stderr, "wingthing sandbox mount probe: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// probeMountNamespace creates the same user+mount namespace shape used by
// _deny_init, then asks the child to make the root private, create and remount
// a read-only bind mask, and mount tmpfs. These are the filesystem primitives
// the sandbox depends on for deny paths and write isolation.
func probeMountNamespace() bool {
	probeDir, err := os.MkdirTemp("", "wt-userns-probe-")
	if err != nil {
		return false
	}
	defer os.RemoveAll(probeDir)

	exe, err := os.Executable()
	if err != nil {
		return false
	}
	cmd := exec.Command(exe, mountProbeArg, probeDir)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings: []syscall.SysProcIDMap{{
			ContainerID: 0,
			HostID:      os.Getuid(),
			Size:        1,
		}},
		GidMappings: []syscall.SysProcIDMap{{
			ContainerID: 0,
			HostID:      os.Getgid(),
			Size:        1,
		}},
	}
	return cmd.Run() == nil
}

func runMountProbe(probeDir string) error {
	clean := filepath.Clean(probeDir)
	if filepath.Dir(clean) != filepath.Clean(os.TempDir()) ||
		!strings.HasPrefix(filepath.Base(clean), "wt-userns-probe-") {
		return fmt.Errorf("refusing unsafe probe target %q", probeDir)
	}
	info, err := os.Stat(clean)
	if err != nil {
		return fmt.Errorf("stat probe target: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("probe target is not a directory")
	}
	if err := unix.Mount("", "/", "", unix.MS_PRIVATE|unix.MS_REC, ""); err != nil {
		return fmt.Errorf("make root private: %w", err)
	}
	probeFile := filepath.Join(clean, "deny-file")
	if err := os.WriteFile(probeFile, nil, 0o600); err != nil {
		return fmt.Errorf("create deny-file probe: %w", err)
	}
	if err := unix.Mount("/dev/null", probeFile, "", unix.MS_BIND, ""); err != nil {
		return fmt.Errorf("bind deny-file probe: %w", err)
	}
	if err := remountBindReadonly(probeFile); err != nil {
		return fmt.Errorf("remount deny-file probe read-only: %w", err)
	}
	if err := verifyExpectedMounts([]expectedMount{{Path: probeFile, ReadOnly: true}}); err != nil {
		return fmt.Errorf("verify deny-file probe: %w", err)
	}
	if err := unix.Unmount(probeFile, 0); err != nil {
		return fmt.Errorf("unmount deny-file probe: %w", err)
	}
	if err := unix.Mount("tmpfs", clean, "tmpfs", unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV, "size=0"); err != nil {
		return fmt.Errorf("mount read-only tmpfs: %w", err)
	}
	if err := unix.Unmount(clean, 0); err != nil {
		return fmt.Errorf("unmount tmpfs: %w", err)
	}
	// Mirror the jail's procfs swap: recursively bind host /proc, then either
	// detach it or mount a fresh procfs over it — the two constructions
	// _jail_agent_init accepts. Ubuntu 22.04 (5.15) proved a host can pass
	// every mount primitive above yet refuse the detach at spawn time, so the
	// probe must fail unless at least one construction works.
	procProbe := filepath.Join(clean, "proc")
	if err := os.Mkdir(procProbe, 0o555); err != nil {
		return fmt.Errorf("create procfs probe dir: %w", err)
	}
	if err := unix.Mount("/proc", procProbe, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return fmt.Errorf("bind procfs probe: %w", err)
	}
	if detachErr := unix.Unmount(procProbe, unix.MNT_DETACH); detachErr != nil {
		if err := unix.Mount("proc", procProbe, "proc", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, ""); err != nil {
			return fmt.Errorf("procfs swap probe: detach refused (%v) and overmount failed: %w", detachErr, err)
		}
	}
	return nil
}

func (s *linuxSandbox) Exec(ctx context.Context, name string, args []string) (*exec.Cmd, error) {
	var cmd *exec.Cmd

	// Collect writable mount paths for write isolation.
	// Note: ro:/ is implicit — Linux namespaces don't restrict reads.
	// Only writable mounts matter for write isolation logic.
	var writablePaths []string
	for _, m := range s.cfg.Mounts {
		if !m.ReadOnly {
			writablePaths = append(writablePaths, m.Source)
		}
	}

	needsWrapper := len(s.cfg.Deny) > 0 || len(s.cfg.DenyWrite) > 0 || len(writablePaths) > 0
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
		logPath := filepath.Join(s.tmpDir, "deny_init.log")
		wrapArgs := []string{"_deny_init",
			"--uid", fmt.Sprintf("%d", uid),
			"--gid", fmt.Sprintf("%d", gid),
			"--log", logPath,
		}
		for _, d := range s.cfg.Deny {
			wrapArgs = append(wrapArgs, "--deny", d)
		}
		for _, d := range s.cfg.DenyWrite {
			wrapArgs = append(wrapArgs, "--deny-write", d)
		}
		home := s.cfg.UserHome
		if home == "" {
			home, _ = os.UserHomeDir()
		}
		if home != "" {
			wrapArgs = append(wrapArgs, "--home", home)
		}
		for _, p := range writablePaths {
			wrapArgs = append(wrapArgs, "--writable", p)
		}
		// In jail mode (deny:/), pass read-only mount paths for allowlist setup.
		for _, d := range s.cfg.Deny {
			if d == "/" {
				for _, m := range s.cfg.Mounts {
					if m.ReadOnly && m.Source != "/" {
						wrapArgs = append(wrapArgs, "--mount-ro", m.Source)
					}
				}
				break
			}
		}
		// UseRegex mounts need overlayfs on HOME (Linux can't do prefix-based
		// write permissions with bind mounts — new file creation and renames
		// in the RO parent directory fail with EROFS).
		for _, m := range s.cfg.Mounts {
			if m.UseRegex && home != "" {
				rel, err := filepath.Rel(home, m.Source)
				if err == nil && !strings.HasPrefix(rel, "..") {
					wrapArgs = append(wrapArgs, "--overlay-prefix", rel)
				}
			}
		}
		wrapArgs = append(wrapArgs, "--")
		wrapArgs = append(wrapArgs, name)
		wrapArgs = append(wrapArgs, args...)
		cmd = exec.CommandContext(ctx, exe, wrapArgs...)
	} else {
		cmd = exec.CommandContext(ctx, name, args...)
	}

	// Wrap with strace if trace mode is enabled.
	if s.cfg.Trace {
		straceBin, err := exec.LookPath("strace")
		if err != nil {
			return nil, fmt.Errorf("trace mode: strace not found in PATH")
		}
		traceLog := filepath.Join(s.tmpDir, "strace.log")
		// --kill-on-exit sets PTRACE_O_EXITKILL so that when strace (the direct
		// child here, holding the outer Pdeathsig) is killed, the kernel kills
		// the sandbox wrapper and its whole jail too. Without it a hard-killed
		// trace session could leave the jail namespace running detached.
		traceArgs := []string{"-f", "--kill-on-exit", "-o", traceLog}
		traceArgs = append(traceArgs, cmd.Path)
		traceArgs = append(traceArgs, cmd.Args[1:]...)
		cmd = exec.CommandContext(ctx, straceBin, traceArgs...)
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

// PostStart adds the sandboxed process to the cgroup (if available) then
// applies prlimit resource limits as belt+suspenders.
//
// Known race: the child process is already running when PostStart is called,
// so there's a brief window before cgroup limits apply. This is acceptable
// because the child is _deny_init (doing mount setup, not the agent), and
// prlimit covers the gap. CLONE_INTO_CGROUP (Linux 5.7+) would eliminate
// this race but requires CAP_SYS_ADMIN.
func (s *linuxSandbox) PostStart(pid int) error {
	// Cgroup first — real memory (RSS) and PID tree limits
	if s.cgroup != nil {
		if err := s.cgroup.AddPID(pid); err != nil {
			log.Printf("linux sandbox: cgroup AddPID(%d) failed: %v (prlimit still applied)", pid, err)
		}
	}
	// Prlimit as belt+suspenders (virtual address space, CPU, FDs)
	for _, rl := range s.rlimits() {
		lim := unix.Rlimit{Cur: rl.value, Max: rl.value}
		if err := unix.Prlimit(pid, rl.resource, &lim, nil); err != nil {
			log.Printf("linux sandbox: prlimit(%d, %d, %d) failed: %v", pid, rl.resource, rl.value, err)
		}
	}
	return nil
}

func (s *linuxSandbox) DiagLog() string {
	return filepath.Join(s.tmpDir, "deny_init.log")
}

func (s *linuxSandbox) TraceLog() string {
	if s.cfg.Trace {
		return filepath.Join(s.tmpDir, "strace.log")
	}
	return ""
}

func (s *linuxSandbox) Destroy() error {
	if s.cgroup != nil {
		if err := s.cgroup.Destroy(); err != nil {
			log.Printf("linux sandbox: cgroup destroy: %v", err)
		}
	}
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
		// Tie the sandbox wrapper's lifetime to this runtime process: if the egg
		// server dies (crash or hard SIGKILL) the wrapper is killed too, which
		// cascades (via the stage-2 init's own Pdeathsig) to tear down the whole
		// jail namespace instead of leaving a detached tenant session running.
		Pdeathsig: syscall.SIGKILL,
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

// cloneFlags returns namespace clone flags based on NetworkNeed.
func (s *linuxSandbox) cloneFlags() uintptr {
	flags := uintptr(syscall.CLONE_NEWNS | syscall.CLONE_NEWPID | syscall.CLONE_NEWNET)
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
		// RLIMIT_AS limits virtual address space, not physical RAM.
		// JIT runtimes (Bun/JSC, V8, Node) reserve 1GB+ of virtual address
		// space for JIT CodeRange alone, plus heap, stack, and shared libs.
		// Enforce a 4GB floor so JIT-based agents don't OOM on startup.
		mem := s.cfg.MemLimit
		const minVAS = 4 * 1024 * 1024 * 1024 // 4GB
		if mem < minVAS {
			log.Printf("linux sandbox: bumping RLIMIT_AS from %dMB to 4GB (JIT needs virtual address space)", mem/1024/1024)
			mem = minVAS
		}
		pairs = append(pairs, rlimitPair{unix.RLIMIT_AS, mem})
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

	deniedSyscalls := append(deniedSyscallsCommon, deniedSyscallsArch...)
	nDenied := len(deniedSyscalls)
	if nDenied == 0 {
		return nil
	}

	denyInstr := unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: seccompRetErrno | uint32(unix.EPERM)}
	var prog []unix.SockFilter

	// ABI validation. A denylist keyed only on seccomp_data.nr is bypassable by
	// issuing the syscall under a different ABI, whose numbers differ from the
	// native table. Reject any non-native architecture, and on x86-64 reject the
	// x32 number range, BEFORE the number checks. Skipped where seccompNativeArch
	// is 0 (unhardened platforms), preserving prior behavior there.
	if seccompNativeArch != 0 {
		// Load seccomp_data.arch (offset 4); if it is native, skip the deny.
		prog = append(prog,
			unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 4},
			unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: seccompNativeArch, Jt: 1, Jf: 0},
			denyInstr,
		)
	}

	// Load seccomp_data.nr (offset 0).
	prog = append(prog, unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0})

	// Conditional deny checks start here; their jump distance to the final deny
	// instruction is fixed up once the full program length is known.
	condStart := len(prog)
	if seccompX32Bit != 0 {
		// Reject the x32 syscall number range (nr >= __X32_SYSCALL_BIT).
		prog = append(prog, unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JGE | unix.BPF_K, K: seccompX32Bit})
	}
	for _, nr := range deniedSyscalls {
		prog = append(prog, unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: nr})
	}
	prog = append(prog, unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: seccompRetAllow}) // default allow
	prog = append(prog, denyInstr)                                                            // final deny

	denyIdx := len(prog) - 1
	for i := condStart; i < denyIdx-1; i++ { // every conditional jumps to the final deny on match
		prog[i].Jt = uint8(denyIdx - i - 1)
	}
	return prog
}

const (
	seccompRetAllow = 0x7fff0000
	seccompRetErrno = 0x00050000
)
