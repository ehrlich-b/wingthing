package daemonctl

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
)

// restartWingDaemonIfRunning stops the running wing daemon and starts a new one
// with the same args so it picks up the new auth token.
func RestartWingDaemonIfRunning() error {
	lifecycleLock, lockErr := AcquireDaemonLifecycleLock()
	if lockErr != nil {
		return lockErr
	}
	defer cmdutil.CloseWithLog("daemon lifecycle lock", lifecycleLock)

	pid, err := ReadPidFrom(WingPidPath(), WingDaemon)
	if err != nil {
		if DaemonAbsentError(err) {
			return nil // no standalone wing daemon running, nothing to do
		}
		return fmt.Errorf("inspect wing daemon state: %w", err)
	}

	// Read saved args so we can restart with same flags
	argsData, err := os.ReadFile(WingArgsPath())
	if err != nil {
		return fmt.Errorf("can't read wing.args (stop and restart manually: wt stop && wt start): %w", err)
	}
	savedArgs, err := ParseSavedDaemonArgs(argsData, WingDaemon)
	if err != nil {
		return fmt.Errorf("can't use wing.args (stop and restart manually: wt stop && wt start): %w", err)
	}

	// Stop the old daemon
	fmt.Printf("restarting wing daemon (pid %d)...\n", pid)
	if err := StopDaemonAndWait(pid, WingDaemon, 5*time.Second); err != nil {
		return fmt.Errorf("refusing to start a competing daemon: %w", err)
	}
	if err := cmdutil.RemoveFiles(WingPidPath(), WingArgsPath(), WingStatusPath()); err != nil {
		return fmt.Errorf("remove stopped daemon metadata: %w", err)
	}

	// Start new daemon with same args
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find executable: %w", err)
	}

	if err := RotateLog(WingLogPath()); err != nil {
		return err
	}
	logFile, err := os.OpenFile(WingLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open log: %w", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		cmdutil.CloseWithLog("wing log", logFile)
		return fmt.Errorf("resolve user home: %w", err)
	}
	child := exec.Command(exe, savedArgs...)
	child.Dir = home
	child.Stdout = logFile
	child.Stderr = logFile
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := child.Start(); err != nil {
		cmdutil.CloseWithLog("wing log", logFile)
		return fmt.Errorf("start daemon: %w", err)
	}
	cmdutil.CloseWithLog("wing log", logFile)

	if err := WriteDaemonMetadata(WingPidPath(), WingArgsPath(), child.Process.Pid, savedArgs); err != nil {
		AbandonStartedDaemon(child)
		return fmt.Errorf("restart daemon: %w", err)
	}

	result := WaitForWingStatus(child.Process.Pid, 5*time.Second)
	switch result {
	case "connected":
		fmt.Printf("wing daemon restarted (pid %d)\n", child.Process.Pid)
		fmt.Printf("  relay: connected\n")
	case "auth_failed":
		AbandonStartedDaemon(child)
		if err := cmdutil.RemoveFiles(WingPidPath(), WingArgsPath(), WingStatusPath()); err != nil {
			return errors.Join(fmt.Errorf("wing daemon restarted but auth failed — run: wt logout && wt login"), fmt.Errorf("remove failed daemon metadata: %w", err))
		}
		return fmt.Errorf("wing daemon restarted but auth failed — run: wt logout && wt login")
	default:
		fmt.Printf("wing daemon restarted (pid %d)\n", child.Process.Pid)
		fmt.Printf("  relay: connecting...\n")
	}
	if err := child.Process.Release(); err != nil {
		log.Printf("warning: failed to release restarted daemon process handle: %v", err)
	}
	return nil
}

func wingRoostFlags(args []string) (roost string, local bool) {
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--local":
			local = true
		case arg == "--roost" && index+1 < len(args):
			index++
			roost = args[index]
		case strings.HasPrefix(arg, "--roost="):
			roost = strings.TrimPrefix(arg, "--roost=")
		}
	}
	return roost, local
}

// activeWingRelayHTTPURL uses the exact coordinator recorded by a new daemon,
// then falls back to saved launch args for an already-running older daemon.
func ActiveWingRelayHTTPURL(cfg *config.Config, status *WingStatus) string {
	if status != nil && status.RoostURL != "" {
		if relayURL := wingpolicy.RelayMetadataURL(status.RoostURL); relayURL != "" {
			return relayURL
		}
	}
	if data, err := os.ReadFile(WingArgsPath()); err == nil {
		if args, parseErr := ParseSavedDaemonArgs(data, WingDaemon); parseErr == nil {
			roost, local := wingRoostFlags(args)
			return wingpolicy.ResolveWingRelayHTTPURL(cfg, roost, local)
		}
	}
	return wingpolicy.ResolveWingRelayHTTPURL(cfg, "", false)
}
