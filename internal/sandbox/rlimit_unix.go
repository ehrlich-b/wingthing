//go:build linux || darwin

package sandbox

import (
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

type rlimitPair struct {
	resource int
	value    uint64
}

func parseRlimit(arg string) (rlimitPair, error) {
	resourceStr, valueStr, ok := strings.Cut(arg, "=")
	if !ok {
		return rlimitPair{}, fmt.Errorf("invalid rlimit %q: want RESOURCE=VALUE", arg)
	}
	resource, err := strconv.Atoi(resourceStr)
	if err != nil || resource < 0 {
		return rlimitPair{}, fmt.Errorf("invalid rlimit resource %q", resourceStr)
	}
	value, err := strconv.ParseUint(valueStr, 10, 64)
	if err != nil {
		return rlimitPair{}, fmt.Errorf("invalid rlimit value %q: %w", valueStr, err)
	}
	return rlimitPair{resource: resource, value: value}, nil
}

// applyRlimits runs only in the sandbox helper, never in the parent process.
// Setrlimit also tells Go's exec path to preserve an explicit NOFILE limit.
func applyRlimits(limits []rlimitPair) error {
	for _, rl := range limits {
		lim := unix.Rlimit{Cur: rl.value, Max: rl.value}
		if err := unix.Setrlimit(rl.resource, &lim); err != nil {
			return fmt.Errorf("setrlimit(%d, %d): %w", rl.resource, rl.value, err)
		}
	}
	return nil
}
