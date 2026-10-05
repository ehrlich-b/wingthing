package daemonctl

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/procinfo"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
)

// daemonStateDir selects the state that owns daemon pid/args/log/status and
// the lifecycle lock. It never substitutes DefaultDir for a selection that
// failed: an unresolvable StateDir is an error, and a selected preview state
// that cannot load (for example an invalid provider binding) returns its own
// directory with the load error so lifecycle mutations fail closed.
func daemonStateDir() (string, error) {
	cfg, loadErr := config.Load()
	if loadErr == nil {
		return cfg.Dir, nil
	}
	dir, err := config.StateDir()
	if err != nil {
		return "", err
	}
	if config.Channel() == "preview" {
		return dir, loadErr
	}
	return dir, nil
}

// daemonStatePath is empty when no state can be selected. Lifecycle
// mutations never reach it then: acquireDaemonLifecycleLock refuses first.
func daemonStatePath(name string) string {
	dir, _ := daemonStateDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, name)
}

func WingPidPath() string {
	return daemonStatePath("wing.pid")
}

const maxLogSize = 1 << 20 // 1MB

// rotateLog rotates path when it exceeds maxLogSize.
// Chain: .log -> .log.1 -> .log.2.gz -> deleted
func RotateLog(path string) error {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) || (err == nil && info.Size() < maxLogSize) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect log for rotation: %w", err)
	}

	// Delete oldest (.log.2.gz)
	if err := cmdutil.RemoveIfExists(path + ".2.gz"); err != nil {
		return fmt.Errorf("remove oldest rotated log: %w", err)
	}

	// Compress .log.1 -> .log.2.gz
	if data, err := os.ReadFile(path + ".1"); err == nil {
		if gz, err := os.Create(path + ".2.gz"); err == nil {
			w := gzip.NewWriter(gz)
			if _, werr := w.Write(data); werr != nil {
				cmdutil.CloseWithLog("rotated gzip stream", w)
				cmdutil.CloseWithLog("rotated log", gz)
				return fmt.Errorf("compress rotated log: %w", werr)
			}
			if err := w.Close(); err != nil {
				cmdutil.CloseWithLog("rotated log", gz)
				return fmt.Errorf("finish rotated log compression: %w", err)
			}
			if err := gz.Close(); err != nil {
				return fmt.Errorf("close rotated log: %w", err)
			}
			if err := cmdutil.RemoveIfExists(path + ".1"); err != nil {
				return fmt.Errorf("remove compressed source log: %w", err)
			}
		} else {
			return fmt.Errorf("create compressed rotated log: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read rotated log: %w", err)
	}

	// Rotate current -> .log.1
	if err := os.Rename(path, path+".1"); err != nil {
		return fmt.Errorf("rotate current log: %w", err)
	}
	return nil
}

func WingArgsPath() string {
	return daemonStatePath("wing.args")
}

func WingLogPath() string {
	return daemonStatePath("wing.log")
}

func WingStatusPath() string {
	return daemonStatePath("wing.status")
}

// wingStatus is the JSON schema for wing.status.
type WingStatus struct {
	State    string `json:"state"` // connecting, connected, auth_failed, disconnected
	Error    string `json:"error,omitempty"`
	TS       string `json:"ts"`
	RoostURL string `json:"roost_url,omitempty"`
}

func writeWingStatus(state, lastErr string) {
	WriteWingStatusForRoost(state, lastErr, "")
}

func WriteWingStatusForRoost(state, lastErr, roostURL string) {
	s := WingStatus{
		State:    state,
		Error:    lastErr,
		TS:       time.Now().UTC().Format(time.RFC3339),
		RoostURL: wingpolicy.RelayMetadataURL(roostURL),
	}
	data, err := json.Marshal(s)
	if err != nil {
		log.Printf("encode wing status: %v", err)
		return
	}
	// A custom coordinator URL can itself carry deployment metadata. Keep the
	// status private just like the saved daemon arguments that selected it.
	if err := WriteAtomicMetadataFile(WingStatusPath(), data, 0600); err != nil {
		log.Printf("write wing status: %v", err)
	}
}

