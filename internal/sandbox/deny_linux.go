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
	"sort"
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
//	_deny_init  -> clones _jail_agent_init into a new PID+user+mount namespace,
//	               mapped to inner-UID 0 so it keeps CAP_SYS_ADMIN across execve.
//	_jail_agent_init (stage 2, inner-root): replaces the temporarily visible
//	               host procfs with one owned by this PID namespace, then clones
//	               _jail_agent_drop into a further nested user namespace that
//	               maps back to the real nonzero uid.
//	_jail_agent_drop (stage 3, nonzero uid): installs seccomp and execs the
//	               agent. It has no capabilities over the mount namespace, so the
//	               sealed filesystem and private procfs stand, and Claude's
//	               --dangerously-skip-permissions root guard is satisfied.
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
	// asserts the visible /proc belongs to this PID namespace either way. The
	// detach is best-effort and its refusal is an expected, benign path, so it
	// is not surfaced to the agent's terminal — only a failed overmount, or a
	// procfs that verifies as non-private, is fatal.
	_ = unix.Unmount("/proc", unix.MNT_DETACH)
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
	agentPID := drop.Process.Pid
	// This process is PID 1 of the jail's PID namespace, so it must act as a
	// real init: forward termination signals to the agent, and reap EVERY exited
	// child. Orphaned grandchildren (the agent double-forks) are reparented to
	// PID 1; without reaping they accumulate as zombies (there is no PID limit),
	// so wait4(-1) rather than waiting only for the direct child. Exit with the
	// agent's status once it is the one that exited.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() {
		for sig := range sigCh {
			if p, err := os.FindProcess(agentPID); err == nil {
				p.Signal(sig)
			}
		}
	}()
	var ws syscall.WaitStatus
	for {
		wpid, err := syscall.Wait4(-1, &ws, 0, nil)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			log.Fatalf("_jail_agent_init: wait4: %v", err)
		}
		if wpid != agentPID {
			continue // reaped an orphaned grandchild — keep going
		}
		if ws.Signaled() {
			os.Exit(128 + int(ws.Signal()))
		}
		os.Exit(ws.ExitStatus())
	}
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
// Args format: --uid UID --gid GID [--log PATH] [--net-relay-fd FD]
// [--proxy-port PORT] [--local-port PORT...] [--deny PATH...] [--home PATH]
// [--writable PATH...] [--mount-ro PATH...] [--overlay-prefix PREFIX...]
// [--rlimit RESOURCE=VALUE...] -- CMD ARGS...
func DenyInit(args []string) {
	var denyPaths []string
	var denyRenamePaths []string
	var denyWritePaths []string
	var writablePaths []string
	var overlayPrefixes []string
	var roMounts []string
	var readAliases []Mount
	var home string
	var logPath string
	var uid, gid int
	var netRelayFD, proxyPort int
	var localPorts []int
	var limits []rlimitPair
	var cmdStart int

	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			cmdStart = i + 1
			break
		}
		if i+1 < len(args) {
			switch args[i] {
			case "--rlimit":
				limit, err := parseRlimit(args[i+1])
				if err != nil {
					log.Fatalf("_deny_init: %v", err)
				}
				limits = append(limits, limit)
				i++
			case "--deny":
				denyPaths = append(denyPaths, args[i+1])
				i++
			case "--deny-rename":
				denyRenamePaths = append(denyRenamePaths, args[i+1])
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
			case "--mount-ro-alias":
				if i+2 >= len(args) {
					log.Fatal("_deny_init: read alias needs source and target")
				}
				readAliases = append(readAliases, Mount{Source: args[i+1], Target: args[i+2], ReadOnly: true})
				i += 2
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
			case "--net-relay-fd":
				netRelayFD, _ = strconv.Atoi(args[i+1])
				i++
			case "--proxy-port":
				proxyPort, _ = strconv.Atoi(args[i+1])
				i++
			case "--local-port":
				port, _ := strconv.Atoi(args[i+1])
				localPorts = append(localPorts, port)
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
	if netRelayFD > 0 {
		if err := startNamespaceRelays(netRelayFD, proxyPort, localPorts); err != nil {
			failEnforcement("start network namespace relay", "127.0.0.1", err)
		}
		log.Printf("_deny_init: network namespace relay active (proxy=%d local_ports=%v)", proxyPort, localPorts)
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
	// Resolve host aliases before setupJail hides or recreates their names.
	// Project descendant masks into every separately bound read alias too.
	denyPaths = CanonicalDenyPaths(denyPaths, readAliases...)
	denyWritePaths = CanonicalDenyPaths(denyWritePaths, readAliases...)

	// Jail mode: deny:/ creates an allowlist filesystem. Only explicitly
	// mounted paths are visible; everything else is inaccessible.
	var overlayPersistFn func()
	jailMode := containsPath(denyPaths, "/")
	allDenied := append(append([]string(nil), denyPaths...), denyWritePaths...)
	var granted []string
	for _, path := range writablePaths {
		masked := false
		for _, denied := range allDenied {
			masked = masked || (denied != "/" && isPathWithin(path, denied))
		}
		if !masked {
			granted = append(granted, path)
		}
	}
	writablePaths = granted
	if err := prepareJailWritablePaths(writablePaths); err != nil {
		failEnforcement("prepare writable paths", home, err)
	}
	privateWritable := append([]string(nil), writablePaths...)
	if home != "" {
		files, err := writablePrefixFiles(home, writablePaths, overlayPrefixes)
		if err != nil {
			failEnforcement("prepare writable prefix files", home, err)
		}
		privateWritable = append(privateWritable, files...)
	}
	var prepare []string
	for _, path := range allDenied {
		if path == "/" {
			continue
		}
		if !jailMode {
			prepare = append(prepare, path)
			continue
		}
		// Paths outside host binds get placeholders in the private jail root.
		sources := append(append([]string(nil), roMounts...), writablePaths...)
		for _, alias := range readAliases {
			sources = append(sources, alias.Source)
		}
		for _, source := range sources {
			if isPathWithin(path, source) {
				prepare = append(prepare, path)
				break
			}
		}
	}
	denyMountCleanup, err := prepareDenyMountpoints(prepare, tmpDir, privateWritable)
	if err != nil {
		failEnforcement("prepare private deny mountpoints", "/", err)
	}
	if jailMode {
		overlayPersistFn = setupJailWithDenyMountpoints(tmpDir, roMounts, writablePaths, home, readAliases, allDenied, overlayPrefixes...)
		var filtered []string
		for _, d := range denyPaths {
			if d != "/" {
				filtered = append(filtered, d)
			}
		}
		denyPaths = filtered
	}

	// Write isolation: make HOME read-only, then punch writable holes.
	// Must happen BEFORE deny mounts so deny tmpfs overlays take precedence.
	// Skip if HOME itself is in the writable list (user wants full HOME rw).
	//
	// When overlay prefixes are present (e.g. ".claude"), use overlayfs on HOME
	// instead of simple bind-mount+RO. Overlayfs provides a copy-on-write layer
	// so new files can be created and renames work (needed for atomic writes).
	// Prefix-matching files are persisted back to the real HOME on exit.
	if !jailMode && home != "" && len(writablePaths) > 0 && !containsPath(writablePaths, home) {
		if len(overlayPrefixes) > 0 {
			overlayPersistFn = setupOverlayHome(home, writablePaths, overlayPrefixes, tmpDir)
		}
		if overlayPersistFn == nil {
			// No overlay needed or overlay failed — fall back to bind-mount approach.
			if err := setupReadonlyHome(home, writablePaths, overlayPrefixes); err != nil {
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
		file, err := openConfinedExisting("/", p)
		if err != nil {
			failEnforcement("inspect deny-write path", p, err)
		}
		if err := unix.Mount(mountFDPath(file), mountFDPath(file), "", unix.MS_BIND, ""); err != nil {
			failEnforcement("bind deny-write path", p, err)
		}
		if err := remountConfinedBindReadonly("/", p); err != nil {
			failEnforcement("make deny-write path read-only", p, err)
		}
		file.Close()
		expectedMounts = append(expectedMounts, expectedMount{Path: p, ReadOnly: true})
	}

	// Linux cannot rename or unlink a mountpoint. Bind each policy ancestor
	// to itself with its existing flags: descendants keep their write grants.
	for _, path := range denyRenamePaths {
		file, err := openConfinedExisting("/", path)
		if err != nil {
			failEnforcement("inspect protected ancestor", path, err)
		}
		if err := unix.Mount(mountFDPath(file), mountFDPath(file), "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
			failEnforcement("pin protected ancestor", path, err)
		}
		file.Close()
		expectedMounts = append(expectedMounts, expectedMount{Path: path})
	}

	// Syscall success is necessary but the live mount table is the security
	// boundary. Verify every requested mask before seccomp and before the agent
	// process exists. This catches LSM behavior and partial setup bugs that leave
	// the resolved policy looking correct while the namespace is still readable.
	if err := verifyExpectedMounts(expectedMounts); err != nil {
		failEnforcement("verify filesystem policy", "/proc/self/mountinfo", err)
	}

	// Apply soft and hard limits before an agent process exists. They are also
	// inherited through both re-execs of the sealed jail's init/drop stages.
	if err := applyRlimits(limits); err != nil {
		failEnforcement("apply resource limits", "agent process", err)
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
	// Resolve cwd through the completed mount tree. An inherited directory FD
	// from before a parent bind/overlay would bypass masks beneath that parent.
	var cwdErr error
	cmd.Dir, cwdErr = os.Getwd()
	if cwdErr != nil {
		failEnforcement("resolve agent working directory", ".", cwdErr)
	}

	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWPID,
		// If this wrapper (_deny_init) dies, kill the PID-namespace init too. In
		// jail mode that init is PID 1, so killing it tears down the whole jail
		// namespace (agent and all descendants), so a hard-terminated session
		// never leaves survivors running detached from the runtime.
		Pdeathsig: syscall.SIGKILL,
	}
	if uid != 0 || overlayPersistFn != nil || denyMountCleanup != nil {
		// Even a root agent must not share the wrapper's user namespace when
		// it retains backing-directory FDs: /proc/PID/fd must remain private.
		// CLONE_NEWNS must accompany CLONE_NEWUSER: the nested user namespace
		// holds no capabilities over the wrapper's mount namespace, so without
		// its own namespace the PID-namespace init cannot swap /proc at all —
		// every mount call fails EPERM (observed on 5.15 shared hosts; masked
		// on root runs without persistence, which keep full capabilities).
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
		if denyMountCleanup != nil {
			denyMountCleanup()
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
	if denyMountCleanup != nil {
		denyMountCleanup()
	}
	os.Exit(0)
}

type denyMountpointPlan struct {
	Parent string
	Paths  []string
}

// Planning only inspects confined paths: no placeholder is ever made on the
// host. Missing suffixes are materialized in a private overlay below.
func planDenyMountpoints(paths []string) ([]denyMountpointPlan, error) {
	byParent := make(map[string][]string)
	for _, path := range paths {
		file, err := openConfinedExisting("/", path)
		if err == nil {
			file.Close()
			continue
		}
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("inspect deny path %s: %w", path, err)
		}
		parent := filepath.Dir(path)
		for {
			file, err = openConfinedExisting("/", parent)
			if err == nil {
				info, statErr := file.Stat()
				file.Close()
				if statErr != nil {
					return nil, statErr
				}
				if !info.IsDir() {
					return nil, fmt.Errorf("deny parent %s is not a directory", parent)
				}
				break
			}
			if !os.IsNotExist(err) || parent == "/" {
				return nil, fmt.Errorf("inspect deny parent %s: %w", parent, err)
			}
			parent = filepath.Dir(parent)
		}
		byParent[parent] = append(byParent[parent], path)
	}
	var plan []denyMountpointPlan
	for parent, paths := range byParent {
		plan = append(plan, denyMountpointPlan{parent, paths})
	}
	sort.Slice(plan, func(i, j int) bool { return len(plan[i].Parent) < len(plan[j].Parent) })
	return plan, nil
}

func prepareDenyMountpoints(paths []string, tmpDir string, writable []string) (func(), error) {
	plans, err := planDenyMountpoints(paths)
	if err != nil || len(plans) == 0 {
		return nil, err
	}
	var cleanups []func()
	cleanup := func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}
	for _, plan := range plans {
		persist := false
		for _, grant := range writable {
			persist = persist || isPathWithin(plan.Parent, grant)
		}
		remove, err := installPrivateDenyMountpoints(plan, tmpDir, persist, writable)
		if err != nil {
			cleanup()
			return nil, err
		}
		cleanups = append(cleanups, remove)
	}
	return cleanup, nil
}

// A private lower layer supplies missing directory mountpoints. For a writable
// parent the real directory is the upper layer, so ordinary new files still go
// straight to the host. For a read-only parent, use a private COW upper instead.
func installPrivateDenyMountpoints(plan denyMountpointPlan, tmpDir string, persist bool, writable []string) (func(), error) {
	parent, err := openConfinedExisting("/", plan.Parent)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	entries, err := readMountInfo("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	// Overlay lookup does not carry inherited submounts. Pin those
	// without turning ordinary files/directories
	// into mountpoints (which would prevent their normal atomic replacement).
	bindPaths := make(map[string]bool)
	for path := range entries {
		if path != plan.Parent && isPathWithin(path, plan.Parent) {
			bindPaths[path] = true
		}
	}
	// A writable descendant does not make its read-only parent a host upper
	// layer. Pin those grants before covering the parent, then bind them back
	// so cache/config writes persist without copying unrelated HOME files up.
	if !persist {
		for _, path := range writable {
			if path != plan.Parent && isPathWithin(path, plan.Parent) {
				bindPaths[path] = true
			}
		}
	}
	var bindNames []string
	for path := range bindPaths {
		bindNames = append(bindNames, path)
	}
	sort.Slice(bindNames, func(i, j int) bool { return len(bindNames[i]) < len(bindNames[j]) })
	var sources []*os.File
	defer func() {
		for _, source := range sources {
			source.Close()
		}
	}()
	for _, path := range bindNames {
		source, err := openConfinedExisting("/", path)
		if err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}
	// Keep staging outside the covered directory: rebinding its children must
	// never publish another alias to the private placeholder backing tree.
	stagingBase := tmpDir
	if isPathWithin(stagingBase, plan.Parent) {
		stagingBase = os.TempDir()
	}
	if isPathWithin(stagingBase, plan.Parent) {
		return nil, fmt.Errorf("no private staging directory outside %s", plan.Parent)
	}
	staging, err := os.MkdirTemp(stagingBase, "deny-view-")
	if err != nil {
		return nil, err
	}
	defer os.Remove(staging)
	if err := unix.Mount("tmpfs", staging, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "size=64m"); err != nil {
		return nil, err
	}
	defer unix.Unmount(staging, unix.MNT_DETACH)
	lower, upper, work := filepath.Join(staging, "lower"), filepath.Join(staging, "upper"), filepath.Join(staging, "work")
	for _, dir := range []string{lower, upper, work} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			return nil, err
		}
	}
	for _, path := range plan.Paths {
		relative, err := filepath.Rel(plan.Parent, path)
		if err != nil {
			return nil, err
		}
		file, err := createConfinedMountpoint(lower, "/"+relative, true)
		if err != nil {
			return nil, err
		}
		file.Close()
	}
	var workFD, workParent *os.File
	var workName string
	cleanup := func() {
		if workFD != nil {
			_ = os.RemoveAll(mountFDPath(workFD) + "/work")
			_ = unix.Unlinkat(int(workParent.Fd()), workName, unix.AT_REMOVEDIR)
			workFD.Close()
			workParent.Close()
		}
	}
	succeeded := false
	defer func() {
		if !succeeded {
			cleanup()
		}
	}()
	if persist {
		// Overlay work must share the upper filesystem and be outside its tree.
		workBase := tmpDir
		if isPathWithin(workBase, plan.Parent) {
			workBase = os.TempDir()
		}
		var upperStat, workStat unix.Stat_t
		if err := unix.Fstat(int(parent.Fd()), &upperStat); err != nil {
			return nil, err
		}
		if err := unix.Stat(workBase, &workStat); err != nil {
			return nil, err
		}
		if workStat.Dev != upperStat.Dev || isPathWithin(workBase, plan.Parent) {
			workBase = filepath.Dir(plan.Parent)
		}
		workPath, err := os.MkdirTemp(workBase, ".deny-work-")
		if err != nil {
			return nil, fmt.Errorf("prepare overlay work outside %s: %w", plan.Parent, err)
		}
		workFD, err = openConfinedExisting("/", workPath)
		if err != nil {
			os.Remove(workPath)
			return nil, err
		}
		workParent, err = openConfinedExisting("/", filepath.Dir(workPath))
		if err != nil {
			workFD.Close()
			workFD = nil
			os.Remove(workPath)
			return nil, err
		}
		workName = filepath.Base(workPath)
		if err := unix.Mkdirat(int(workFD.Fd()), "work", 0o700); err != nil {
			return nil, err
		}
		upper, work = mountFDPath(parent), mountFDPath(workFD)+"/work"
		// Hide the retained work directory while overlayfs keeps its backing FD.
		if err := unix.Mount("tmpfs", workPath, "tmpfs", unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV, "size=0"); err != nil {
			return nil, err
		}
	}
	options := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", lower, upper, work)
	if !persist {
		options = fmt.Sprintf("lowerdir=%s:%s,upperdir=%s,workdir=%s", lower, mountFDPath(parent), upper, work)
	}
	if err := unix.Mount("overlay", mountFDPath(parent), "overlay", 0, options); err != nil {
		return nil, fmt.Errorf("private deny view at %s: %w", plan.Parent, err)
	}
	for i, path := range bindNames {
		target, err := openConfinedExisting("/", path)
		if err == nil {
			err = unix.Mount(mountFDPath(sources[i]), mountFDPath(target), "", unix.MS_BIND|unix.MS_REC, "")
			target.Close()
		}
		if err != nil {
			return nil, err
		}
	}
	succeeded = true
	return cleanup, nil
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
	// Never leave the saved HOME alias visible, including fallback paths. The
	// overlay and writable bind mounts retain their own kernel references.
	defer func() {
		if err := unix.Unmount(realHome, unix.MNT_DETACH); err != nil {
			failEnforcement("hide overlay backing HOME", realHome, err)
		}
	}()

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
		// Ensure both sides have compatible mountpoints. Writable rules may
		// name a single file (the browser-request inbox is intentionally one
		// such file), so blindly using MkdirAll turns that valid policy into a
		// fatal "not a directory" error.
		if err := prepareWritableMountpoint(realPath, p); err != nil {
			log.Printf("_deny_init: prepare writable %s: %v", p, err)
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
	// Keep a CLOEXEC directory handle for the wrapper's post-exit persistence.
	// It is not inherited by the agent; the nested user namespace also denies
	// access to the ancestor wrapper's /proc/PID/fd directory.
	realHomeFD, err := os.Open(realHome)
	if err != nil {
		failEnforcement("retain overlay persistence directory", realHome, err)
	}
	persistHome := mountFDPath(realHomeFD)
	upperFD, err := os.Open(upperDir)
	if err != nil {
		failEnforcement("retain overlay upper directory", upperDir, err)
	}
	persistUpper := mountFDPath(upperFD)
	// Raw upper/work aliases would let the agent bypass visible deny mounts
	// and inject files that the wrapper later persists. Keep only private FDs.
	for _, path := range []string{upperDir, workDir} {
		if err := unix.Mount("tmpfs", path, "tmpfs", unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV, "size=0"); err != nil {
			failEnforcement("hide overlay backing directory", path, err)
		}
		expected = append(expected, expectedMount{Path: path, FSType: "tmpfs", ReadOnly: true})
	}
	if err := verifyExpectedMounts(expected); err != nil {
		failEnforcement("verify hidden overlay backing directories", tmpDir, err)
	}

	// Return function that persists prefix-matching files from overlay upper
	// back to real HOME. Called after the agent process exits.
	return func() {
		defer realHomeFD.Close()
		defer upperFD.Close()
		entries, err := os.ReadDir(persistUpper)
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
				persistDir(filepath.Join(persistUpper, name), filepath.Join(persistHome, name))
				continue
			}
			src := filepath.Join(persistUpper, name)
			dst := filepath.Join(persistHome, name)
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

// setupReadonlyHome binds and recursively seals HOME, then reopens only
// declared writable paths and prefix-matching files. Works for overwriting existing files but cannot
// handle new file creation or renames in HOME.
func setupReadonlyHome(home string, writablePaths, prefixes []string) error {
	// Pin original writable mounts before sealing the new HOME tree. Binding a
	// source resolved afterward would inherit the newly read-only mount flags.
	files, err := writablePrefixFiles(home, writablePaths, prefixes)
	if err != nil {
		return err
	}
	paths := append(append([]string(nil), writablePaths...), files...)
	var sources []*os.File
	defer func() {
		for _, source := range sources {
			source.Close()
		}
	}()
	for _, p := range paths {
		if err := prepareWritableMountpoint(p, p); err != nil {
			return fmt.Errorf("create writable mountpoint %s: %w", p, err)
		}
		file, err := openConfinedExisting("/", p)
		if err != nil {
			return err
		}
		sources = append(sources, file)
	}
	if err := unix.Mount(home, home, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return fmt.Errorf("bind HOME: %w", err)
	}
	if err := remountBindReadonly(home); err != nil {
		return fmt.Errorf("remount HOME read-only: %w", err)
	}
	expected := []expectedMount{{Path: home, ReadOnly: true, RecursiveReadOnly: true}}
	for i, p := range paths {
		target, err := openConfinedExisting("/", p)
		if err != nil {
			return err
		}
		err = unix.Mount(mountFDPath(sources[i]), mountFDPath(target), "", unix.MS_BIND|unix.MS_REC, "")
		target.Close()
		if err != nil {
			return fmt.Errorf("bind writable path %s: %w", p, err)
		}
		expected = append(expected, expectedMount{Path: p, Writable: true})
	}
	if err := verifyExpectedMounts(expected); err != nil {
		return fmt.Errorf("verify HOME write isolation: %w", err)
	}
	log.Printf("_deny_init: write isolation: HOME=%s ro, %d writable paths", home, len(writablePaths))
	return nil
}

func writablePrefixFiles(home string, writablePaths, prefixes []string) ([]string, error) {
	var files []string
	for _, prefix := range prefixes {
		p := filepath.Join(home, prefix)
		if !isPathWithin(p, home) || p == home || !containsPath(writablePaths, p) {
			return nil, fmt.Errorf("writable prefix is not a declared HOME mount: %s", prefix)
		}
		dir := filepath.Dir(p)
		parent, err := openConfinedExisting("/", dir)
		if err != nil {
			return nil, err
		}
		entries, err := os.ReadDir(mountFDPath(parent))
		if err != nil {
			parent.Close()
			return nil, err
		}
		for _, entry := range entries {
			if entry.Name() == filepath.Base(p) || !strings.HasPrefix(entry.Name(), filepath.Base(p)) || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			file, err := openMountpointAt(parent, entry.Name(), path)
			if err != nil {
				parent.Close()
				return nil, err
			}
			info, err := file.Stat()
			file.Close()
			if err != nil {
				parent.Close()
				return nil, err
			}
			if info.Mode().IsRegular() {
				files = append(files, path)
			}
		}
		parent.Close()
	}
	return files, nil
}

// prepareWritableMountpoint makes target suitable for a bind mount of source.
// Historically writable rules were directories and an absent source therefore
// still means "create this directory". Existing non-directories are preserved
// as file/socket mountpoints instead of being passed to MkdirAll.
func prepareWritableMountpoint(source, target string) error {
	info, err := os.Stat(source)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		if err := os.MkdirAll(source, 0o755); err != nil {
			return err
		}
		info, err = os.Stat(source)
		if err != nil {
			return err
		}
	}
	if info.IsDir() {
		return os.MkdirAll(target, 0o755)
	}

	targetInfo, err := os.Stat(target)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		return file.Close()
	}
	if targetInfo.IsDir() {
		return fmt.Errorf("source is not a directory but target is")
	}
	return nil
}

// persistDir recursively copies directory contents from overlay upper to real HOME.
func persistDir(src, dst string) {
	sourceInfo, err := os.Lstat(src)
	if err != nil || !sourceInfo.IsDir() || sourceInfo.Mode()&os.ModeSymlink != 0 {
		return
	}
	if destinationInfo, statErr := os.Lstat(dst); statErr == nil {
		if !destinationInfo.IsDir() || destinationInfo.Mode()&os.ModeSymlink != 0 {
			if removeErr := os.RemoveAll(dst); removeErr != nil {
				log.Printf("_deny_init: replace unsafe persistence directory %s: %v", dst, removeErr)
				return
			}
		}
	} else if !os.IsNotExist(statErr) {
		return
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return
	}
	if err := os.MkdirAll(dst, 0755); err != nil {
		return
	}
	for _, e := range entries {
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dst, e.Name())
		entryInfo, err := os.Lstat(s)
		if err != nil || entryInfo.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if e.IsDir() {
			persistDir(s, d)
			continue
		}
		if !entryInfo.Mode().IsRegular() {
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
	sourceInfo, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !sourceInfo.Mode().IsRegular() || sourceInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing non-regular persistence source %s", src)
	}
	if destinationInfo, err := os.Lstat(dst); err == nil {
		if destinationInfo.Mode()&os.ModeSymlink != 0 || !destinationInfo.Mode().IsRegular() {
			if err := os.RemoveAll(dst); err != nil {
				return err
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

type expectedMount struct {
	Path              string
	FSType            string
	ReadOnly          bool
	Writable          bool
	RecursiveReadOnly bool
}

type mountInfoEntry struct {
	FSType  string
	Options map[string]bool
}

// remountBindReadonly seals the bind and every inherited submount while
// preserving each mount's current flags. Passing a minimal flag set works on
// many Linux kernels, but WSL returns EPERM when a remount would implicitly
// discard flags inherited from the source mount.
func remountBindReadonly(path string) error {
	return remountBindReadonlyAt(path, path)
}

func remountBindReadonlyAt(path, target string) error {
	entries, err := readMountInfo("/proc/self/mountinfo")
	if err != nil {
		return err
	}
	plan, err := readonlyBindMountPlan(entries, path)
	if err != nil {
		return err
	}
	var expected []expectedMount
	for _, step := range plan {
		mountTarget := target
		var file *os.File
		if step.Path != filepath.Clean(path) {
			file, err = openConfinedExisting("/", step.Path)
			if err != nil {
				return err
			}
			mountTarget = mountFDPath(file)
		}
		err = unix.Mount("", mountTarget, "", step.Flags, "")
		if file != nil {
			file.Close()
		}
		if err != nil {
			return fmt.Errorf("seal inherited mount %s: %w", step.Path, err)
		}
		expected = append(expected, expectedMount{Path: step.Path, ReadOnly: true})
	}
	return verifyExpectedMounts(expected)
}

type bindRemount struct {
	Path  string
	Flags uintptr
}

func readonlyBindMountPlan(entries map[string]mountInfoEntry, root string) ([]bindRemount, error) {
	root = filepath.Clean(root)
	if _, ok := entries[root]; !ok {
		return nil, fmt.Errorf("bind mount missing at %s", root)
	}
	var plan []bindRemount
	for path, entry := range entries {
		if isPathWithin(path, root) {
			plan = append(plan, bindRemount{path, uintptr(unix.MS_REMOUNT|unix.MS_BIND|unix.MS_RDONLY) | mountFlagsFromOptions(entry.Options)})
		}
	}
	// Seal children first, so inherited writable mounts cannot survive sealing
	// their parent. Explicit writable grants are rebound only after this pass.
	sort.Slice(plan, func(i, j int) bool {
		if len(plan[i].Path) != len(plan[j].Path) {
			return len(plan[i].Path) > len(plan[j].Path)
		}
		return plan[i].Path < plan[j].Path
	})
	return plan, nil
}

func remountConfinedBindReadonly(root, path string) error {
	// A handle opened before the bind still refers to the covered mount.
	// Resolve again through confined parent FDs to pin the new top mount.
	file, err := openConfinedExisting(root, path)
	if err != nil {
		return err
	}
	defer file.Close()
	return remountBindReadonlyAt(filepath.Join(root, path), mountFDPath(file))
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
		if want.RecursiveReadOnly {
			for path, entry := range entries {
				if !isPathWithin(path, want.Path) || entry.Options["ro"] {
					continue
				}
				mode := want
				for _, grant := range expected {
					if (grant.ReadOnly || grant.Writable) && isPathWithin(path, grant.Path) && len(grant.Path) > len(mode.Path) {
						mode = grant
					}
				}
				if !mode.Writable {
					return fmt.Errorf("inherited mount at %s is writable", path)
				}
			}
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
	if isWSL2Kernel() {
		log.Fatalf("_deny_init: sandbox enforcement failed during %s at %q: %v (platform: WSL2; some WSL2 configurations reject required mount or namespace operations inside unprivileged user namespaces; use a privileged Linux container or VM as the outer sandbox boundary); refusing to launch agent",
			operation, path, err)
	}
	profile := currentSecurityProfile()
	if profile == "" {
		profile = "unreported"
	}
	log.Fatalf("_deny_init: sandbox enforcement failed: %s %q: %v (security profile: %s); refusing to launch agent",
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
func setupJail(tmpDir string, roMounts, writablePaths []string, home string, readAliases []Mount, prefixes ...string) func() {
	return setupJailWithDenyMountpoints(tmpDir, roMounts, writablePaths, home, readAliases, nil, prefixes...)
}

func setupJailWithDenyMountpoints(tmpDir string, roMounts, writablePaths []string, home string, readAliases []Mount, denied []string, prefixes ...string) func() {
	// Prepare persistent writable directories before a read-only ancestor is
	// bound into the jail. Fresh homes may not yet have their declared caches.
	if err := prepareJailWritablePaths(writablePaths); err != nil {
		failEnforcement("prepare jail writable paths", home, err)
	}
	newRoot := filepath.Join(tmpDir, "newroot")
	if err := os.MkdirAll(newRoot, 0755); err != nil {
		log.Fatalf("_deny_init: jail mkdir newroot: %v", err)
	}
	if err := unix.Mount("tmpfs", newRoot, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "size=64m"); err != nil {
		log.Fatalf("_deny_init: jail mount newroot: %v", err)
	}
	// Recreate merged-usr symlinks (/bin -> usr/bin, etc.) if the host uses them.
	aliases := make(map[string]string)
	for _, link := range [][2]string{
		{"bin", "usr/bin"}, {"sbin", "usr/sbin"}, {"lib", "usr/lib"}, {"lib64", "usr/lib64"},
	} {
		if target, err := os.Readlink("/" + link[0]); err == nil {
			if target != link[1] && target != "/"+link[1] {
				failEnforcement("validate jail system symlink", "/"+link[0], fmt.Errorf("unexpected target %q", target))
			}
			aliases["/"+link[0]] = "/" + link[1]
			if err := os.Symlink(link[1], filepath.Join(newRoot, link[0])); err != nil {
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
	tmpFD, err := createConfinedMountpoint(newRoot, tmpDir, true)
	if err != nil {
		failEnforcement("create sandbox temp directory inside jail", tmpDir, err)
	}
	tmpFD.Close()
	// Create deny targets after the virtual mounts, which would otherwise
	// cover placeholders under /tmp and /dev. No host binds exist yet, so
	// creation still touches only the private jail filesystem.
	for _, path := range denied {
		if path == "/" {
			continue
		}
		directory := true
		if info, err := os.Lstat(path); err == nil {
			directory = info.IsDir()
		}
		file, err := createConfinedMountpoint(newRoot, path, directory)
		if err != nil {
			failEnforcement("create private jail deny mountpoint", path, err)
		}
		file.Close()
	}
	// HOME itself is an empty jail directory unless explicitly declared. Agent
	// config directories are already included in writablePaths by its profile.
	if home != "" {
		homeFD, err := createConfinedMountpoint(newRoot, home, true)
		if err != nil {
			failEnforcement("create jail HOME directory", home, err)
		}
		homeFD.Close()
	}
	// Prefix config files live in the otherwise empty jail HOME, where atomic
	// replacement remains possible. Persist only those declared prefixes; the
	// agent's config directories already persist through their bind mounts.
	persist, extraWritable, err := prepareJailPrefixFiles(newRoot, home, roMounts, writablePaths, prefixes)
	if err != nil {
		failEnforcement("prepare jail prefix config", home, err)
	}
	writablePaths = append(append([]string(nil), writablePaths...), extraWritable...)
	// Mount parents before children so each declared child retains its mode.
	expected := []expectedMount{
		{Path: "/", FSType: "tmpfs", Writable: true},
		{Path: "/proc"},
		{Path: "/dev", FSType: "tmpfs", Writable: true},
		{Path: "/dev/shm", FSType: "tmpfs", Writable: true},
		{Path: "/tmp", FSType: "tmpfs", Writable: true},
	}
	for _, mount := range jailMounts(roMounts, writablePaths, aliases) {
		p := mount.Path
		sourceFD, targetFD, err := jailMkTarget(newRoot, p)
		if err != nil {
			failEnforcement("create jail mountpoint", p, err)
		}
		if err := unix.Mount(mountFDPath(sourceFD), mountFDPath(targetFD), "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
			failEnforcement("bind jail path", p, err)
		}
		if mount.ReadOnly {
			if err := remountConfinedBindReadonly(newRoot, p); err != nil {
				failEnforcement("make jail path read-only", p, err)
			}
		}
		sourceFD.Close()
		targetFD.Close()
		if mount.ReadOnly {
			expected = append(expected, expectedMount{Path: p, ReadOnly: true, RecursiveReadOnly: true})
			log.Printf("_deny_init: jail ro %s", p)
		} else {
			expected = append(expected, expectedMount{Path: p, Writable: true})
			log.Printf("_deny_init: jail rw %s", p)
		}
	}
	// Aliases use resolved, confined sources and synthetic read-only targets.
	// A changed symlink is never followed during mountpoint creation.
	for _, alias := range readAliases {
		sourceFD, targetFD, err := jailMkTargetAt(newRoot, alias.Source, alias.Target)
		if err != nil {
			failEnforcement("create jail read alias", alias.Target, err)
		}
		if err := unix.Mount(mountFDPath(sourceFD), mountFDPath(targetFD), "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
			failEnforcement("bind jail read alias", alias.Target, err)
		}
		if err := remountConfinedBindReadonly(newRoot, alias.Target); err != nil {
			failEnforcement("make jail read alias read-only", alias.Target, err)
		}
		sourceFD.Close()
		targetFD.Close()
		expected = append(expected, expectedMount{Path: alias.Target, ReadOnly: true, RecursiveReadOnly: true})
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
	return persist
}

func prepareJailWritablePaths(paths []string) error {
	for _, path := range paths {
		file, err := openConfinedExisting("/", path)
		if os.IsNotExist(err) {
			file, err = createConfinedMountpoint("/", path, true)
		}
		if err != nil {
			return fmt.Errorf("prepare %s: %w", path, err)
		}
		file.Close()
	}
	return nil
}

func prepareJailPrefixFiles(root, home string, readonly, writable, prefixes []string) (func(), []string, error) {
	files, err := writablePrefixFiles(home, writable, prefixes)
	if err != nil {
		return nil, nil, err
	}
	declared := append(append([]string(nil), readonly...), writable...)
	covered := func(path string) bool {
		for _, mount := range declared {
			if isPathWithin(path, mount) {
				return true
			}
		}
		return false
	}
	var extraWritable []string
	for _, path := range files {
		if covered(path) {
			// Keep explicit file mounts' modes. Otherwise, a declared prefix
			// punches a writable file hole in its explicitly mounted parent.
			if !containsPath(declared, path) {
				extraWritable = append(extraWritable, path)
			}
			continue
		}
		source, err := openConfinedExisting("/", path)
		if err != nil {
			return nil, nil, err
		}
		target, err := createConfinedMountpoint(root, path, false)
		if err == nil {
			err = copyPinnedFile(source, target)
			target.Close()
		}
		source.Close()
		if err != nil {
			return nil, nil, err
		}
	}
	type prefixDirectory struct {
		path, prefix string
		host         *os.File
	}
	var directories []prefixDirectory
	for _, prefix := range prefixes {
		path := filepath.Join(home, prefix)
		parent := filepath.Dir(path)
		if covered(parent) {
			continue
		}
		host, err := openConfinedExisting("/", parent)
		if err != nil {
			for _, directory := range directories {
				directory.host.Close()
			}
			return nil, nil, err
		}
		directories = append(directories, prefixDirectory{parent, filepath.Base(path), host})
	}
	if len(directories) == 0 {
		return nil, extraWritable, nil
	}
	return func() {
		for _, directory := range directories {
			entries, err := os.ReadDir(directory.path)
			if err == nil {
				for _, entry := range entries {
					path := filepath.Join(directory.path, entry.Name())
					if entry.Name() == directory.prefix || !strings.HasPrefix(entry.Name(), directory.prefix) || covered(path) || !entry.Type().IsRegular() {
						continue
					}
					if err := copyFile(path, filepath.Join(mountFDPath(directory.host), entry.Name())); err != nil {
						log.Printf("_deny_init: persist jail prefix %s: %v", path, err)
					}
				}
			}
			directory.host.Close()
		}
	}, extraWritable, nil
}

func copyPinnedFile(source, target *os.File) error {
	in, err := os.Open(mountFDPath(source))
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	targetInfo, err := target.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || !targetInfo.Mode().IsRegular() || targetInfo.Size() != 0 {
		return fmt.Errorf("refusing to initialize a nonempty or non-regular jail config target")
	}
	out, err := os.OpenFile(mountFDPath(target), os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer out.Close()
	if err := out.Chmod(info.Mode().Perm()); err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	return err
}

func isPathWithin(path, root string) bool {
	cleanPath := filepath.Clean(path)
	cleanRoot := filepath.Clean(root)
	return cleanPath == cleanRoot || strings.HasPrefix(cleanPath, strings.TrimSuffix(cleanRoot, string(filepath.Separator))+string(filepath.Separator))
}

func jailMounts(readonly, writable []string, aliases map[string]string) []expectedMount {
	// Bind the validated merged-usr destinations directly. The confined walker
	// still rejects every symlink in the source and target; aliases recreated by
	// setupJail must not be mistaken for agent-controlled mountpoint symlinks.
	canonical := func(path string) string {
		path = filepath.Clean(path)
		for alias, target := range aliases {
			if path == alias || strings.HasPrefix(path, alias+"/") {
				return target + strings.TrimPrefix(path, alias)
			}
		}
		return path
	}
	byPath := make(map[string]expectedMount)
	for _, p := range readonly {
		p = canonical(p)
		byPath[p] = expectedMount{Path: p, ReadOnly: true}
	}
	for _, p := range writable {
		p = canonical(p)
		byPath[p] = expectedMount{Path: p, Writable: true}
	}
	mounts := make([]expectedMount, 0, len(byPath))
	for _, mount := range byPath {
		mounts = append(mounts, mount)
	}
	sort.Slice(mounts, func(i, j int) bool {
		left, right := mounts[i].Path, mounts[j].Path
		if strings.Count(left, "/") != strings.Count(right, "/") {
			return strings.Count(left, "/") < strings.Count(right, "/")
		}
		return left < right
	})
	return mounts
}

// jailMkTarget pins both sides of a jail bind mount. No component may be a
// symlink, and existing target files are opened without truncating them.
func jailMkTarget(root, src string) (source, target *os.File, err error) {
	return jailMkTargetAt(root, src, src)
}

func jailMkTargetAt(root, src, dst string) (source, target *os.File, err error) {
	source, err = openConfinedExisting("/", src)
	if err != nil {
		return nil, nil, err
	}
	info, err := source.Stat()
	if err == nil {
		target, err = createConfinedMountpoint(root, dst, info.IsDir())
	}
	if err != nil {
		source.Close()
		return nil, nil, err
	}
	return source, target, nil
}

func mountFDPath(file *os.File) string {
	return fmt.Sprintf("/proc/self/fd/%d", file.Fd())
}

// openConfinedParent walks from a pinned root, refusing symlinks at every
// component. mkdirat/openat also keep creation confined if a component is
// replaced while setup is running.
func openConfinedParent(root, path string, create bool) (*os.File, string, error) {
	if !filepath.IsAbs(path) || !filepath.IsAbs(root) {
		return nil, "", fmt.Errorf("mount path must be absolute: %s", path)
	}
	for _, part := range strings.Split(path, "/") {
		if part == ".." {
			return nil, "", fmt.Errorf("mount path contains parent traversal: %s", path)
		}
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, "", err
	}
	parts := strings.Split(strings.TrimPrefix(filepath.Clean(path), "/"), "/")
	var rootParts []string
	if filepath.Clean(root) != "/" {
		rootParts = strings.Split(strings.TrimPrefix(filepath.Clean(root), "/"), "/")
	}
	parents := append(append([]string(nil), rootParts...), parts[:len(parts)-1]...)
	for i, part := range parents {
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if openErr == unix.ENOENT && create && i >= len(rootParts) {
			if mkdirErr := unix.Mkdirat(fd, part, 0o755); mkdirErr != nil && mkdirErr != unix.EEXIST {
				unix.Close(fd)
				return nil, "", mkdirErr
			}
			next, openErr = unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		unix.Close(fd)
		if openErr != nil {
			return nil, "", openErr
		}
		fd = next
	}
	base := parts[len(parts)-1]
	if base == "" {
		base = "."
	}
	return os.NewFile(uintptr(fd), path), base, nil
}

func openConfinedExisting(root, path string) (*os.File, error) {
	parent, base, err := openConfinedParent(root, path, false)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	return openMountpointAt(parent, base, path)
}

func openMountpointAt(parent *os.File, base, path string) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), base, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		file.Close()
		if err == nil {
			err = fmt.Errorf("refusing symlink mountpoint: %s", path)
		}
		return nil, err
	}
	return file, nil
}

func createConfinedMountpoint(root, path string, directory bool) (*os.File, error) {
	parent, base, err := openConfinedParent(root, path, true)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	if directory {
		err = unix.Mkdirat(int(parent.Fd()), base, 0o755)
	} else {
		var fd int
		fd, err = unix.Openat(int(parent.Fd()), base, unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_CREAT|unix.O_EXCL, 0o644)
		if err == nil {
			err = unix.Close(fd)
		}
	}
	if err != nil && err != unix.EEXIST {
		return nil, err
	}
	file, err := openMountpointAt(parent, base, path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || info.IsDir() != directory {
		file.Close()
		if err == nil {
			err = fmt.Errorf("incompatible mountpoint type: %s", path)
		}
		return nil, err
	}
	return file, nil
}
