//go:build linux

package sandbox

import (
	"bufio"
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

const jailAgentInitArg = "_jail_agent_init"
const jailAgentDropArg = "_jail_agent_drop"

// The sealed jail runs the agent through two nested user-namespace stages so it
// can both swap /proc AND run unprivileged:
//
//   _deny_init  -> clones _jail_agent_init into a new PID+user+mount namespace,
//                  mapped to inner-UID 0 so it keeps CAP_SYS_ADMIN across execve.
//   _jail_agent_init (stage 2, inner-root): replaces the temporarily visible
//                  host procfs with one owned by this PID namespace, then clones
//                  _jail_agent_drop into a further nested user namespace that
//                  maps back to the real nonzero uid.
//   _jail_agent_drop (stage 3, nonzero uid): installs seccomp and execs the
//                  agent. It has no capabilities over the mount namespace, so the
//                  sealed filesystem and private procfs stand, and Claude's
//                  --dangerously-skip-permissions root guard is satisfied.
//
// Both re-execs pass the real uid/gid as argv so the drop stage knows its map.
// Arg layout: <stage-arg> <uid> <gid> -- <command...>.
func init() {
	if len(os.Args) < 6 || os.Args[4] != "--" {
		return
	}
	switch os.Args[1] {
	case jailAgentInitArg:
		jailAgentInit(os.Args[2], os.Args[3], os.Args[5:])
	case jailAgentDropArg:
		jailAgentDrop(os.Args[2], os.Args[3], os.Args[5:])
	}
}

// jailAgentInit (stage 2) runs as inner-UID 0, so it still holds CAP_SYS_ADMIN
// over its mount namespace and can swap /proc. It never execs the agent itself:
// after the swap it clones jailAgentDrop into a nested user namespace that maps
// back to the real nonzero uid, so the agent runs unprivileged.
func jailAgentInit(uidStr, gidStr string, command []string) {
	if len(command) == 0 || command[0] == "" {
		log.Fatal("_jail_agent_init: missing command")
	}
	// Detaching the host procfs is preferred, but a kernel may refuse it when
	// the bound tree carries locked host submounts (binfmt_misc on 5.15).
	// Mounting the PID-namespace procfs on top is equally sound: the host
	// procfs is left shadowed and unreachable, the seccomp filter installed by
	// the drop stage denies the agent mount and umount, and verifyPrivateProcfs
	// asserts the visible /proc belongs to this PID namespace either way.
	if err := unix.Unmount("/proc", unix.MNT_DETACH); err != nil {
		log.Printf("_jail_agent_init: detach host procfs refused (%v); overmounting PID-namespace procfs", err)
	}
	if err := unix.Mount("proc", "/proc", "proc", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, ""); err != nil {
		failEnforcement("mount PID-namespace procfs", "/proc", err)
	}
	if err := verifyPrivateProcfs(); err != nil {
		failEnforcement("verify PID-namespace procfs", "/proc/self/status", err)
	}

	// Drop to the real nonzero uid before the agent runs. We cannot
	// unshare(CLONE_NEWUSER) in-process (the Go runtime is multithreaded), so
	// re-exec with the clone flag. The nested user namespace maps our inner-UID
	// 0 to the real uid; the drop stage keeps this PID + mount namespace (and so
	// the private procfs), but holds no capability over the mount namespace.
	uid, err := strconv.Atoi(uidStr)
	if err != nil {
		log.Fatalf("_jail_agent_init: bad uid %q: %v", uidStr, err)
	}
	gid, err := strconv.Atoi(gidStr)
	if err != nil {
		log.Fatalf("_jail_agent_init: bad gid %q: %v", gidStr, err)
	}
	dropArgs := append([]string{jailAgentDropArg, uidStr, gidStr, "--"}, command...)
	drop := exec.Command("/proc/self/exe", dropArgs...)
	drop.Stdin = os.Stdin
	drop.Stdout = os.Stdout
	drop.Stderr = os.Stderr
	drop.Env = os.Environ()
	drop.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: uid, HostID: 0, Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: gid, HostID: 0, Size: 1}},
	}
	if err := drop.Start(); err != nil {
		log.Fatalf("_jail_agent_init: start drop stage: %v", err)
	}
	// This process is PID 1 of the jail's PID namespace; act as a minimal init —
	// forward termination signals to the agent and exit with its status.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() {
		for sig := range sigCh {
			drop.Process.Signal(sig)
		}
	}()
	if err := drop.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		log.Fatalf("_jail_agent_init: wait drop stage: %v", err)
	}
	os.Exit(0)
}

