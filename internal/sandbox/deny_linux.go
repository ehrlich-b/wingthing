//go:build linux

package sandbox

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

// DenyInit is called early in main when the binary is re-exec'd as a sandbox
// deny-path wrapper. It runs as root (UID 0) inside the user namespace so it
// can mount tmpfs over denied paths. After mounting, it creates a nested user
// namespace to drop back to the real UID, then execs the agent command.
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

	// Drop from root to real UID via nested user namespace.
	// This prevents agents (e.g. Claude Code) from seeing euid==0 and
	// refusing --dangerously-skip-permissions.
	if uid != 0 {
		if err := syscall.Unshare(syscall.CLONE_NEWUSER); err != nil {
			log.Printf("_deny_init: unshare user ns: %v (continuing as root)", err)
		} else {
			// Must deny setgroups before writing gid_map (unprivileged user ns requirement)
			os.WriteFile("/proc/self/setgroups", []byte("deny"), 0)
			os.WriteFile("/proc/self/uid_map", []byte(fmt.Sprintf("%d 0 1\n", uid)), 0)
			os.WriteFile("/proc/self/gid_map", []byte(fmt.Sprintf("%d 0 1\n", gid)), 0)
		}
	}

	// Exec the real command — replaces this process.
	cmdArgs := args[cmdStart:]
	binary, err := exec.LookPath(cmdArgs[0])
	if err != nil {
		log.Fatalf("_deny_init: %v", err)
	}
	if err := syscall.Exec(binary, cmdArgs, os.Environ()); err != nil {
		log.Fatalf("_deny_init: exec %s: %v", binary, err)
	}
}
