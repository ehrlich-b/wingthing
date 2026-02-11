//go:build linux

package sandbox

import (
	"log"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// DenyInit is called early in main when the binary is re-exec'd as a sandbox
// deny-path wrapper. It mounts empty read-only tmpfs over each denied path
// (hiding its contents), then execs the real command.
// This runs inside a user+mount namespace where we have CAP_SYS_ADMIN.
func DenyInit(args []string) {
	var denyPaths []string
	var cmdStart int

	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			cmdStart = i + 1
			break
		}
		if args[i] == "--deny" && i+1 < len(args) {
			denyPaths = append(denyPaths, args[i+1])
			i++
		}
	}

	if cmdStart == 0 || cmdStart >= len(args) {
		log.Fatal("_deny_init: missing -- separator or command")
	}

	// Mount empty read-only tmpfs over each deny path to hide its contents.
	for _, p := range denyPaths {
		if err := os.MkdirAll(p, 0755); err != nil {
			log.Printf("_deny_init: mkdir %s: %v", p, err)
			continue
		}
		if err := unix.Mount("tmpfs", p, "tmpfs", unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV, "size=0"); err != nil {
			log.Printf("_deny_init: mount deny %s: %v", p, err)
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
