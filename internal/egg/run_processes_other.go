//go:build !darwin

package egg

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func processSnapshotContext(parent context.Context) (map[int]observedProcess, error) {
	// Exclude names, arguments and environment from descendant metadata.
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/ps", "-axo", "pid=,ppid=,pgid=,stat=,lstart=")
	cmd.WaitDelay = 100 * time.Millisecond
	data, err := cmd.Output()
	if err != nil {
		return nil, errors.New("process descendant inventory unavailable")
	}
	out := make(map[int]observedProcess)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 9 || strings.HasPrefix(fields[3], "Z") {
			continue
		}
		pid, e1 := strconv.Atoi(fields[0])
		parent, e2 := strconv.Atoi(fields[1])
		group, e3 := strconv.Atoi(fields[2])
		if e1 != nil || e2 != nil || e3 != nil {
			continue
		}
		out[pid] = observedProcess{RunDescendant: RunDescendant{pid, parent, group}, start: strings.Join(fields[4:], " ")}
	}
	return out, nil
}

func sessionProcessGroupAlive(ctx context.Context, pgid int) (bool, error) {
	members, err := processSnapshotContext(ctx)
	if err != nil {
		return false, err
	}
	for _, member := range members {
		if member.ProcessGroupID == pgid {
			return true, nil
		}
	}
	return false, nil
}

func sessionGroupSignalError(_ int, signalErr error) error {
	return signalErr
}
