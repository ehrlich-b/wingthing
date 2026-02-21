//go:build linux

package sandbox

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// DenyInit is called early in main when the binary is re-exec'd as a sandbox
// wrapper. It runs as root (UID 0) inside the user namespace so it can:
//  1. Mount tmpfs over denied paths to hide their contents
//  2. Apply write isolation: make HOME read-only, then bind writable sub-mounts
//  3. Install seccomp filter to prevent agent from undoing isolation
//
// After setup, it spawns the agent in a nested user namespace (CLONE_NEWUSER
// for UID drop) + PID namespace (CLONE_NEWPID for PID isolation). The wrapper
// itself is NOT in a PID namespace — this keeps host /proc valid so Go can
// write uid_map for the nested CLONE_NEWUSER without remounting /proc.
//
// Args format: --uid UID --gid GID [--log PATH] [--deny PATH...] [--home PATH] [--writable PATH...] [--overlay-prefix PREFIX...] -- CMD ARGS...
func DenyInit(args []string) {
	var denyPaths []string
	var denyWritePaths []string
	var writablePaths []string
	var overlayPrefixes []string
	var home string
	var logPath string
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
			case "--deny-write":
				denyWritePaths = append(denyWritePaths, args[i+1])
				i++
			case "--writable":
				writablePaths = append(writablePaths, args[i+1])
				i++
			case "--overlay-prefix":
				overlayPrefixes = append(overlayPrefixes, args[i+1])
				i++
			case "--home":
				home = args[i+1]
				i++
			case "--log":
				logPath = args[i+1]
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

	// Redirect logs to file so they don't leak into the agent's PTY.
	if logPath != "" {
		if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err == nil {
			log.SetOutput(f)
			defer f.Close()
		}
	}

	if cmdStart == 0 || cmdStart >= len(args) {
		log.Fatal("_deny_init: missing -- separator or command")
	}

	// Make all mounts in this namespace private so bind mounts don't
	// propagate back to the parent namespace. systemd sets "/" to shared
	// propagation by default, which causes every bind mount we create
	// here to leak into the host mount table (accumulating thousands of
	// stale mounts across egg sessions).
	if err := unix.Mount("", "/", "", unix.MS_PRIVATE|unix.MS_REC, ""); err != nil {
		log.Printf("_deny_init: make root private: %v", err)
	}

	// Write isolation: make HOME read-only, then punch writable holes.
	// Must happen BEFORE deny mounts so deny tmpfs overlays take precedence.
	// Skip if HOME itself is in the writable list (user wants full HOME rw).
	//
	// When overlay prefixes are present (e.g. ".claude"), use overlayfs on HOME
	// instead of simple bind-mount+RO. Overlayfs provides a copy-on-write layer
	// so new files can be created and renames work (needed for atomic writes).
	// Prefix-matching files are persisted back to the real HOME on exit.
	var overlayPersistFn func()
	tmpDir := filepath.Dir(logPath)
	if home != "" && len(writablePaths) > 0 && !containsPath(writablePaths, home) {
		if len(overlayPrefixes) > 0 {
			overlayPersistFn = setupOverlayHome(home, writablePaths, overlayPrefixes, tmpDir)
		}
		if overlayPersistFn == nil {
			// No overlay needed or overlay failed — fall back to bind-mount approach.
			setupReadonlyHome(home, writablePaths)
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

	// Deny-write paths — bind mount read-only so agent can read but not modify.
	for _, p := range denyWritePaths {
		if _, err := os.Stat(p); err != nil {
			continue // file doesn't exist, skip
		}
		if err := unix.Mount(p, p, "", unix.MS_BIND, ""); err != nil {
			log.Printf("_deny_init: bind deny-write %s: %v", p, err)
			continue
		}
		if err := unix.Mount("", p, "", unix.MS_REMOUNT|unix.MS_BIND|unix.MS_RDONLY, ""); err != nil {
			log.Printf("_deny_init: remount deny-write ro %s: %v", p, err)
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
		if overlayPersistFn != nil {
			overlayPersistFn()
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		log.Printf("_deny_init: wait: %v", err)
		os.Exit(1)
	}
	if overlayPersistFn != nil {
		overlayPersistFn()
	}
	os.Exit(0)
}

// setupOverlayHome mounts overlayfs on HOME so that new file creation and
// renames work for prefix-matching paths (e.g. .claude.json temp files).
// Writable dirs are bind-mounted through the overlay from real HOME so their
// writes persist immediately. On process exit, prefix-matching files from the
// overlay upper dir are copied back to real HOME.
// Returns a persist function, or nil if overlay setup failed.
func setupOverlayHome(home string, writablePaths, prefixes []string, tmpDir string) func() {
	// Save a reference to the real HOME before mounting overlay on top.
	realHome := filepath.Join(tmpDir, "real-home")
	if err := os.MkdirAll(realHome, 0755); err != nil {
		log.Printf("_deny_init: mkdir real-home: %v", err)
		return nil
	}
	if err := unix.Mount(home, realHome, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		log.Printf("_deny_init: bind real-home: %v", err)
		return nil
	}

	// Create overlay upper (COW layer) and work dirs.
	upperDir := filepath.Join(tmpDir, "overlay-upper")
	workDir := filepath.Join(tmpDir, "overlay-work")
	if err := os.MkdirAll(upperDir, 0755); err != nil {
		log.Printf("_deny_init: mkdir overlay-upper: %v", err)
		return nil
	}
	if err := os.MkdirAll(workDir, 0755); err != nil {
		log.Printf("_deny_init: mkdir overlay-work: %v", err)
		return nil
	}

	// Mount overlayfs on HOME. Lower layer is the real HOME (via saved ref).
	// Upper layer is the tmpdir COW — writes go here, real HOME is untouched.
	opts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", realHome, upperDir, workDir)
	if err := unix.Mount("overlay", home, "overlay", 0, opts); err != nil {
		log.Printf("_deny_init: overlay HOME: %v (falling back to bind-mount)", err)
		return nil
	}
	log.Printf("_deny_init: overlay HOME=%s upper=%s", home, upperDir)

	// Bind-mount writable dirs FROM real HOME through the overlay so their
	// writes persist immediately to the real filesystem (not just the COW layer).
	// If ANY bind-mount fails, tear down the overlay — running with ephemeral
	// auth state is worse than the old bind-mount approach (it can invalidate
	// OAuth tokens on the server side when the session ends).
	bindFailed := false
	for _, p := range writablePaths {
		if !strings.HasPrefix(p, home+string(filepath.Separator)) {
			continue
		}
		rel, err := filepath.Rel(home, p)
		if err != nil {
			continue
		}
		realPath := filepath.Join(realHome, rel)
		// Ensure mount target exists in the overlay merged view.
		if err := os.MkdirAll(p, 0755); err != nil {
			log.Printf("_deny_init: mkdir writable %s: %v", p, err)
			bindFailed = true
			break
		}
		if err := unix.Mount(realPath, p, "", unix.MS_BIND, ""); err != nil {
			log.Printf("_deny_init: bind writable %s: %v (aborting overlay)", p, err)
			bindFailed = true
			break
		}
		log.Printf("_deny_init: bind writable %s (persistent via %s)", p, realPath)
	}
	if bindFailed {
		// Tear down the overlay — unmount and fall back to setupReadonlyHome.
		unix.Unmount(home, 0)
		log.Printf("_deny_init: overlay aborted, falling back to bind-mount")
		return nil
	}

	// Return function that persists prefix-matching files from overlay upper
	// back to real HOME. Called after the agent process exits.
	return func() {
		entries, err := os.ReadDir(upperDir)
		if err != nil {
			return
		}
		for _, e := range entries {
			name := e.Name()
			matched := false
			for _, prefix := range prefixes {
				if strings.HasPrefix(name, prefix) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
			if e.IsDir() {
				// Persist directory contents if they ended up in the overlay
				// upper (shouldn't happen with working bind-mounts, but be safe).
				persistDir(filepath.Join(upperDir, name), filepath.Join(realHome, name))
				continue
			}
			src := filepath.Join(upperDir, name)
			dst := filepath.Join(realHome, name)
			if err := copyFile(src, dst); err != nil {
				log.Printf("_deny_init: persist %s: %v", name, err)
			} else {
				log.Printf("_deny_init: persisted %s from overlay", name)
			}
		}
	}
}

// setupReadonlyHome is the original write isolation approach: bind-mount HOME,
// punch writable holes for specific paths + prefix-matching files, then
// remount HOME read-only. Works for overwriting existing files but cannot
// handle new file creation or renames in HOME.
func setupReadonlyHome(home string, writablePaths []string) {
	if err := unix.Mount(home, home, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		log.Printf("_deny_init: bind HOME %s: %v (write isolation skipped)", home, err)
		return
	}

	// Bind-mount each writable path BEFORE remounting HOME read-only.
	for _, p := range writablePaths {
		if err := os.MkdirAll(p, 0755); err != nil {
			log.Printf("_deny_init: mkdir writable %s: %v", p, err)
			continue
		}
		if err := unix.Mount(p, p, "", unix.MS_BIND, ""); err != nil {
			log.Printf("_deny_init: bind writable %s: %v", p, err)
		}
	}

	// Bind-mount files adjacent to writable dirs that share the same prefix.
	// e.g., writable ~/.claude also makes ~/.claude.json writable.
	for _, p := range writablePaths {
		dir := filepath.Dir(p)
		base := filepath.Base(p)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if name == base || !strings.HasPrefix(name, base) {
				continue
			}
			if e.IsDir() {
				continue
			}
			fp := filepath.Join(dir, name)
			if err := unix.Mount(fp, fp, "", unix.MS_BIND, ""); err != nil {
				log.Printf("_deny_init: bind writable file %s: %v", fp, err)
			} else {
				log.Printf("_deny_init: bind writable file %s (prefix match)", fp)
			}
		}
	}

	// Remount HOME read-only. Child bind-mounts stay read-write.
	if err := unix.Mount("", home, "", unix.MS_REMOUNT|unix.MS_BIND|unix.MS_RDONLY, ""); err != nil {
		log.Printf("_deny_init: remount HOME ro: %v", err)
	} else {
		log.Printf("_deny_init: write isolation: HOME=%s ro, %d writable paths", home, len(writablePaths))
	}
}

// persistDir recursively copies directory contents from overlay upper to real HOME.
func persistDir(src, dst string) {
	entries, err := os.ReadDir(src)
	if err != nil {
		return
	}
	os.MkdirAll(dst, 0755)
	for _, e := range entries {
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dst, e.Name())
		if e.IsDir() {
			persistDir(s, d)
			continue
		}
		if err := copyFile(s, d); err != nil {
			log.Printf("_deny_init: persist %s: %v", d, err)
		} else {
			log.Printf("_deny_init: persisted %s from overlay", d)
		}
	}
}

// copyFile copies src to dst, preserving permissions.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
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

	log.Printf("_deny_init: seccomp installed (%d denied syscalls)", len(deniedSyscallsCommon)+len(deniedSyscallsArch))
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
