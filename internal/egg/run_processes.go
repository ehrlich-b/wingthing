package egg

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Descendant reports identity only, never command arguments or diagnostics.
type RunDescendant struct {
	PID            int `json:"pid"`
	ParentPID      int `json:"parent_pid"`
	ProcessGroupID int `json:"process_group_id"`
}

type observedProcess struct {
	RunDescendant
	start string
}

type runProcessTree struct {
	mu   sync.Mutex
	root int
	seen map[int]observedProcess
}

func processSnapshot() (map[int]observedProcess, error) {
	// Both supported Unix platforms provide these fields. Exclude arguments,
	// names and environment so no provider content enters survivor metadata.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, "/bin/ps", "-axo", "pid=,ppid=,pgid=,stat=,lstart=").Output()
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

func (tree *runProcessTree) observe(snapshot map[int]observedProcess) {
	tree.mu.Lock()
	defer tree.mu.Unlock()
	if tree.seen == nil {
		tree.seen = make(map[int]observedProcess)
	}
	for changed := true; changed; {
		changed = false
		for pid, process := range snapshot {
			if pid == tree.root {
				continue
			}
			oldParent, parentSeen := tree.seen[process.ParentPID]
			parent, present := snapshot[process.ParentPID]
			parentSeen = parentSeen && present && parent.start == oldParent.start
			if process.ParentPID != tree.root && !parentSeen {
				continue
			}
			if old, exists := tree.seen[pid]; exists && old.start == process.start {
				continue
			}
			tree.seen[pid] = process
			changed = true
		}
	}
}

func (tree *runProcessTree) kill(sess *Session) ([]RunDescendant, error) {
	before, inventoryErr := processSnapshot()
	if inventoryErr == nil {
		tree.observe(before)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := terminateSession(ctx, sess, 3*time.Second)
	after, afterErr := processSnapshot()
	// SIGKILL delivery is asynchronous. Await disappearance of the signalled
	// group before distinguishing escaped descendants from a process exiting.
	if sess.processGroupID == sess.PID && afterErr == nil {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			alive := false
			for _, process := range after {
				if process.ProcessGroupID == sess.PID {
					alive = true
					break
				}
			}
			if !alive {
				break
			}
			select {
			case <-ctx.Done():
				err = errors.Join(err, ctx.Err())
			case <-ticker.C:
			}
			if ctx.Err() != nil {
				break
			}
			after, afterErr = processSnapshot()
			if afterErr != nil {
				break
			}
		}
	}
	if afterErr != nil {
		return nil, errors.Join(err, inventoryErr, afterErr)
	}
	tree.mu.Lock()
	defer tree.mu.Unlock()
	var survivors []RunDescendant
	for pid, old := range tree.seen {
		if current, alive := after[pid]; alive && current.start == old.start {
			survivors = append(survivors, current.RunDescendant)
		}
	}
	sort.Slice(survivors, func(i, j int) bool { return survivors[i].PID < survivors[j].PID })
	return survivors, errors.Join(err, inventoryErr)
}

// PTYs start a new session/process group. Tests and non-PTY callers may not;
// never signal a group shared with the egg or its parent.
func signalSessionGroup(sess *Session, signal syscall.Signal) error {
	pid := sess.cmd.Process.Pid
	group, err := syscall.Getpgid(pid)
	if sess.processGroupID == pid || (err == nil && group == pid) {
		err = syscall.Kill(-pid, signal)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	err = sess.cmd.Process.Signal(signal)
	if errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}