func ReadWingStatus() (*WingStatus, error) {
	data, err := os.ReadFile(WingStatusPath())
	if err != nil {
		return nil, err
	}
	var s WingStatus
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// waitForWingStatus polls wing.status for up to timeout, returning the final state.
// Returns "connected", "auth_failed", or "" (timeout/still connecting).
func WaitForWingStatus(pid int, timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		// Check if daemon died
		if !procinfo.OwnedProcessIsAlive(pid) {
			// Process exited — check final status
			if s, err := ReadWingStatus(); err == nil {
				return s.State
			}
			return "auth_failed" // daemon died, likely auth
		}
		if s, err := ReadWingStatus(); err == nil {
			switch s.State {
			case "connected", "auth_failed":
				return s.State
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return ""
}

func RoostPidPath() string {
	return daemonStatePath("roost.pid")
}

func RoostArgsPath() string {
	return daemonStatePath("roost.args")
}

func RoostLogPath() string {
	return daemonStatePath("roost.log")
}

func WriteDaemonMetadata(pidPath, argsPath string, pid int, args []string) error {
	if err := WriteAtomicMetadataFile(argsPath, []byte(strings.Join(args, "\n")), 0600); err != nil {
		return fmt.Errorf("write daemon args: %w", err)
	}
	if err := WriteAtomicMetadataFile(pidPath, []byte(strconv.Itoa(pid)), 0644); err != nil {
		_ = os.Remove(argsPath)
		return fmt.Errorf("write daemon pid: %w", err)
	}
	return nil
}

func WriteAtomicMetadataFile(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".wt-daemon-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer cmdutil.RemoveWithLog(tmpPath)
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer cmdutil.CloseWithLog("metadata directory", dir)
	return dir.Sync()
}

func AcquireDaemonLifecycleLock() (*os.File, error) {
	dir, err := daemonStateDir()
	if err != nil {
		return nil, fmt.Errorf("select daemon state: %w", err)
	}
	return AcquireDaemonLifecycleLockAt(filepath.Join(dir, "daemon.lock"))
}

func AcquireDaemonLifecycleLockAt(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open daemon lifecycle lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, fmt.Errorf("another daemon start/stop is already in progress")
		}
		return nil, fmt.Errorf("lock daemon lifecycle: %w", err)
	}
	return file, nil
}

// abandonStartedDaemon terminates and reaps a child whose startup could not be
// committed to disk. This prevents a successful exec from becoming an
// invisible daemon when readiness or metadata persistence fails.
func AbandonStartedDaemon(child *exec.Cmd) {
	if child == nil || child.Process == nil {
		return
	}
	_ = child.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		_ = child.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = child.Process.Kill()
		<-done
	}
}

type DaemonKind string

const (
	WingDaemon  DaemonKind = "wing"
	RoostDaemon DaemonKind = "roost"
)

var (
	ErrNoDaemonRunning = errors.New("no daemon running")
	errStaleDaemonPID  = errors.New("stale daemon pid")
)

// readPidFrom reads a PID from a specific file and verifies that it still
// identifies the expected wt foreground daemon. PIDs are recycled, so merely
// finding a live same-UID process is not enough before callers send signals.
func ReadPidFrom(path string, kind DaemonKind) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		_ = os.Remove(path)
		return 0, fmt.Errorf("%w: invalid PID: %v", errStaleDaemonPID, err)
	}
	if !procinfo.OwnedProcessIsAlive(pid) {
		_ = os.Remove(path)
		return 0, errStaleDaemonPID
	}
	matches, inspectErr := inspectDaemonPid(pid, kind)
	if inspectErr != nil {
		return 0, inspectErr
	}
	if !matches {
		_ = os.Remove(path)
		return 0, errStaleDaemonPID
	}
	return pid, nil
}

// daemonPidMatches confirms the command shape emitted by wingStartCmd or
// roostStartCmd. Failure to inspect argv fails closed: a status check may call
// a daemon stopped, but stop/update will never signal an unconfirmed process.
func inspectDaemonPid(pid int, kind DaemonKind) (bool, error) {
	argv, err := procinfo.ProcessArgv(pid)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
			return false, nil
		}
		return false, fmt.Errorf("inspect %s daemon pid %d: %w", kind, pid, err)
	}
	return daemonArgvMatches(argv, kind), nil
}

