//go:build linux

package sandbox

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// DenyInit is called early in main when the binary is re-exec'd as a sandbox
// wrapper. It runs as root (UID 0) inside the user namespace so it can:
//   1. Mount tmpfs over denied paths to hide their contents
//   2. Apply write isolation: make HOME read-only, then bind writable sub-mounts
//   3. Install seccomp filter to prevent agent from undoing isolation
//
// After setup, it spawns the agent in a nested user namespace (CLONE_NEWUSER
// for UID drop) + PID namespace (CLONE_NEWPID for PID isolation). The wrapper
// itself is NOT in a PID namespace — this keeps host /proc valid so Go can
// write uid_map for the nested CLONE_NEWUSER without remounting /proc.
//
// Args format: --uid UID --gid GID [--deny PATH...] [--home PATH] [--writable PATH...] -- CMD ARGS...
func DenyInit(args []string) {
	var denyPaths []string
	var writablePaths []string
	var home string
	var uid, gid int
	var cmdStart int

	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			cmdStart = i + 1
			break
		}
		if i+1 < len(args) {
			switch args[i] {
			case "--deny":
				denyPaths = append(denyPaths, args[i+1])
				i++
			case "--writable":
				writablePaths = append(writablePaths, args[i+1])
				i++
			case "--home":
				home = args[i+1]
				i++
			case "--uid":
				uid, _ = strconv.Atoi(args[i+1])
				i++
			case "--gid":
				gid, _ = strconv.Atoi(args[i+1])
				i++
			}
		}
	}

	if cmdStart == 0 || cmdStart >= len(args) {
		log.Fatal("_deny_init: missing -- separator or command")
	}

	// Write isolation: make HOME read-only, then punch writable holes.
	// Must happen BEFORE deny mounts so deny tmpfs overlays take precedence.
	// Skip if HOME itself is in the writable list (user wants full HOME rw).
	if home != "" && len(writablePaths) > 0 && !containsPath(writablePaths, home) {
		// Bind-mount HOME to itself so we can remount it
		if err := unix.Mount(home, home, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
			log.Printf("_deny_init: bind HOME %s: %v (write isolation skipped)", home, err)
		} else {
			// Bind-mount each writable path BEFORE remounting HOME read-only.
			// These are independent mount points that survive the parent remount-ro.
			for _, p := range writablePaths {
				if err := os.MkdirAll(p, 0755); err != nil {
					log.Printf("_deny_init: mkdir writable %s: %v", p, err)
					continue
				}
				if err := unix.Mount(p, p, "", unix.MS_BIND, ""); err != nil {
					log.Printf("_deny_init: bind writable %s: %v", p, err)
				}
			}
			// Remount HOME read-only. Child bind-mounts at writable paths are
			// separate mount points and stay read-write.
			if err := unix.Mount("", home, "", unix.MS_REMOUNT|unix.MS_BIND|unix.MS_RDONLY, ""); err != nil {
				log.Printf("_deny_init: remount HOME ro: %v", err)
			} else {
				log.Printf("_deny_init: write isolation: HOME=%s ro, %d writable paths", home, len(writablePaths))
			}
		}
	}

	// Mount empty read-only tmpfs over each deny path to hide its contents.
	// We're UID 0 in the namespace -> have CAP_SYS_ADMIN -> can mount.
	for _, p := range denyPaths {
		if err := os.MkdirAll(p, 0755); err != nil {
			log.Printf("_deny_init: mkdir %s: %v", p, err)
			continue
		}
		if err := unix.Mount("tmpfs", p, "tmpfs", unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV, "size=0"); err != nil {
			log.Printf("_deny_init: mount deny %s: %v", p, err)
		}
	}

	// Install seccomp filter AFTER mounts (SYS_MOUNT is in the deny list).
	// This prevents the agent from undoing deny-path overmounts or write
	// isolation via mount/umount. The filter is inherited by child processes.
	if err := installSeccomp(); err != nil {
		log.Printf("_deny_init: seccomp: %v (continuing without)", err)
	}

	// Spawn agent with CLONE_NEWPID (PID isolation) + CLONE_NEWUSER (UID drop).
	// The wrapper is NOT in a PID namespace (parent strips CLONE_NEWPID for it),
	// so host /proc is valid and Go can write uid_map without remounting /proc.
	cmdArgs := args[cmdStart:]
	binPath := cmdArgs[0]

	// Debug: verify binary is accessible before exec
	if info, err := os.Lstat(binPath); err != nil {
		log.Printf("_deny_init: binary %s: %v", binPath, err)
	} else {
		log.Printf("_deny_init: binary %s mode=%s size=%d", binPath, info.Mode(), info.Size())
	}

	cmd := exec.Command(binPath, cmdArgs[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()

	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWPID,
	}
	if uid != 0 {
		cmd.SysProcAttr.Cloneflags |= syscall.CLONE_NEWUSER
		cmd.SysProcAttr.UidMappings = []syscall.SysProcIDMap{{
			ContainerID: uid,
			HostID:      0, // 0 in our namespace = real uid on host
			Size:        1,
		}}
		cmd.SysProcAttr.GidMappings = []syscall.SysProcIDMap{{
			ContainerID: gid,
			HostID:      0,
			Size:        1,
		}}
	}

	if err := cmd.Start(); err != nil {
		log.Fatalf("_deny_init: start agent: %v", err)
	}

	// Forward signals to child
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() {
		for sig := range sigCh {
			cmd.Process.Signal(sig)
		}
	}()

	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		log.Printf("_deny_init: wait: %v", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// installSeccomp installs a BPF seccomp filter that denies dangerous syscalls
// (mount, umount, ptrace, etc.). Must be called AFTER all mounts are complete.
// The filter is inherited by child processes via fork/exec.
func installSeccomp() error {
	prog := buildSeccompFilter()
	if prog == nil {
		return nil
	}

	// PR_SET_NO_NEW_PRIVS is required before installing seccomp filters.
	if _, _, errno := unix.RawSyscall(unix.SYS_PRCTL,
		unix.PR_SET_NO_NEW_PRIVS, 1, 0); errno != 0 {
		return fmt.Errorf("prctl(NO_NEW_PRIVS): %v", errno)
	}

	bpfProg := unix.SockFprog{
		Len:    uint16(len(prog)),
		Filter: &prog[0],
	}

	// SECCOMP_SET_MODE_FILTER = 1
	if _, _, errno := unix.RawSyscall(unix.SYS_SECCOMP,
		1, 0, uintptr(unsafe.Pointer(&bpfProg))); errno != 0 {
		return fmt.Errorf("seccomp(SET_MODE_FILTER): %v", errno)
	}

	log.Printf("_deny_init: seccomp installed (%d denied syscalls)", len(deniedSyscalls))
	return nil
}

// containsPath checks if the path list contains the given path.
func containsPath(paths []string, target string) bool {
	for _, p := range paths {
		if p == target {
			return true
		}
	}
	return false
}
