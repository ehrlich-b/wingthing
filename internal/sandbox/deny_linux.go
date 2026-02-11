//go:build linux

package sandbox

import (
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

// DenyInit is called early in main when the binary is re-exec'd as a sandbox
// wrapper. It runs as root (UID 0) inside the user namespace so it can:
//   1. Mount tmpfs over denied paths to hide their contents
//   2. Apply write isolation: make HOME read-only, then bind writable sub-mounts
// After setup, it spawns the agent in a nested user namespace (via
// exec.Command + CLONE_NEWUSER) mapped to the real UID.
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

// containsPath checks if the path list contains the given path.
func containsPath(paths []string, target string) bool {
	for _, p := range paths {
		if p == target {
			return true
		}
	}
	return false
}
