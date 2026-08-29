//go:build darwin

package main

import (
	"encoding/binary"
	"fmt"

	"golang.org/x/sys/unix"
)

// processArgv uses KERN_PROCARGS2 instead of ps. ps flattens argv into display
// text and cannot distinguish a space in the executable path from an argument
// boundary, which made daemon lifecycle commands reject valid custom installs.
func processArgv(pid int) ([]string, error) {
	data, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, err
	}
	return parseDarwinProcArgs(data)
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
