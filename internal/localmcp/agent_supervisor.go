package localmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/daemonctl"
	"github.com/ehrlich-b/wingthing/internal/procinfo"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/taskrun"
)

// A private stdin payload freezes the caller's execution boundary without
// putting prompts, identity, or credentials in argv. Only the supervisor's
// readiness pipe is inherited; none of the MCP transport descriptors survive.
type agentRunLaunch struct {
	Version     string
	Principal   string
	LauncherPID int
	Options     taskrun.TaskRunOptions
	ParentID    string
	Direction   string
}

func agentSupervisorArgs(runID, stateDir string) []string {
	return []string{"egg", "supervise-run", "--run-id", runID, "--state-dir", stateDir}
}

func (s *Server) spawnAgentRun(runID string, followup *agentRunFollowup, options taskrun.TaskRunOptions) error {
	launch := agentRunLaunch{Version: s.Version, Principal: s.clientPrincipal(), LauncherPID: os.Getpid(), Options: options}
	if followup != nil {
		launch.ParentID, launch.Direction = followup.parentID, followup.direction
	}
	data, err := json.Marshal(launch)
	if err != nil {
		return fmt.Errorf("encode agent supervisor launch: %w", err)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if strings.HasSuffix(filepath.Base(exe), ".test") {
		return errors.New("refusing to start agent supervisor from test binary; use a built wt receiver")
	}
	stateDir, err := filepath.Abs(s.Cfg.Dir)
	if err != nil {
		return fmt.Errorf("resolve agent supervisor state: %w", err)
	}
	ready, readyWriter, err := os.Pipe()
	if err != nil {
		return err
	}
	defer ready.Close()
	defer readyWriter.Close()
	dir := filepath.Join(s.Cfg.Dir, "runs", runID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(dir, "supervisor.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer cmdutil.CloseWithLog("agent supervisor log", logFile)
	child := exec.Command(exe, agentSupervisorArgs(runID, stateDir)...)
	child.Stdin = bytes.NewReader(data)
	child.Stdout, child.Stderr = logFile, logFile
	child.ExtraFiles = []*os.File{readyWriter}
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	child.Env = os.Environ()
	if err := child.Start(); err != nil {
		return fmt.Errorf("start agent supervisor: %w", err)
	}
	_ = readyWriter.Close()
	ack := make(chan error, 1)
	go func() {
		var b [1]byte
		_, err := io.ReadFull(ready, b[:])
		if err == nil && b[0] != 1 {
			err = errors.New("invalid agent supervisor acknowledgement")
		}
		ack <- err
	}()
	select {
	case err = <-ack:
	case <-time.After(5 * time.Second):
		err = errors.New("agent supervisor did not become ready within 5s")
	}
	if err != nil {
		daemonctl.AbandonStartedDaemon(child)
		return fmt.Errorf("agent supervisor startup (log %s): %w", filepath.Join(dir, "supervisor.log"), err)
	}
	// Reap while this MCP host exists. The supervisor does not depend on the
	// waiter; init adopts it if the submitting host exits first.
	go func() {
		if err := child.Wait(); err != nil {
			s.markLostAgentSupervisor(runID, child.Process.Pid, fmt.Sprintf("supervisor exit: %v; provider exit unknown", err))
		}
	}()
	return nil
}

// RunAgentSupervisor is the hidden re-exec entry point. Admission and the
// frozen egg policy belong to the submitting adapter; this process owns only
// that already-authorized run. Claiming its PID precedes the readiness ack.
func RunAgentSupervisor(ctx context.Context, cfg *config.Config, runID string, input io.Reader, ready *os.File) error {
	data, err := io.ReadAll(io.LimitReader(input, 1<<20+1))
	if err != nil {
		return err
	}
	if len(data) > 1<<20 {
		return errors.New("agent supervisor launch exceeds 1 MiB")
	}
	var launch agentRunLaunch
	if err := decodeStrict(data, &launch); err != nil {
		return err
	}
	s := &Server{Version: launch.Version, Cfg: cfg, Principal: launch.Principal}
	task, taskStore, err := s.ownedAgentRun(runID)
	if err != nil {
		return err
	}
	defer cmdutil.CloseWithLog("supervisor task store", taskStore)
	if task.Status != "pending" || task.RunnerPID != launch.LauncherPID {
		return errors.New("agent run is already claimed or terminal")
	}
	identity, err := procinfo.ProcessIdentity(os.Getpid())
	if err != nil {
		return fmt.Errorf("identify agent supervisor: %w", err)
	}
	if err := taskStore.ClaimAgentRunSupervisor(runID, launch.LauncherPID, os.Getpid(), identity); err != nil {
		return err
	}
	if _, err := ready.Write([]byte{1}); err != nil {
		s.setAgentRunError(runID, err)
		return err
	}
	if err := ready.Close(); err != nil {
		s.setAgentRunError(runID, err)
		return err
	}
	var followup *agentRunFollowup
	if launch.ParentID != "" {
		followup = &agentRunFollowup{parentID: launch.ParentID, direction: launch.Direction}
	}
	if err := s.executeAgentRun(ctx, runID, followup, launch.Options); err != nil {
		s.setAgentRunError(runID, err)
		return err
	}
	return nil
}

func (s *Server) markLostAgentSupervisor(runID string, pid int, message string) {
	taskStore, err := store.Open(s.Cfg.DBPath())
	if err != nil {
		return
	}
	defer cmdutil.CloseWithLog("lost supervisor store", taskStore)
	_ = taskStore.MarkAgentRunOrphaned(runID, pid, message)
}

func (s *Server) reconcileAgentRun(taskStore *store.Store, task *store.Task) (*store.Task, error) {
	if (task.Status != "pending" && task.Status != "running") || task.RunnerPID <= 0 || s.agentSupervisorIsAlive(task) {
		return task, nil
	}
	message := fmt.Sprintf("supervising Wingthing process %d exited or changed identity; provider exit unknown", task.RunnerPID)
	if err := taskStore.MarkAgentRunOrphaned(task.ID, task.RunnerPID, message); err != nil {
		return nil, fmt.Errorf("mark orphaned agent run: %w", err)
	}
	return taskStore.GetTask(task.ID)
}

func (s *Server) agentSupervisorIsAlive(task *store.Task) bool {
	if !procinfo.OwnedProcessIsAlive(task.RunnerPID) {
		return false
	}
	if task.RunnerIdentity != "" {
		identity, err := procinfo.ProcessIdentity(task.RunnerPID)
		return err == nil && identity == task.RunnerIdentity
	}
	// Runs admitted by older binaries have no saved start identity. Retain
	// compatibility when argv verifies the run's supervisor or the old host.
	argv, err := procinfo.ProcessArgv(task.RunnerPID)
	if err != nil {
		return false
	}
	if agentSupervisorArgvMatches(argv, task.ID, s.Cfg.Dir) {
		return true
	}
	if !agentLegacyHostArgvMatches(argv) {
		return false
	}
	startedAt, err := procinfo.ProcessStartTime(task.RunnerPID)
	return err == nil && !startedAt.After(task.CreatedAt)
}

func (s *Server) stopDetachedAgentRun(taskStore *store.Store, task *store.Task) error {
	if !s.agentSupervisorIsAlive(task) {
		return nil
	}
	argv, err := procinfo.ProcessArgv(task.RunnerPID)
	if err != nil {
		if !procinfo.OwnedProcessIsAlive(task.RunnerPID) {
			return nil
		}
		return fmt.Errorf("inspect agent supervisor: %w", err)
	}
	if agentLegacyHostArgvMatches(argv) {
		return fmt.Errorf("agent run is owned by legacy Wingthing MCP host PID %d; stop it through that host", task.RunnerPID)
	}
	if !agentSupervisorArgvMatches(argv, task.ID, s.Cfg.Dir) {
		return errors.New("run is no longer attached to a verified Wingthing supervisor")
	}
	proc, err := os.FindProcess(task.RunnerPID)
	if err != nil {
		return err
	}
	if !s.agentSupervisorIsAlive(task) {
		return nil
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil && s.agentSupervisorIsAlive(task) {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return waitForAgentRunCondition(ctx, func() (bool, error) {
		if !s.agentSupervisorIsAlive(task) {
			return true, nil
		}
		argv, err := procinfo.ProcessArgv(task.RunnerPID)
		if err != nil {
			return false, err
		}
		return !agentSupervisorArgvMatches(argv, task.ID, s.Cfg.Dir), nil
	})
}

func agentLegacyHostArgvMatches(argv []string) bool {
	// The previous release ran agent_run inside `wt mcp stdio`, with optional
	// client, conversation and isolation flags following the subcommands.
	return len(argv) >= 3 && argv[1] == "mcp" && argv[2] == "stdio"
}

func agentSupervisorArgvMatches(argv []string, runID, stateDir string) bool {
	want := agentSupervisorArgs(runID, stateDir)
	if len(argv) != len(want)+1 {
		return false
	}
	last := len(argv) - 1
	if strings.Join(argv[1:last], "\x00") != strings.Join(want[:len(want)-1], "\x00") {
		return false
	}
	// Different clients may select the same state through a symlink alias.
	actual, err := os.Stat(argv[last])
	if err != nil || !actual.IsDir() {
		return false
	}
	expected, err := os.Stat(stateDir)
	return err == nil && expected.IsDir() && os.SameFile(actual, expected)
}