func daemonArgvMatches(argv []string, kind DaemonKind) bool {
	if config.Channel() == "preview" {
		exe, err := os.Executable()
		if err != nil || len(argv) == 0 || wingpolicy.CanonicalPolicyPath(argv[0]) != wingpolicy.CanonicalPolicyPath(exe) {
			return false
		}
	}
	if len(argv) < 4 || argv[2] != "start" {
		return false
	}
	switch kind {
	case WingDaemon:
		if argv[1] != "wing" && argv[1] != "daemon" {
			return false
		}
	case RoostDaemon:
		if argv[1] != "roost" {
			return false
		}
	default:
		return false
	}
	for _, arg := range argv[3:] {
		if arg == "--foreground" {
			return true
		}
	}
	return false
}

func ParseSavedDaemonArgs(data []byte, kind DaemonKind) ([]string, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil, fmt.Errorf("empty daemon args")
	}
	args := strings.Split(trimmed, "\n")
	executable := "wt"
	if config.Channel() == "preview" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			return nil, err
		}
	}
	argv := append([]string{executable}, args...)
	if !daemonArgvMatches(argv, kind) {
		return nil, fmt.Errorf("saved args do not describe a %s foreground daemon", kind)
	}
	return args, nil
}

// readDaemon returns the one live daemon represented by local metadata. A
// healthy installation cannot run a standalone wing and a roost at once. If
// both metadata files identify live daemons, fail closed so lifecycle commands
// do not stop an arbitrary half of the conflicting installation.
func ReadDaemon() (int, DaemonKind, error) {
	wingPID, wingErr := ReadPidFrom(WingPidPath(), WingDaemon)
	roostPID, roostErr := ReadPidFrom(RoostPidPath(), RoostDaemon)
	if wingErr == nil && roostErr == nil {
		return 0, "", fmt.Errorf("both wing (pid %d) and roost (pid %d) daemons are running", wingPID, roostPID)
	}
	if wingErr == nil {
		if !DaemonAbsentError(roostErr) {
			return 0, "", roostErr
		}
		return wingPID, WingDaemon, nil
	}
	if roostErr == nil {
		if !DaemonAbsentError(wingErr) {
			return 0, "", wingErr
		}
		return roostPID, RoostDaemon, nil
	}
	if !DaemonAbsentError(wingErr) {
		return 0, "", wingErr
	}
	if !DaemonAbsentError(roostErr) {
		return 0, "", roostErr
	}
	return 0, "", ErrNoDaemonRunning
}

func DaemonAbsentError(err error) bool {
	return os.IsNotExist(err) || errors.Is(err, errStaleDaemonPID)
}

// readPid tries wing.pid first, then roost.pid. Returns the first live daemon PID.
func ReadPid() (int, error) {
	pid, _, err := ReadDaemon()
	return pid, err
}

// stopDaemonAndWait revalidates the daemon command immediately before
// signaling it, then waits until that specific daemon identity is gone. This
// keeps the lifecycle lock meaningful: callers must not delete metadata and
// allow a replacement to start while the old listener is still shutting down.
func StopDaemonAndWait(pid int, kind DaemonKind, timeout time.Duration) error {
	if !procinfo.OwnedProcessIsAlive(pid) {
		return nil
	}
	matches, inspectErr := inspectDaemonPid(pid, kind)
	if inspectErr != nil {
		return inspectErr
	}
	if !matches {
		return nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find %s daemon pid %d: %w", kind, pid, err)
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		if !procinfo.OwnedProcessIsAlive(pid) {
			return nil
		}
		matches, inspectErr = inspectDaemonPid(pid, kind)
		if inspectErr != nil {
			return inspectErr
		}
		if !matches {
			return nil
		}
		return fmt.Errorf("stop %s daemon pid %d: %w", kind, pid, err)
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if !procinfo.OwnedProcessIsAlive(pid) {
			return nil
		}
		matches, inspectErr = inspectDaemonPid(pid, kind)
		if inspectErr != nil {
			return inspectErr
		}
		if !matches {
			return nil
		}
		select {
		case <-deadline.C:
			return fmt.Errorf("%s daemon pid %d did not stop within %s", kind, pid, timeout)
		case <-ticker.C:
		}
	}
}

func SignalDaemon(sig os.Signal) error {
	pid, err := ReadPid()
	if err != nil {
		if DaemonAbsentError(err) {
			return nil
		}
		return fmt.Errorf("find daemon to signal: %w", err)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find daemon process %d: %w", pid, err)
	}
	if err := proc.Signal(sig); err != nil {
		return fmt.Errorf("signal daemon process %d: %w", pid, err)
	}
	return nil
}
