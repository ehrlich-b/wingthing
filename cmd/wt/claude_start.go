package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/daemonctl"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/procinfo"
)

const claudeWingStartupTimeout = 10 * time.Second

func missingClaudeWing(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED)
}

// ensureClaudeWing returns the authenticated connection used for admission.
// Only the implicit local owner may start a missing runtime. A handshake or
// policy refusal is authoritative, and a live unready daemon is never restarted.
func ensureClaudeWing(ctx context.Context, dir, clientName string) (*controlsocket.Client, error) {
	startup, cancel := context.WithTimeout(ctx, claudeWingStartupTimeout)
	defer cancel()
	client, err := controlsocket.Dial(startup, dir, controlsocket.Hello{Client: clientName})
	if startup.Err() != nil {
		if client != nil {
			_ = client.Close()
		}
		return nil, startup.Err()
	}
	if err == nil || clientName != "" || !missingClaudeWing(err) {
		return client, err
	}
	// Load validates the selected channel and state before lifecycle mutations;
	// never fall back to a different directory on configuration failure.
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if cfg.Dir != dir {
		return nil, errors.New("selected wing state changed during Claude startup")
	}
	wc, err := config.LoadWingConfig(dir)
	if err != nil {
		return nil, err
	}
	// The runtime historically falls back from an unreadable default egg.yaml.
	// Implicit startup must not silently replace an existing isolation policy.
	policyPath := wc.EggConfig
	if policyPath == "" {
		policyPath = filepath.Join(dir, "egg.yaml")
		if _, err := os.Lstat(policyPath); errors.Is(err, os.ErrNotExist) {
			policyPath = ""
		} else if err != nil {
			return nil, err
		}
	} else if !filepath.IsAbs(policyPath) {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		policyPath = filepath.Join(home, policyPath)
	}
	if policyPath != "" {
		if _, err := egg.ResolveEggConfig(policyPath); err != nil {
			return nil, fmt.Errorf("load wing isolation policy: %w", err)
		}
	}
	lock, err := daemonctl.AcquireDaemonLifecycleLockContext(startup)
	if err != nil {
		return nil, err
	}
	// Another caller may have completed startup while we waited for the lock.
	client, err = controlsocket.Dial(startup, dir, controlsocket.Hello{})
	if startup.Err() != nil {
		cmdutil.CloseWithLog("daemon lifecycle lock", lock)
		if client != nil {
			_ = client.Close()
		}
		return nil, startup.Err()
	}
	if err == nil || !missingClaudeWing(err) {
		cmdutil.CloseWithLog("daemon lifecycle lock", lock)
		return client, err
	}
	pid, _, err := daemonctl.ReadDaemon()
	var exited <-chan struct{}
	if errors.Is(err, daemonctl.ErrNoDaemonRunning) {
		if err := startup.Err(); err != nil {
			cmdutil.CloseWithLog("daemon lifecycle lock", lock)
			return nil, err
		}
		child, startErr := startBackgroundWing([]string{"wing", "start", "--foreground", "--local-only"})
		if startErr != nil {
			cmdutil.CloseWithLog("daemon lifecycle lock", lock)
			return nil, startErr
		}
		pid = child.Process.Pid
		done := make(chan struct{})
		exited = done
		// Reap an early exit before checking ownership. Release alone leaves a
		// zombie while this launching CLI lives, which looks alive to kill(0).
		// Waiting does not bind the child's lifetime to caller cancellation.
		go func() { _ = child.Wait(); close(done) }()
	} else if err != nil {
		cmdutil.CloseWithLog("daemon lifecycle lock", lock)
		return nil, fmt.Errorf("inspect daemon state: %w", err)
	}
	cmdutil.CloseWithLog("daemon lifecycle lock", lock)
	// Release the lock once metadata is durable. All callers now wait for the
	// same daemon, and cancelling one wait must not terminate another's runtime.
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	exitError := func() error {
		// Revalidate under the lifecycle lock before removing exited metadata.
		if lock, lockErr := daemonctl.AcquireDaemonLifecycleLockContext(startup); lockErr == nil {
			if _, _, readErr := daemonctl.ReadDaemon(); errors.Is(readErr, daemonctl.ErrNoDaemonRunning) {
				if err := cmdutil.RemoveFiles(daemonctl.WingArgsPath(), daemonctl.WingStatusPath()); err != nil {
					log.Printf("clean exited wing metadata: %v", err)
				}
			}
			cmdutil.CloseWithLog("daemon lifecycle lock", lock)
		}
		return fmt.Errorf("wing daemon exited before readiness; inspect %s", daemonctl.WingLogPath())
	}
	for {
		client, err = controlsocket.Dial(startup, dir, controlsocket.Hello{})
		if err == nil {
			return client, nil
		}
		if startup.Err() != nil {
			return nil, fmt.Errorf("local wing readiness: %w; inspect %s", startup.Err(), daemonctl.WingLogPath())
		}
		if !missingClaudeWing(err) {
			return nil, err
		}
		if !procinfo.OwnedProcessIsAlive(pid) {
			return nil, exitError()
		}
		select {
		case <-exited:
			return nil, exitError()
		case <-startup.Done():
			return nil, fmt.Errorf("local wing readiness: %w; inspect %s", startup.Err(), daemonctl.WingLogPath())
		case <-ticker.C:
		}
	}
}

// stop joins the timer goroutine before attach can write provider output.
func claudeStartupProgress(out io.Writer) (stop func()) {
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		timer := time.NewTimer(250 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-done:
			return
		case <-timer.C:
			_, _ = fmt.Fprint(out, "\rStarting local Claude...")
		}
		<-done
		_, _ = fmt.Fprint(out, "\r\x1b[2K")
	}()
	return func() { close(done); <-stopped }
}