// jailAgentDrop (stage 3) runs as the real nonzero uid in a nested user
// namespace with no capability over the mount namespace. The sealed filesystem
// and the private procfs the parent installed both stand; it only clamps
// syscalls and execs the agent.
func jailAgentDrop(uidStr, gidStr string, command []string) {
	if len(command) == 0 || command[0] == "" {
		log.Fatal("_jail_agent_drop: missing command")
	}
	logOutput := log.Writer()
	log.SetOutput(io.Discard)
	seccompErr := installSeccomp()
	log.SetOutput(logOutput)
	if seccompErr != nil {
		failEnforcement("install seccomp", "agent process", seccompErr)
	}
	if err := syscall.Exec(command[0], command, os.Environ()); err != nil {
		log.Fatalf("_jail_agent_drop: exec agent: %v", err)
	}
}

func verifyPrivateProcfs() error {
	file, err := os.Open("/proc/self/status")
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "NSpid:") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "NSpid:"))
		if len(fields) != 1 || fields[0] != "1" {
			return fmt.Errorf("agent init is not PID 1 in a private procfs (NSpid=%q)", fields)
		}
		return nil
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return fmt.Errorf("NSpid is unavailable")
}

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
// Args format: --uid UID --gid GID [--log PATH] [--deny PATH...] [--home PATH] [--writable PATH...] [--mount-ro PATH...] [--overlay-prefix PREFIX...] -- CMD ARGS...
func DenyInit(args []string) {
	var denyPaths []string
	var denyWritePaths []string
	var writablePaths []string
	var overlayPrefixes []string
	var roMounts []string
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
			case "--mount-ro":
				roMounts = append(roMounts, args[i+1])
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
		failEnforcement("make mount namespace private", "/", err)
	}

	tmpDir := filepath.Dir(logPath)

	// Jail mode: deny:/ creates an allowlist filesystem. Only explicitly
	// mounted paths are visible; everything else is inaccessible.
	jailMode := containsPath(denyPaths, "/")
	if jailMode {
		setupJail(tmpDir, roMounts, writablePaths, home)
		var filtered []string
		for _, d := range denyPaths {
			if d != "/" {
				filtered = append(filtered, d)
			}
		}
		denyPaths = filtered
	}

	// Deny mounts need a concrete mountpoint. Prepare absent paths while their
	// parent is still writable; write isolation below may remount HOME read-only.
	// The later mount and mount-table verification remain mandatory, so an
	// existing secret path can never pass through after a masking failure.
	if operation, path, err := prepareDenyMountpoints(denyPaths); err != nil {
		failEnforcement(operation, path, err)
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
	if !jailMode && home != "" && len(writablePaths) > 0 && !containsPath(writablePaths, home) {
		if len(overlayPrefixes) > 0 {
			overlayPersistFn = setupOverlayHome(home, writablePaths, overlayPrefixes, tmpDir)
		}
		if overlayPersistFn == nil {
			// No overlay needed or overlay failed — fall back to bind-mount approach.
			if err := setupReadonlyHome(home, writablePaths); err != nil {
				failEnforcement("isolate HOME writes", home, err)
			}
		}
	}

	// Mount empty read-only tmpfs over each deny path to hide its contents.
	// We're UID 0 in the namespace -> have CAP_SYS_ADMIN -> can mount.
	var expectedMounts []expectedMount
	for _, p := range denyPaths {
		// Stat to determine if path is a file or directory. Files can't
		// be overmounted with tmpfs — bind-mount /dev/null instead.
		info, statErr := os.Lstat(p)
		if statErr != nil {
			failEnforcement("inspect deny path", p, statErr)
		}
		if !info.IsDir() {
			// Regular file (or symlink): bind-mount /dev/null over it.
			if err := unix.Mount("/dev/null", p, "", unix.MS_BIND, ""); err != nil {
				failEnforcement("mask denied file", p, err)
			}
			// Remount read-only so agent can't write to it. Preserve the bind
			// mount's existing VFS flags: WSL rejects a remount that implicitly
			// drops flags such as nosuid or relatime even though Linux commonly
			// accepts the shorter MS_REMOUNT|MS_BIND|MS_RDONLY form.
			if err := remountBindReadonly(p); err != nil {
				failEnforcement("make denied file mask read-only", p, err)
			}
			expectedMounts = append(expectedMounts, expectedMount{Path: p, ReadOnly: true})
			log.Printf("_deny_init: deny file %s (bind /dev/null)", p)
			continue
		}

		// Preserve ~/.ssh/known_hosts so SSH can verify host keys without
		// prompting. The prompt writes to /dev/tty and interleaves with
		// agent output, producing garbled text. Skipped if known_hosts
		// is also explicitly denied (deny: ~/.ssh/known_hosts).
		var knownHosts []byte
		sshDir := filepath.Join(os.Getenv("HOME"), ".ssh")
		khPath := filepath.Join(sshDir, "known_hosts")
		if p == sshDir && !containsPath(denyPaths, khPath) {
			knownHosts, _ = os.ReadFile(khPath)
		}

		if knownHosts != nil {
			// Mount writable tmpfs, write known_hosts, remount read-only.
			if err := unix.Mount("tmpfs", p, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "size=65536"); err != nil {
				failEnforcement("mask denied directory", p, err)
			}
			khPath := filepath.Join(p, "known_hosts")
			if err := os.WriteFile(khPath, knownHosts, 0644); err != nil {
				failEnforcement("preserve SSH known_hosts", khPath, err)
			}
			if err := unix.Mount("", p, "", unix.MS_REMOUNT|unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV, "size=65536"); err != nil {
				failEnforcement("make denied directory mask read-only", p, err)
			}
		} else {
			if err := unix.Mount("tmpfs", p, "tmpfs", unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV, "size=0"); err != nil {
				failEnforcement("mask denied directory", p, err)
			}
		}
		expectedMounts = append(expectedMounts, expectedMount{Path: p, FSType: "tmpfs", ReadOnly: true})
	}

	// Deny-write paths — bind mount read-only so agent can read but not modify.
	for _, p := range denyWritePaths {
		if _, err := os.Stat(p); err != nil {
			if os.IsNotExist(err) {
				log.Printf("_deny_init: deny-write path absent at launch: %s", p)
				continue
			}
			failEnforcement("inspect deny-write path", p, err)
		}
		if err := unix.Mount(p, p, "", unix.MS_BIND, ""); err != nil {
			failEnforcement("bind deny-write path", p, err)
		}
		if err := remountBindReadonly(p); err != nil {
			failEnforcement("make deny-write path read-only", p, err)
		}
		expectedMounts = append(expectedMounts, expectedMount{Path: p, ReadOnly: true})
	}

	// Syscall success is necessary but the live mount table is the security
	// boundary. Verify every requested mask before seccomp and before the agent
	// process exists. This catches LSM behavior and partial setup bugs that leave
	// the resolved policy looking correct while the namespace is still readable.
	if err := verifyExpectedMounts(expectedMounts); err != nil {
		failEnforcement("verify filesystem policy", "/proc/self/mountinfo", err)
	}

	// Install seccomp after mounts. Jail mode delegates this to the PID-namespace
	// init, which must replace the temporarily visible host procfs first.
	if !jailMode {
		if err := installSeccomp(); err != nil {
			failEnforcement("install seccomp", "agent process", err)
		}
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
	if jailMode {
		// /proc/self/exe remains executable after pivot_root even when the wt
		// binary's original host path is outside the jail allowlist. Carry the
		// outer wrapper's PATH resolution into the jail because syscall.Exec does
		// not search PATH and the host-side agent binary itself is intentionally
		// absent there.
		resolvedCommand := append([]string{cmd.Path}, cmdArgs[1:]...)
		initArgs := append([]string{jailAgentInitArg, strconv.Itoa(uid), strconv.Itoa(gid), "--"}, resolvedCommand...)
		cmd = exec.Command("/proc/self/exe", initArgs...)
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()

	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWPID,
	}
	if uid != 0 {
		// CLONE_NEWNS must accompany CLONE_NEWUSER: the nested user namespace
		// holds no capabilities over the wrapper's mount namespace, so without
		// its own namespace the PID-namespace init cannot swap /proc at all —
		// every mount call fails EPERM (observed on 5.15 shared hosts; masked
		// on root runs, which skip CLONE_NEWUSER and keep full capabilities).
		cmd.SysProcAttr.Cloneflags |= syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS
		// Jail mode maps the init to inner-UID 0 so it keeps CAP_SYS_ADMIN across
		// execve and can swap /proc; _jail_agent_init then drops to the real
		// nonzero uid via a further nested user namespace before the agent runs.
		// Non-jail mode has no procfs swap and runs the agent directly, so it
		// maps straight to the real uid.
		initUID, initGID := uid, gid
		if jailMode {
			initUID, initGID = 0, 0
		}
		cmd.SysProcAttr.UidMappings = []syscall.SysProcIDMap{{
			ContainerID: initUID,
			HostID:      0, // 0 in our namespace = real uid on host
			Size:        1,
		}}
		cmd.SysProcAttr.GidMappings = []syscall.SysProcIDMap{{
			ContainerID: initGID,
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

func prepareDenyMountpoints(paths []string) (operation, path string, err error) {
	for _, path := range paths {
		if _, statErr := os.Lstat(path); statErr == nil {
			continue
		} else if !os.IsNotExist(statErr) {
			return "inspect deny path", path, statErr
		}
		if mkdirErr := os.MkdirAll(path, 0o755); mkdirErr != nil {
			return "create deny mountpoint", path, mkdirErr
		}
		if _, statErr := os.Lstat(path); statErr != nil {
			return "inspect created deny path", path, statErr
		}
		log.Printf("_deny_init: prepared absent deny mountpoint %s", path)
	}
	return "", "", nil
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
	expected := []expectedMount{{Path: home, FSType: "overlay", Writable: true}}
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
		expected = append(expected, expectedMount{Path: p, Writable: true})
		log.Printf("_deny_init: bind writable %s (persistent via %s)", p, realPath)
	}
	if !bindFailed {
		if err := verifyExpectedMounts(expected); err != nil {
			log.Printf("_deny_init: verify overlay HOME: %v (aborting overlay)", err)
			bindFailed = true
		}
	}
	if bindFailed {
		// Tear down the overlay — unmount and fall back to setupReadonlyHome.
		if err := unix.Unmount(home, 0); err != nil {
			failEnforcement("remove incomplete HOME overlay", home, err)
		}
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
			// Remove symlinks at dst so we don't follow them and write
			// outside the per-user home (e.g. stale symlink to /opt/wingthing/.claude.json).
			if fi, err := os.Lstat(dst); err == nil && fi.Mode()&os.ModeSymlink != 0 {
				os.Remove(dst)
			}
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
func setupReadonlyHome(home string, writablePaths []string) error {
	if err := unix.Mount(home, home, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return fmt.Errorf("bind HOME: %w", err)
	}

	// Bind-mount each writable path BEFORE remounting HOME read-only.
	var expected []expectedMount
	for _, p := range writablePaths {
		if err := os.MkdirAll(p, 0755); err != nil {
			return fmt.Errorf("create writable mountpoint %s: %w", p, err)
		}
		if err := unix.Mount(p, p, "", unix.MS_BIND, ""); err != nil {
			return fmt.Errorf("bind writable path %s: %w", p, err)
		}
		expected = append(expected, expectedMount{Path: p, Writable: true})
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
				return fmt.Errorf("bind writable prefix file %s: %w", fp, err)
			}
			expected = append(expected, expectedMount{Path: fp, Writable: true})
			log.Printf("_deny_init: bind writable file %s (prefix match)", fp)
		}
	}

	// Remount HOME read-only. Child bind-mounts stay read-write.
	if err := remountBindReadonly(home); err != nil {
		return fmt.Errorf("remount HOME read-only: %w", err)
	}
	expected = append(expected, expectedMount{Path: home, ReadOnly: true})
	if err := verifyExpectedMounts(expected); err != nil {
		return fmt.Errorf("verify HOME write isolation: %w", err)
	}
	log.Printf("_deny_init: write isolation: HOME=%s ro, %d writable paths", home, len(writablePaths))
	return nil
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
		// Remove symlinks so we don't follow them outside per-user home.
		if fi, err := os.Lstat(d); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			os.Remove(d)
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

type expectedMount struct {
	Path     string
	FSType   string
	ReadOnly bool
	Writable bool
}

type mountInfoEntry struct {
	FSType  string
	Options map[string]bool
}

// remountBindReadonly changes only the bind mount's read-only state while
// preserving its current per-mount flags. Passing a minimal flag set works on
// many Linux kernels, but WSL returns EPERM when a remount would implicitly
// discard flags inherited from the source mount.
func remountBindReadonly(path string) error {
	entries, err := readMountInfo("/proc/self/mountinfo")
	if err != nil {
		return err
	}
	entry, ok := effectiveMountEntry(entries, path)
	if !ok {
		return fmt.Errorf("bind mount missing at %s", path)
	}
	flags := uintptr(unix.MS_REMOUNT | unix.MS_BIND | unix.MS_RDONLY)
	flags |= mountFlagsFromOptions(entry.Options)
	return unix.Mount("", path, "", flags, "")
}

func mountFlagsFromOptions(options map[string]bool) uintptr {
	known := []struct {
		option string
		flag   uintptr
	}{
		{"nosuid", unix.MS_NOSUID},
		{"nodev", unix.MS_NODEV},
		{"noexec", unix.MS_NOEXEC},
		{"sync", unix.MS_SYNCHRONOUS},
		{"mand", unix.MS_MANDLOCK},
		{"dirsync", unix.MS_DIRSYNC},
		{"nosymfollow", unix.MS_NOSYMFOLLOW},
		{"noatime", unix.MS_NOATIME},
		{"nodiratime", unix.MS_NODIRATIME},
		{"relatime", unix.MS_RELATIME},
		{"iversion", unix.MS_I_VERSION},
		{"strictatime", unix.MS_STRICTATIME},
		{"lazytime", unix.MS_LAZYTIME},
	}
	var flags uintptr
	for _, candidate := range known {
		if options[candidate.option] {
			flags |= candidate.flag
		}
	}
	return flags
}

func effectiveMountEntry(entries map[string]mountInfoEntry, path string) (mountInfoEntry, bool) {
	clean := filepath.Clean(path)
	entry, ok := entries[clean]
	if ok {
		return entry, true
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return mountInfoEntry{}, false
	}
	entry, ok = entries[filepath.Clean(resolved)]
	return entry, ok
}

// verifyExpectedMounts reads the effective mount table in the sandbox child.
// It deliberately verifies kernel state rather than the policy struct or the
// sequence of successful-looking mount calls.
func verifyExpectedMounts(expected []expectedMount) error {
	if len(expected) == 0 {
		return nil
	}
	entries, err := readMountInfo("/proc/self/mountinfo")
	if err != nil {
		return err
	}
	return verifyMountEntries(entries, expected)
}

func verifyMountEntries(entries map[string]mountInfoEntry, expected []expectedMount) error {
	for _, want := range expected {
		got, ok := effectiveMountEntry(entries, want.Path)
		if !ok {
			return fmt.Errorf("required mount missing at %s", want.Path)
		}
		if want.FSType != "" && got.FSType != want.FSType {
			return fmt.Errorf("mount at %s uses %s, expected %s", want.Path, got.FSType, want.FSType)
		}
		if want.ReadOnly && !got.Options["ro"] {
			return fmt.Errorf("mount at %s is writable", want.Path)
		}
		if want.Writable && !got.Options["rw"] {
			return fmt.Errorf("mount at %s is read-only", want.Path)
		}
	}
	return nil
}

func readMountInfo(path string) (map[string]mountInfoEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read mount table: %w", err)
	}
	entries := make(map[string]mountInfoEntry)
	for lineNo, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 10 {
			return nil, fmt.Errorf("parse mount table line %d: too few fields", lineNo+1)
		}
		separator := -1
		for i := 6; i < len(fields); i++ {
			if fields[i] == "-" {
				separator = i
				break
			}
		}
		if separator < 0 || separator+2 >= len(fields) {
			return nil, fmt.Errorf("parse mount table line %d: missing separator", lineNo+1)
		}
		options := make(map[string]bool)
		for _, option := range strings.Split(fields[5], ",") {
			options[option] = true
		}
		mountPoint := unescapeMountInfoPath(fields[4])
		entries[filepath.Clean(mountPoint)] = mountInfoEntry{
			FSType:  fields[separator+1],
			Options: options,
		}
	}
	return entries, nil
}

func unescapeMountInfoPath(path string) string {
	return strings.NewReplacer(
		`\040`, " ",
		`\011`, "\t",
		`\012`, "\n",
		`\134`, `\`,
	).Replace(path)
}

func failEnforcement(operation, path string, err error) {
	profile := currentSecurityProfile()
	if profile == "" {
		profile = "unreported"
	}
	log.Fatalf("_deny_init: filesystem enforcement failed: %s %q: %v (security profile: %s); refusing to launch agent",
		operation, path, err, profile)
}

func currentSecurityProfile() string {
	data, err := os.ReadFile("/proc/self/attr/current")
	if err != nil {
		return ""
	}
	return strings.Trim(string(data), " \t\r\n\x00")
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

	// Apply the filter to every Go runtime thread. Without TSYNC, only the
	// calling OS thread is filtered and a later fork/exec can silently inherit
	// an unfiltered thread's state. On a TSYNC synchronization failure Linux may
	// return the first unsynchronized thread ID as a positive result with errno 0.
	result, _, errno := unix.RawSyscall(unix.SYS_SECCOMP,
		unix.SECCOMP_SET_MODE_FILTER, unix.SECCOMP_FILTER_FLAG_TSYNC, uintptr(unsafe.Pointer(&bpfProg)))
	if errno != 0 {
		return fmt.Errorf("seccomp(SET_MODE_FILTER): %v", errno)
	}
	if result != 0 {
		return fmt.Errorf("seccomp(SET_MODE_FILTER|TSYNC): thread %d was not synchronized", result)
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

// setupJail creates an allowlist filesystem using pivot_root. Starting from an
// empty tmpfs root, it bind-mounts only the paths in roMounts (read-only) and
// writablePaths (read-write), plus essential virtual filesystems (/proc, /dev,
// /tmp). After pivot_root, the old root is lazily unmounted — nothing outside
// the explicit mounts is accessible.
func setupJail(tmpDir string, roMounts, writablePaths []string, home string) {
	newRoot := filepath.Join(tmpDir, "newroot")
	if err := os.MkdirAll(newRoot, 0755); err != nil {
		log.Fatalf("_deny_init: jail mkdir newroot: %v", err)
	}
	if err := unix.Mount("tmpfs", newRoot, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "size=64m"); err != nil {
		log.Fatalf("_deny_init: jail mount newroot: %v", err)
	}
	// Recreate merged-usr symlinks (/bin -> usr/bin, etc.) if the host uses them.
	for _, link := range [][2]string{
		{"bin", "usr/bin"}, {"sbin", "usr/sbin"}, {"lib", "usr/lib"}, {"lib64", "usr/lib64"},
	} {
		if target, err := os.Readlink("/" + link[0]); err == nil {
			if err := os.Symlink(target, filepath.Join(newRoot, link[0])); err != nil {
				failEnforcement("recreate jail symlink", "/"+link[0], err)
			}
			log.Printf("_deny_init: jail symlink /%s -> %s", link[0], target)
		}
	}
	// Essential virtual filesystems FIRST — user bind-mounts may land on top
	// of these (e.g. a writable path under /tmp).
	// Bind-mount host /proc temporarily. The outer wrapper needs host PIDs while
	// Go writes the nested user namespace's uid_map. The nested PID-namespace
	// init replaces this mount before it executes the agent.
	//
	// The bind must be recursive: the host's binfmt_misc autofs under
	// /proc/sys/fs is a locked submount in this user namespace, and a
	// non-recursive bind that would expose what locked mounts cover is
	// refused (EPERM). Keeping the children also keeps procfs "fully
	// visible", which the kernel requires before it lets the PID-namespace
	// init mount a fresh proc.
	procPath := filepath.Join(newRoot, "proc")
	if err := os.MkdirAll(procPath, 0555); err != nil {
		failEnforcement("create jail /proc", "/proc", err)
	}
	if err := unix.Mount("/proc", procPath, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		failEnforcement("bind jail /proc", "/proc", err)
	}
	devPath := filepath.Join(newRoot, "dev")
	if err := os.MkdirAll(devPath, 0755); err != nil {
		failEnforcement("create jail /dev", "/dev", err)
	}
	if err := unix.Mount("tmpfs", devPath, "tmpfs", unix.MS_NOSUID, "size=65536,mode=755"); err != nil {
		failEnforcement("mount jail /dev", "/dev", err)
	}
	for _, dev := range []string{"null", "zero", "urandom", "tty", "random"} {
		dp := filepath.Join(devPath, dev)
		f, err := os.Create(dp)
		if err != nil {
			failEnforcement("create jail device mountpoint", "/dev/"+dev, err)
		}
		if err := f.Close(); err != nil {
			failEnforcement("close jail device mountpoint", "/dev/"+dev, err)
		}
		if err := unix.Mount("/dev/"+dev, dp, "", unix.MS_BIND, ""); err != nil {
			failEnforcement("bind jail device", "/dev/"+dev, err)
		}
	}
	shmPath := filepath.Join(devPath, "shm")
	if err := os.MkdirAll(shmPath, 01777); err != nil {
		failEnforcement("create jail /dev/shm", "/dev/shm", err)
	}
	if err := unix.Mount("tmpfs", shmPath, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "size=64m"); err != nil {
		failEnforcement("mount jail /dev/shm", "/dev/shm", err)
	}
	tmpPath := filepath.Join(newRoot, "tmp")
	if err := os.MkdirAll(tmpPath, 01777); err != nil {
		failEnforcement("create jail /tmp", "/tmp", err)
	}
	if err := unix.Mount("tmpfs", tmpPath, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "size=1g"); err != nil {
		failEnforcement("mount jail /tmp", "/tmp", err)
	}
	// Recreate tmpDir inside jail so HOME/TMPDIR env vars resolve.
	if err := os.MkdirAll(filepath.Join(newRoot, tmpDir), 0755); err != nil {
		failEnforcement("create sandbox temp directory inside jail", tmpDir, err)
	}
	// Bind-mount read-only paths from real root.
	expected := []expectedMount{
		{Path: "/", FSType: "tmpfs", Writable: true},
		{Path: "/proc"},
		{Path: "/dev", FSType: "tmpfs", Writable: true},
		{Path: "/dev/shm", FSType: "tmpfs", Writable: true},
		{Path: "/tmp", FSType: "tmpfs", Writable: true},
	}
	for _, p := range roMounts {
		target := filepath.Join(newRoot, p)
		if err := jailMkTarget(p, target); err != nil {
			failEnforcement("create jail read-only mountpoint", p, err)
		}
		if err := unix.Mount(p, target, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
			failEnforcement("bind jail read-only path", p, err)
		}
		if err := remountBindReadonly(target); err != nil {
			failEnforcement("make jail path read-only", p, err)
		}
		expected = append(expected, expectedMount{Path: p, ReadOnly: true})
		log.Printf("_deny_init: jail ro %s", p)
	}
	// Bind-mount writable paths (order matters: rw mounts override ro parents).
	for _, p := range writablePaths {
		// HOME is mounted as one persistent owner-scoped tree below. Mounting a
		// child separately is redundant and would follow a user-created symlink
		// on the host side before pivot_root.
		if home != "" && isPathWithin(p, home) {
			continue
		}
		target := filepath.Join(newRoot, p)
		if err := jailMkTarget(p, target); err != nil {
			failEnforcement("create jail writable mountpoint", p, err)
		}
		if err := unix.Mount(p, target, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
			failEnforcement("bind jail writable path", p, err)
		}
		expected = append(expected, expectedMount{Path: p, Writable: true})
		log.Printf("_deny_init: jail rw %s", p)
	}
	// Bind-mount home directory (writable).
	if home != "" {
		target := filepath.Join(newRoot, home)
		if err := os.MkdirAll(target, 0755); err != nil {
			failEnforcement("create jail HOME mountpoint", home, err)
		} else if err := unix.Mount(home, target, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
			failEnforcement("bind jail HOME", home, err)
		} else {
			expected = append(expected, expectedMount{Path: home, Writable: true})
			log.Printf("_deny_init: jail home %s", home)
		}
	}
	// pivot_root: swap new root into place, old root at .pivot.
	// Save cwd so we can restore it after pivot (cmd.Dir set by parent).
	origCwd, _ := os.Getwd()
	pivotDir := filepath.Join(newRoot, ".pivot")
	if err := os.MkdirAll(pivotDir, 0700); err != nil {
		failEnforcement("create pivot directory", pivotDir, err)
	}
	if err := unix.PivotRoot(newRoot, pivotDir); err != nil {
		failEnforcement("activate jail root", "/", err)
	}
	if origCwd != "" {
		if err := os.Chdir(origCwd); err != nil {
			failEnforcement("restore working directory inside jail", origCwd, err)
		}
	} else {
		if err := os.Chdir("/"); err != nil {
			failEnforcement("enter jail root", "/", err)
		}
	}
	if err := unix.Unmount("/.pivot", unix.MNT_DETACH); err != nil {
		failEnforcement("detach old root", "/.pivot", err)
	}
	if err := os.Remove("/.pivot"); err != nil {
		failEnforcement("remove old root mountpoint", "/.pivot", err)
	}
	if err := verifyExpectedMounts(expected); err != nil {
		failEnforcement("verify jail filesystem policy", "/proc/self/mountinfo", err)
	}
	log.Printf("_deny_init: jail active (ro=%d rw=%d home=%s)", len(roMounts), len(writablePaths), home)
}

func isPathWithin(path, root string) bool {
	cleanPath := filepath.Clean(path)
	cleanRoot := filepath.Clean(root)
	return cleanPath == cleanRoot || strings.HasPrefix(cleanPath, cleanRoot+string(filepath.Separator))
}

// jailMkTarget creates the bind-mount target inside the jail root.
// For directories it creates the full path; for files/sockets it creates the
// parent directory and an empty file as the mount point.
func jailMkTarget(src, target string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return os.MkdirAll(target, 0755)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	f, err := os.Create(target)
	if err != nil {
		return err
	}
	return f.Close()
}
