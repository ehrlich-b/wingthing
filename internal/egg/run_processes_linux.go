//go:build linux

package egg

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/ehrlich-b/wingthing/internal/sandbox"
	"golang.org/x/sys/unix"
)

func parseProcStat(data string) (observedProcess, error) {
	// comm can contain spaces, parentheses and newlines. All numeric identity
	// fields follow the LAST closing parenthesis, not the first one.
	end := strings.LastIndexByte(data, ')')
	begin := strings.IndexByte(data, '(')
	if begin < 1 || end < begin {
		return observedProcess{}, errors.New("invalid process stat")
	}
	fields := strings.Fields(data[end+1:])
	if len(fields) < 20 {
		return observedProcess{}, errors.New("short process stat")
	}
	pid, e1 := strconv.Atoi(strings.TrimSpace(data[:begin]))
	ppid, e2 := strconv.Atoi(fields[1])
	pgid, e3 := strconv.Atoi(fields[2])
	sid, e4 := strconv.Atoi(fields[3])
	_, e5 := strconv.ParseUint(fields[19], 10, 64) // field 22: kernel start ticks
	if errors.Join(e1, e2, e3, e4, e5) != nil {
		return observedProcess{}, errors.New("invalid process identity")
	}
	return observedProcess{RunDescendant: RunDescendant{pid, ppid, pgid}, start: fields[19], session: sid, zombie: fields[0] == "Z"}, nil
}

func readProcProcess(pid int) (observedProcess, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return observedProcess{}, err
	}
	return parseProcStat(string(data))
}

func processSnapshot() (map[int]observedProcess, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, errors.New("process descendant inventory unavailable")
	}
	out := make(map[int]observedProcess)
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		p, err := readProcProcess(pid)
		if errors.Is(err, os.ErrNotExist) {
			continue // process exited during enumeration
		}
		if err != nil {
			return nil, errors.New("process descendant inventory unavailable")
		}
		out[pid] = p
	}
	return out, nil
}

func signalIdentifiedProcess(process observedProcess, signal syscall.Signal) error {
	fd, err := unix.PidfdOpen(process.PID, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil && !errors.Is(err, unix.ENOSYS) && !errors.Is(err, unix.EINVAL) {
		return err
	}
	if fd >= 0 {
		defer unix.Close(fd)
	}
	current, readErr := readProcProcess(process.PID)
	if errors.Is(readErr, os.ErrNotExist) {
		return nil
	}
	if readErr != nil {
		return errors.New("process identity unavailable")
	}
	if current.start != process.start || current.zombie {
		return nil
	}
	if err == nil {
		err = unix.PidfdSendSignal(fd, unix.Signal(signal), nil, 0)
	} else {
		err = syscall.Kill(process.PID, signal)
	}
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	return err
}

func reapDescendant(process observedProcess, root int) {
	if process.PID != root && process.ParentPID == os.Getpid() {
		// Never Wait4(-1): exec.Cmd owns the provider and every direct helper.
		current, err := readProcProcess(process.PID)
		if err == nil && current.start == process.start && current.zombie {
			_, _ = unix.Wait4(process.PID, nil, unix.WNOHANG, nil)
		}
	}
}

type eggCgroup struct {
	path  string
	owned bool
}

func newEggCgroup(sb sandbox.Sandbox) (*eggCgroup, error) {
	if resource, ok := sb.(interface{ CgroupPath() string }); ok && resource.CgroupPath() != "" {
		return &eggCgroup{path: resource.CgroupPath()}, nil
	}
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "0::/") {
			continue
		}
		parent := filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(line, "0::"))
		// Some cgroup namespaces expose paths above their visible root.
		if strings.Contains(strings.TrimPrefix(line, "0::"), "..") {
			return nil, errors.New("cgroup delegation is outside the visible hierarchy")
		}
		path, err := os.MkdirTemp(parent, "wt-containment-")
		if err != nil {
			return nil, fmt.Errorf("cgroup v2 is not delegated: %w", err)
		}
		return &eggCgroup{path: path, owned: true}, nil
	}
	return nil, errors.New("cgroup v2 unavailable")
}

func (cg *eggCgroup) attach(pid int) error {
	return writeCgroupFile(filepath.Join(cg.path, "cgroup.procs"), strconv.Itoa(pid))
}

func writeCgroupFile(path, value string) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, err = f.WriteString(value)
	return errors.Join(err, f.Close())
}

func (cg *eggCgroup) members() ([]int, error) {
	var members []int
	err := filepath.WalkDir(cg.path, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Name() != "cgroup.procs" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, value := range strings.Fields(string(data)) {
			pid, err := strconv.Atoi(value)
			if err != nil {
				return err
			}
			members = append(members, pid)
		}
		return nil
	})
	return members, err
}

func (cg *eggCgroup) kill() error {
	// Linux 5.14+: cgroup.kill is recursive and handles concurrent fork/move.
	err := writeCgroupFile(filepath.Join(cg.path, "cgroup.kill"), "1")
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Older v2 kernels: the identity-checked tree sweep remains active. Include
	// every member, even a daemon that escaped ancestry before the first scan.
	pids, err := cg.members()
	if err != nil {
		return err
	}
	for _, pid := range pids {
		p, readErr := readProcProcess(pid)
		if errors.Is(readErr, os.ErrNotExist) {
			continue
		}
		if readErr != nil {
			return readErr
		}
		err = errors.Join(err, signalIdentifiedProcess(p, syscall.SIGSTOP), signalIdentifiedProcess(p, syscall.SIGKILL))
	}
	return err
}

func (cg *eggCgroup) close() error {
	if cg.owned {
		return os.Remove(cg.path)
	}
	return nil // resource cgroups are removed by sandbox.Destroy after cleanup
}

func prepareProcessTree(_ *exec.Cmd, sb sandbox.Sandbox) (*runProcessTree, error) {
	// Each real egg is a dedicated process. Snapshot its pre-existing children
	// before launch; newly adopted orphans belong to this egg's sole provider.
	snapshot, err := processSnapshot()
	if err != nil {
		return nil, err
	}
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		return nil, fmt.Errorf("enable egg child subreaper: %w", err)
	}
	tree := &runProcessTree{adopt: true, baseline: make(map[int]string)}
	for pid, p := range snapshot {
		if p.ParentPID == os.Getpid() {
			tree.baseline[pid] = p.start
		}
	}
	if cg, err := newEggCgroup(sb); err == nil {
		tree.boundary = cg
	}
	return tree, nil
}
