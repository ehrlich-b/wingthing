//go:build darwin

package procinfo

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"

	"golang.org/x/sys/unix"
)

// ProcessArgv uses KERN_PROCARGS2 instead of ps. ps flattens argv into display
// text and cannot distinguish a space in the executable path from an argument
// boundary, which made daemon lifecycle commands reject valid custom installs.
func ProcessArgv(pid int) ([]string, error) {
	if pid <= 0 || pid > math.MaxInt32 {
		return nil, unix.EINVAL
	}
	data, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		if errors.Is(err, unix.EINVAL) {
			members, inventoryErr := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
			err = darwinProcArgsError(err, members, inventoryErr)
		}
		return nil, err
	}
	return parseDarwinProcArgs(data)
}

// XNU's sysctl_procargsx returns EINVAL when proc_find cannot acquire the
// process, but also for invalid buffers, unavailable stacks and VM copy errors.
// Confirm absence or kernel exit through the separate PID inventory before
// translating it to ESRCH. sysctl_prochandle returns an empty result for a
// missing PID and includes zombies; fill_user64_externproc exports P_LEXIT as
// P_WEXIT. A process committed to exit cannot resume user code.
// https://github.com/apple-oss-distributions/xnu/blob/xnu-11417.140.69/bsd/kern/kern_sysctl.c
func darwinProcArgsError(argsErr error, members []unix.KinfoProc, inventoryErr error) error {
	if inventoryErr != nil {
		// Keep the argv error as the cause: ENOENT from an unavailable sysctl
		// must not be mistaken for proof that the target process is absent.
		return fmt.Errorf("%w; inspect process state: %v", argsErr, inventoryErr)
	}
	for _, member := range members {
		if member.Proc.P_stat != 5 && member.Proc.P_flag&0x2000 == 0 { // SZOMB, P_WEXIT in sys/proc.h.
			return argsErr
		}
	}
	return unix.ESRCH
}

func parseDarwinProcArgs(data []byte) ([]string, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("kern.procargs2 response is truncated")
	}
	argc := int(binary.LittleEndian.Uint32(data[:4]))
	if argc < 1 || argc > 1<<20 {
		return nil, fmt.Errorf("kern.procargs2 returned invalid argc %d", argc)
	}

	// The payload begins with argc, the executable path, alignment NULs, then
	// exactly argc NUL-terminated argv entries followed by the environment.
	position := 4
	for position < len(data) && data[position] != 0 {
		position++
	}
	if position == len(data) {
		return nil, fmt.Errorf("kern.procargs2 response has no executable terminator")
	}
	for position < len(data) && data[position] == 0 {
		position++
	}

	argv := make([]string, 0, argc)
	for len(argv) < argc {
		if position >= len(data) {
			return nil, fmt.Errorf("kern.procargs2 response ended after %d of %d arguments", len(argv), argc)
		}
		start := position
		for position < len(data) && data[position] != 0 {
			position++
		}
		if position == len(data) {
			return nil, fmt.Errorf("kern.procargs2 argument %d is unterminated", len(argv))
		}
		argv = append(argv, string(data[start:position]))
		position++
	}
	return argv, nil
}
