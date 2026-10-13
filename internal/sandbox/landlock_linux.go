//go:build linux

package sandbox

import (
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const landlockExecArg = "_landlock_exec"
const landlockWrites = uint64(unix.LANDLOCK_ACCESS_FS_WRITE_FILE | unix.LANDLOCK_ACCESS_FS_REMOVE_DIR | unix.LANDLOCK_ACCESS_FS_REMOVE_FILE | unix.LANDLOCK_ACCESS_FS_MAKE_CHAR | unix.LANDLOCK_ACCESS_FS_MAKE_DIR | unix.LANDLOCK_ACCESS_FS_MAKE_REG | unix.LANDLOCK_ACCESS_FS_MAKE_SOCK | unix.LANDLOCK_ACCESS_FS_MAKE_FIFO | unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK | unix.LANDLOCK_ACCESS_FS_MAKE_SYM | unix.LANDLOCK_ACCESS_FS_REFER | unix.LANDLOCK_ACCESS_FS_TRUNCATE)

func landlockABI() (int, error) {
	abi, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 {
		return 0, errno
	}
	return int(abi), nil
}

func installLandlockWrites(paths []string) error {
	if err := CheckProviderWriteProtection(); err != nil {
		return err
	}
	// Use only ABI 3 rights, even on newer kernels. The first eight bytes are
	// handled_access_fs; newer network/scoping fields are deliberately absent.
	handled := landlockWrites
	ruleset, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&handled)), 8, 0)
	if errno != 0 {
		return fmt.Errorf("create Landlock write ruleset: %w", errno)
	}
	defer unix.Close(int(ruleset))
	for _, path := range paths {
		fd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
		if err != nil {
			return fmt.Errorf("open Landlock grant %s: %w", path, err)
		}
		var st unix.Stat_t
		err = unix.Fstat(fd, &st)
		allowed := landlockWrites
		if st.Mode&unix.S_IFMT != unix.S_IFDIR {
			allowed = unix.LANDLOCK_ACCESS_FS_WRITE_FILE | unix.LANDLOCK_ACCESS_FS_TRUNCATE
		}
		attr := unix.LandlockPathBeneathAttr{Allowed_access: allowed, Parent_fd: int32(fd)}
		if err == nil {
			_, _, e := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, ruleset, unix.LANDLOCK_RULE_PATH_BENEATH, uintptr(unsafe.Pointer(&attr)), 0, 0, 0)
			if e != 0 {
				err = e
			}
		}
		unix.Close(fd)
		if err != nil {
			return fmt.Errorf("add Landlock grant %s: %w", path, err)
		}
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	_, _, errno = unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, ruleset, 0, 0)
	if errno != 0 {
		return fmt.Errorf("restrict provider with Landlock: %w", errno)
	}
	return nil
}

// Landlock before ABI 8 affects only the calling thread. Lock that thread
// through execve, which replaces the multithreaded Go wrapper, so every provider
// thread and descendant inherits the domain. Never resume ordinary Go work.
func init() {
	if len(os.Args) < 2 || os.Args[1] != landlockExecArg {
		return
	}
	runtime.LockOSThread()
	var paths []string
	i := 2
	for i+1 < len(os.Args) && os.Args[i] == "--writable" {
		paths = append(paths, os.Args[i+1])
		i += 2
	}
	if i+1 >= len(os.Args) || os.Args[i] != "--" {
		fmt.Fprintln(os.Stderr, "invalid Landlock exec arguments")
		os.Exit(1)
	}
	if err := installLandlockWrites(paths); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := syscall.Exec(os.Args[i+1], os.Args[i+1:], os.Environ()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
