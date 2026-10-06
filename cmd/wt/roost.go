package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/daemonctl"
	"github.com/ehrlich-b/wingthing/internal/localrelay"
	"github.com/ehrlich-b/wingthing/internal/wing"
	"github.com/spf13/cobra"
)

func roostCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "roost",
		Short: "Run relay + wing in a single process (self-hosted mode)",
		Long:  "Starts the relay server and a local wing together. One command, one process, one log stream.\nUse 'wt roost start' to daemonize, or 'wt roost start --foreground' for systemd/debugging.",
	}

	cmd.AddCommand(roostStartCmd())
	cmd.AddCommand(roostStopCmd())
	cmd.AddCommand(roostStatusCmd())

	return cmd
}

func roostStartCmd() *cobra.Command {
	// Relay flags
	var addrFlag string
	var devFlag bool
	var httpsFlag bool
	var httpsAddrFlag string
	// Wing flags
	var labelsFlag string
	var pathsFlag string
	var eggConfigFlag string
	var auditFlag bool
	var debugFlag bool
	var orgFlag string
	// Shared
	var foregroundFlag bool

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start roost (relay + wing)",
		Long:  "Start a roost — relay server and local wing in one process. Daemonizes by default. Use --foreground for debugging or systemd.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := localrelay.ValidateAuthProviderEnvironment(); err != nil {
				return err
			}
			var lifecycleLock *os.File
			if !foregroundFlag {
				var err error
				lifecycleLock, err = daemonctl.AcquireDaemonLifecycleLock()
				if err != nil {
					return err
				}
				defer cmdutil.CloseWithLog("daemon lifecycle lock", lifecycleLock)
			}
			localMode := !localrelay.AuthProvidersConfigured()
			if err := localrelay.ValidateLocalHTTPSMode(httpsFlag, localMode, false); err != nil {
				return err
			}
			if localMode && !httpsFlag {
				var err error
				addrFlag, err = localrelay.PrepareLocalHTTPAddress(addrFlag, cmd.Flags().Changed("addr"))
				if err != nil {
					return err
				}
			}
			if !foregroundFlag {
				// Check before the trust ceremony so a failed duplicate start has
				// no certificate or trust-store side effects.
				if pid, kind, err := daemonctl.ReadDaemon(); err == nil {
					if kind == daemonctl.RoostDaemon {
						return fmt.Errorf("roost daemon already running (pid %d)", pid)
					}
					return fmt.Errorf("wing daemon already running (pid %d) — stop it first with: wt stop", pid)
				} else if !errors.Is(err, daemonctl.ErrNoDaemonRunning) {
					return fmt.Errorf("inspect daemon state: %w", err)
				}
			}
			var localHTTPS *localrelay.LocalHTTPSConfig
			if httpsFlag {
				cfg, err := config.Load()
				if err != nil {
					return err
				}
				localHTTPS, err = localrelay.PrepareLocalHTTPS(cmd.Context(), cfg.Dir, addrFlag, httpsAddrFlag, cmd.Flags().Changed("addr"))
				if err != nil {
					return err
				}
				addrFlag = localHTTPS.HTTPAddr
			}
			if foregroundFlag {
				return localrelay.RunRoostForeground(version, addrFlag, devFlag, labelsFlag, pathsFlag, eggConfigFlag, orgFlag, auditFlag, debugFlag, localHTTPS)
			}

			exe, err := os.Executable()
			if err != nil {
				return err
			}

			// Build child args
			var childArgs []string
			childArgs = append(childArgs, "roost", "start", "--foreground")
			if addrFlag != ":8080" {
				childArgs = append(childArgs, "--addr", addrFlag)
			}
			if devFlag {
				childArgs = append(childArgs, "--dev")
			}
			if httpsFlag {
				childArgs = append(childArgs, "--https", "--https-addr", httpsAddrFlag)
			}
			if labelsFlag != "" {
				childArgs = append(childArgs, "--labels", labelsFlag)
			}
			if pathsFlag != "" {
				childArgs = append(childArgs, "--paths", pathsFlag)
			}
			if eggConfigFlag != "" {
				childArgs = append(childArgs, "--egg-config", eggConfigFlag)
			}
			if orgFlag != "" {
				childArgs = append(childArgs, "--org", orgFlag)
			}
			if auditFlag {
				childArgs = append(childArgs, "--audit")
			}
			if debugFlag {
				childArgs = append(childArgs, "--debug")
			}

			if err := daemonctl.RotateLog(daemonctl.RoostLogPath()); err != nil {
				return err
			}
			logFile, err := os.OpenFile(daemonctl.RoostLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
			if err != nil {
				return fmt.Errorf("open log: %w", err)
			}

			home, err := os.UserHomeDir()
			if err != nil {
				cmdutil.CloseWithLog("roost log", logFile)
				return fmt.Errorf("resolve user home: %w", err)
			}

			child := exec.Command(exe, childArgs...)
			child.Dir = home
			child.Stdout = logFile
			child.Stderr = logFile
			child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			readyReader, readyWriter, err := os.Pipe()
			if err != nil {
				cmdutil.CloseWithLog("roost log", logFile)
				return fmt.Errorf("create daemon readiness pipe: %w", err)
			}
			child.ExtraFiles = []*os.File{readyWriter}
			child.Env = localrelay.ReplaceEnvironmentValue(os.Environ(), localrelay.RoostReadyFDEnv, "3")

			if err := child.Start(); err != nil {
				cmdutil.CloseWithLog("roost readiness reader", readyReader)
				cmdutil.CloseWithLog("roost readiness writer", readyWriter)
				cmdutil.CloseWithLog("roost log", logFile)
				return fmt.Errorf("start daemon: %w", err)
			}
			if err := readyWriter.Close(); err != nil {
				daemonctl.AbandonStartedDaemon(child)
				cmdutil.CloseWithLog("roost readiness reader", readyReader)
				cmdutil.CloseWithLog("roost log", logFile)
				return fmt.Errorf("close parent readiness writer: %w", err)
			}
			if err := logFile.Close(); err != nil {
				daemonctl.AbandonStartedDaemon(child)
				cmdutil.CloseWithLog("roost readiness reader", readyReader)
				return fmt.Errorf("close roost log: %w", err)
			}
			if err := localrelay.AwaitRoostReady(readyReader, localrelay.RoostDaemonReadyTimeout); err != nil {
				daemonctl.AbandonStartedDaemon(child)
				return fmt.Errorf("roost daemon did not become ready: %w (see %s)", err, daemonctl.RoostLogPath())
			}
			if err := daemonctl.WriteDaemonMetadata(daemonctl.RoostPidPath(), daemonctl.RoostArgsPath(), child.Process.Pid, childArgs); err != nil {
				daemonctl.AbandonStartedDaemon(child)
				return fmt.Errorf("start roost daemon: %w", err)
			}
			if err := child.Process.Release(); err != nil {
				log.Printf("warning: failed to release daemon process handle: %v", err)
			}
			fmt.Printf("roost daemon started (pid %d)\n", child.Process.Pid)
			fmt.Printf("  log: %s\n", daemonctl.RoostLogPath())
			fmt.Println()
			if localHTTPS != nil {
				fmt.Printf("open %s to start a terminal\n", localHTTPS.URL)
			} else {
				fmt.Printf("open %s to start a terminal\n", localrelay.LocalHTTPURL(addrFlag))
			}
			return nil
		},
	}

	// Relay flags
	cmd.Flags().StringVar(&addrFlag, "addr", config.DefaultListenAddr(), "listen address")
	cmd.Flags().BoolVar(&devFlag, "dev", false, "reload templates from disk on each request")
	cmd.Flags().BoolVar(&httpsFlag, "https", false, "serve the local browser UI over HTTPS using an on-demand, device-local CA")
	cmd.Flags().StringVar(&httpsAddrFlag, "https-addr", localrelay.DefaultHTTPSAddr(), "loopback HTTPS address for the local browser UI")
	// Wing flags
	cmd.Flags().StringVar(&labelsFlag, "labels", "", "comma-separated wing labels")
	cmd.Flags().StringVar(&pathsFlag, "paths", "", "comma-separated directories the wing can browse")
	cmd.Flags().StringVar(&eggConfigFlag, "egg-config", "", "path to egg.yaml for sandbox defaults")
	cmd.Flags().StringVar(&orgFlag, "org", "", "org name or ID")
	cmd.Flags().BoolVar(&auditFlag, "audit", false, "enable audit logging for all egg sessions")
	cmd.Flags().BoolVar(&debugFlag, "debug", false, "dump raw PTY output for each egg")
	// Shared
	cmd.Flags().BoolVar(&foregroundFlag, "foreground", false, "run in foreground instead of daemonizing")

	return cmd
}

func roostStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the roost daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			lifecycleLock, lockErr := daemonctl.AcquireDaemonLifecycleLock()
			if lockErr != nil {
				return lockErr
			}
			defer cmdutil.CloseWithLog("daemon lifecycle lock", lifecycleLock)
			pid, err := daemonctl.ReadPidFrom(daemonctl.RoostPidPath(), daemonctl.RoostDaemon)
			if err != nil {
				return fmt.Errorf("no roost daemon running")
			}
			if err := daemonctl.StopDaemonAndWait(pid, daemonctl.RoostDaemon, 5*time.Second); err != nil {
				return err
			}
			if err := cmdutil.RemoveFiles(daemonctl.RoostPidPath(), daemonctl.RoostArgsPath()); err != nil {
				return fmt.Errorf("remove roost daemon metadata: %w", err)
			}
			fmt.Printf("roost daemon stopped (pid %d)\n", pid)
			return nil
		},
	}
}

func roostStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Check roost daemon status",
		RunE: func(cmd *cobra.Command, args []string) error {
			pid, err := daemonctl.ReadPidFrom(daemonctl.RoostPidPath(), daemonctl.RoostDaemon)
			if err != nil {
				if daemonctl.DaemonAbsentError(err) {
					fmt.Println("roost daemon is not running")
					return nil
				}
				return fmt.Errorf("inspect roost daemon state: %w", err)
			}
			fmt.Printf("roost daemon is running (pid %d)\n", pid)
			fmt.Printf("  log: %s\n", daemonctl.RoostLogPath())

			cfg, _ := config.Load()
			if cfg != nil {
				sessions := wing.ListAliveEggSessions(cfg)
				if len(sessions) > 0 {
					fmt.Println("  egg sessions:")
					for _, s := range sessions {
						fmt.Printf("    %s  %s  %s\n", s.SessionID, s.Agent, s.CWD)
					}
				} else {
					fmt.Println("  egg sessions: none")
				}
			}
			return nil
		},
	}
}
