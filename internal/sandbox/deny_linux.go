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

	// Spawn agent in a new user namespace mapped to real UID.
	// exec.Command uses clone() which happens before the child has Go threads,
	// so CLONE_NEWUSER works (unlike syscall.Unshare which fails with EINVAL).
	cmdArgs := args[cmdStart:]
	cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
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
