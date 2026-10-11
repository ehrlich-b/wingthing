package egg

import (
	"context"
	"errors"
	"os"
	"sort"
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
	return processSnapshotContext(context.Background())
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

func sessionOwnedGroupID(sess *Session) int {
	pid := sess.cmd.Process.Pid
	group, err := syscall.Getpgid(pid)
	if sess.processGroupID == pid || (err == nil && group == pid) {
		return pid
	}
	return 0
}

// PTYs start a new session/process group. Tests and non-PTY callers may not;
// never signal a group shared with the egg or its parent.
func signalSessionGroup(sess *Session, pgid int, signal syscall.Signal) error {
	if pgid != 0 {
		err := syscall.Kill(-pgid, signal)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return sessionGroupSignalError(pgid, err)
	}
	err := sess.cmd.Process.Signal(signal)
	if errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}
