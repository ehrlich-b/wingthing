//go:build linux

package sandbox

import (
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
// deny-path wrapper. It runs as root (UID 0) inside the user namespace so it
// can mount tmpfs over denied paths. After mounting, it spawns the agent in a
// nested user namespace (via exec.Command + CLONE_NEWUSER) mapped to the real
// UID, so the agent doesn't see root.
//
// We can't use syscall.Unshare(CLONE_NEWUSER) because Go processes are
// multi-threaded; unshare requires single-threaded. Instead we use
// exec.Command which clones before the child has threads.
//
// Args format: --uid UID --gid GID --deny PATH [--deny PATH...] -- CMD ARGS...
func DenyInit(args []string) {
	var denyPaths []string
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

	// Mount empty read-only tmpfs over each deny path to hide its contents.
	// We're UID 0 in the namespace → have CAP_SYS_ADMIN → can mount.
	for _, p := range denyPaths {
		if err := os.MkdirAll(p, 0755); err != nil {
			log.Printf("_deny_init: mkdir %s: %v", p, err)
			continue
		}
		if err := unix.Mount("tmpfs", p, "tmpfs", unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV, "size=0"); err != nil {
			log.Printf("_deny_init: mount deny %s: %v", p, err)
		}
	}

	// Remount /proc for our PID namespace so Go's runtime can write
	// /proc/<child_pid>/uid_map for the nested CLONE_NEWUSER below.
	// Without this, /proc still shows host PIDs and the uid_map write fails,
	// causing execve to return ENOENT.
	if err := unix.Mount("proc", "/proc", "proc", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, ""); err != nil {
		log.Printf("_deny_init: remount /proc: %v (nested userns may fail)", err)
	}

	// Spawn agent in a new user namespace mapped to real UID.
	// exec.Command uses clone() which happens before the child has Go threads,
	// so CLONE_NEWUSER works (unlike syscall.Unshare which fails with EINVAL).
	cmdArgs := args[cmdStart:]
	binPath := cmdArgs[0]

	// Debug: verify binary is accessible before exec
	if info, err := os.Lstat(binPath); err != nil {
		log.Printf("_deny_init: binary %s: %v", binPath, err)
	} else {
		log.Printf("_deny_init: binary %s mode=%s size=%d", binPath, info.Mode(), info.Size())
		if info.Mode()&os.ModeSymlink != 0 {
			if target, err := os.Readlink(binPath); err == nil {
				log.Printf("_deny_init: symlink -> %s", target)
			}
		}
	}

	cmd := exec.Command(binPath, cmdArgs[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()

	if uid != 0 {
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Cloneflags: syscall.CLONE_NEWUSER,
			UidMappings: []syscall.SysProcIDMap{{
				ContainerID: uid,
				HostID:      0, // 0 in our namespace = real uid on host
				Size:        1,
			}},
			GidMappings: []syscall.SysProcIDMap{{
				ContainerID: gid,
				HostID:      0,
				Size:        1,
			}},
		}
	}

	// Install seccomp filter before starting the agent. PR_SET_NO_NEW_PRIVS
	// is required before seccomp and the filter is inherited by the child.
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		log.Printf("_deny_init: PR_SET_NO_NEW_PRIVS: %v (seccomp skipped)", err)
	} else {
		filter := buildSeccompFilter()
		if len(filter) > 0 {
			prog := unix.SockFprog{
				Len:    uint16(len(filter)),
				Filter: &filter[0],
			}
			if err := seccompSetModeFilter(&prog); err != nil {
				log.Printf("_deny_init: seccomp: %v (continuing without filter)", err)
			} else {
				log.Printf("_deny_init: seccomp filter installed (%d instructions)", len(filter))
			}
		}
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

// seccompSetModeFilter installs a seccomp BPF filter via the seccomp syscall.
func seccompSetModeFilter(prog *unix.SockFprog) error {
	const seccompSetModeFilterOp = 1 // SECCOMP_SET_MODE_FILTER
	_, _, errno := unix.Syscall(unix.SYS_SECCOMP, seccompSetModeFilterOp, 0, uintptr(unsafe.Pointer(prog)))
	if errno != 0 {
		return errno
	}
	return nil
}
