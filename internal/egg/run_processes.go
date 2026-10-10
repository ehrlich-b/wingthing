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

// RunDescendant reports identity only, never command arguments or diagnostics.
type RunDescendant struct {
	PID            int `json:"pid"`
	ParentPID      int `json:"parent_pid"`
	ProcessGroupID int `json:"process_group_id"`
}

type observedProcess struct {
	RunDescendant
	start   string
	session int
	zombie  bool
}

type processBoundary interface {
	attach(int) error
	kill() error
	members() ([]int, error)
	close() error
}

type runProcessTree struct {
	mu          sync.Mutex
	killMu      sync.Mutex
	root        int
	rootStart   string
	seen        map[int]observedProcess
	boundary    processBoundary
	adopt       bool // only enabled by the dedicated egg before launching its provider
	baseline    map[int]string
	stop        chan struct{}
	monitorDone chan struct{}
	cleaned     bool
}

func (tree *runProcessTree) observe(snapshot map[int]observedProcess) {
	tree.mu.Lock()
	defer tree.mu.Unlock()
	if tree.seen == nil {
		tree.seen = make(map[int]observedProcess)
	}
	root, rootAlive := snapshot[tree.root]
	if tree.rootStart == "" && rootAlive {
		tree.rootStart = root.start
	}
	rootAlive = rootAlive && root.start == tree.rootStart
	for changed := true; changed; {
		changed = false
		// A live, previously identified member is required before using a group
		// or session as evidence. Numeric group/session IDs alone can be reused.
		groups, sessions := map[int]bool{}, map[int]bool{}
		addScope := func(p observedProcess) {
			if p.ProcessGroupID == tree.root || p.ProcessGroupID == p.PID {
				groups[p.ProcessGroupID] = true
			}
			if p.session == tree.root || p.session == p.PID {
				sessions[p.session] = true
			}
		}
		if rootAlive {
			addScope(root)
		}
		for pid, old := range tree.seen {
			if current, ok := snapshot[pid]; ok && current.start == old.start {
				addScope(current)
			}
		}
		for pid, process := range snapshot {
			if pid == tree.root {
				continue
			}
			oldParent, parentSeen := tree.seen[process.ParentPID]
			parent, present := snapshot[process.ParentPID]
			parentSeen = parentSeen && present && parent.start == oldParent.start
			adopted := tree.adopt && process.ParentPID == os.Getpid() && tree.baseline[pid] != process.start
			if !(rootAlive && process.ParentPID == tree.root) && !parentSeen &&
				!groups[process.ProcessGroupID] && !sessions[process.session] && !adopted {
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

func (tree *runProcessTree) start(pid int) error {
	tree.root = pid // launch barrier is still closed; no concurrent readers yet
	snapshot, err := processSnapshot()
	if err == nil {
		tree.observe(snapshot)
	} else if tree.boundary == nil {
		return err
	}
	if tree.boundary != nil {
		if err := tree.boundary.attach(pid); err != nil {
			return err // never release an uncontained provider after an attach failure
		}
	}
	// Inventory failure cannot disable a kernel boundary. Sweeps still report
	// that verification was unavailable when the session stops.
	tree.stop, tree.monitorDone = make(chan struct{}), make(chan struct{})
	go func() {
		defer close(tree.monitorDone)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-tree.stop:
				return
			case <-ticker.C:
				if snapshot, err := processSnapshot(); err == nil {
					tree.observe(snapshot)
				}
			}
		}
	}()
	return nil
}

func (tree *runProcessTree) targets(snapshot map[int]observedProcess) []observedProcess {
	tree.observe(snapshot)
	if tree.boundary != nil {
		if pids, err := tree.boundary.members(); err == nil {
			tree.mu.Lock()
			for _, pid := range pids {
				if p, ok := snapshot[pid]; ok && pid != tree.root {
					tree.seen[pid] = p
				}
			}
			tree.mu.Unlock()
		}
	}
	tree.mu.Lock()
	defer tree.mu.Unlock()
	var targets []observedProcess
	if root, ok := snapshot[tree.root]; ok && root.start == tree.rootStart {
		targets = append(targets, root)
	}
	for pid, old := range tree.seen {
		if current, ok := snapshot[pid]; ok && current.start == old.start {
			targets = append(targets, current)
		}
	}
	return targets
}

func (tree *runProcessTree) kill(sess *Session) ([]RunDescendant, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return tree.terminate(ctx, sess, 3*time.Second)
}

func (tree *runProcessTree) terminate(ctx context.Context, sess *Session, grace time.Duration) ([]RunDescendant, error) {
	tree.killMu.Lock()
	defer tree.killMu.Unlock()
	if tree.cleaned {
		return nil, nil
	}
	defer func() {
		if tree.stop != nil {
			close(tree.stop)
			<-tree.monitorDone
			tree.stop = nil
		}
	}()
	before, inventoryErr := processSnapshot()
	if inventoryErr == nil {
		tree.observe(before)
	}
	err := terminateSession(ctx, sess, grace)
	if tree.boundary != nil {
		err = errors.Join(err, tree.boundary.kill())
	}
	// Refresh ancestry before every signal, including after parents exit. On
	// Linux adopted orphans remain attributable even after setsid/double-fork.
	// Freeze identified members before killing so a fork storm cannot keep
	// creating new children between enumeration and signal delivery.
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var survivors []RunDescendant
	for {
		snapshot, scanErr := processSnapshot()
		if scanErr != nil {
			return nil, errors.Join(err, inventoryErr, scanErr)
		}
		targets := tree.targets(snapshot)
		survivors = nil
		for _, process := range targets {
			if process.zombie {
				reapDescendant(process, tree.root)
				continue
			}
			if process.PID != tree.root {
				survivors = append(survivors, process.RunDescendant)
			}
			err = errors.Join(err, signalIdentifiedProcess(process, syscall.SIGSTOP))
		}
		for _, process := range targets {
			if !process.zombie {
				err = errors.Join(err, signalIdentifiedProcess(process, syscall.SIGKILL))
			}
		}
		populated := false
		if tree.boundary != nil {
			members, memberErr := tree.boundary.members()
			err = errors.Join(err, memberErr)
			populated = len(members) != 0
		}
		alive := false
		for _, process := range targets {
			alive = alive || !process.zombie
		}
		if !alive && !populated {
			break
		}
		select {
		case <-ctx.Done():
			sort.Slice(survivors, func(i, j int) bool { return survivors[i].PID < survivors[j].PID })
			return survivors, errors.Join(err, inventoryErr, ctx.Err())
		case <-ticker.C:
		}
	}
	if tree.boundary != nil {
		err = errors.Join(err, tree.boundary.close())
	}
	err = errors.Join(err, inventoryErr)
	tree.cleaned = err == nil
	return nil, err
}

// PTYs start a new session/process group. Tests and non-PTY callers may not;
// never signal a group shared with the egg or its parent. With an inventory,
// signal members individually so reused group IDs cannot target other work.
func signalSessionGroup(sess *Session, signal syscall.Signal) error {
	if sess.processTree != nil {
		snapshot, err := processSnapshot()
		if err != nil {
			// The direct child's os.Process handle is safe even when enumeration
			// fails; the cgroup is killed separately by terminate.
			return signalDirectProcess(sess, signal)
		}
		var signalErr error
		for _, process := range sess.processTree.targets(snapshot) {
			signalErr = errors.Join(signalErr, signalIdentifiedProcess(process, signal))
		}
		return signalErr
	}
	pid := sess.cmd.Process.Pid
	group, err := syscall.Getpgid(pid)
	if err == nil && group == pid {
		err = syscall.Kill(-pid, signal)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	return signalDirectProcess(sess, signal)
}

func signalDirectProcess(sess *Session, signal syscall.Signal) error {
	err := sess.cmd.Process.Signal(signal)
	if errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

func stopSessionProcesses(ctx context.Context, sess *Session, grace time.Duration) error {
	if sess.processTree == nil {
		return terminateSession(ctx, sess, grace)
	}
	survivors, err := sess.processTree.terminate(ctx, sess, grace)
	if len(survivors) != 0 {
		err = errors.Join(err, errors.New("provider descendants survived termination"))
	}
	return err
}
