package main

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/daemonctl"
	"github.com/ehrlich-b/wingthing/internal/dashboard"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/localmcp"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
	"github.com/ehrlich-b/wingthing/internal/skill"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/taskrun"
	"github.com/ehrlich-b/wingthing/internal/thread"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
	"github.com/spf13/cobra"
)

var version = "dev"

func main() {
	// Fast path: re-exec'd as sandbox deny-path wrapper (Linux mount namespace).
	// Must run before cobra to avoid any overhead — this process execs immediately.
	if len(os.Args) > 1 && os.Args[1] == "_deny_init" {
		sandbox.DenyInit(os.Args[2:])
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := executeCLI(ctx, os.Args[1:], remotepkg.ProcessIO()); err != nil {
		var exitErr *cmdutil.CommandExitError
		if errors.As(err, &exitErr) {
			if exitErr.Message != "" {
				_, _ = fmt.Fprintln(os.Stderr, exitErr.Message)
			}
			os.Exit(exitErr.Code)
		}
		_, _ = fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func newRootCommand() *cobra.Command {
	var remoteTarget string
	var remoteBinary string
	var remoteCWD string
	var remoteState string
	root := &cobra.Command{
		Use:           config.BinaryName(),
		Short:         "wingthing — an agent manager for agents",
		Long:          "An agent manager for agents: one typed control plane for durable agent runs and terminals across your machines, with human inspection and takeover when useful.",
		Version:       version,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("remote") || cmd.Flags().Changed("remote-binary") || cmd.Flags().Changed("remote-state") || cmd.Flags().Changed("remote-cwd") {
				return errors.New("remote routing was not initialized")
			}
			streams := remotepkg.Streams(cmd.Context())
			if len(args) == 0 && streams.StdinTTY && streams.StdoutTTY {
				return dashboard.Run(cmd.Context(), version, streams)
			}
			return cmd.Help()
		},
	}
	root.PersistentFlags().StringVarP(&remoteTarget, "remote", "r", "", "run a supported command on this SSH host or alias")
	root.PersistentFlags().StringVar(&remoteBinary, "remote-binary", config.BinaryName(), "channel-matching executable or absolute path on the remote host")
	root.PersistentFlags().StringVar(&remoteState, "remote-state", "", "absolute state directory on the remote host (sets WINGTHING_DIR and WINGTHING_PREVIEW_DIR there)")
	root.PersistentFlags().StringVar(&remoteCWD, "remote-cwd", "", "remote working directory for bare interactive entry")
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if cmd.Flags().Changed("remote") || cmd.Flags().Changed("remote-binary") || cmd.Flags().Changed("remote-state") || cmd.Flags().Changed("remote-cwd") {
			return errors.New("remote routing was not initialized")
		}
		return nil
	}

	root.AddCommand(
		runCmd(),
		startCmd(),
		stopCmd(),
		timelineCmd(),
		threadCmd(),
		statusCmd(),
		logCmd(),
		agentCmd(),
		scheduleCmd(),
		retryCmd(),
		promptCmd(),
		initCmd(),
		loginCmd(),
		logoutCmd(),
		whoamiCmd(),
		phoneCmd(),
		supportCmd(),
		embedCmd(),
		doctorCmd(),
		serveCmd(),
		wingCmd(),
		roostCmd(),
		eggCmd(),
		terminalCmd(),
		attachCmd(),
		sessionCmd(),
		conversationCmd(),
		wingsCmd(),
		keygenCmd(),
		updateCmd(),
		channelCmd(),
		toolCallCmd(),
		toolListCmd(),
		mcpCmd(),
		localCertCmd(),
		remoteCmd(),
		remoteEnterCmd(),
	)
	if config.Channel() == "preview" {
		root.AddCommand(previewProviderCmd())
	}
	return root
}

func startCmd() *cobra.Command {
	var debugFlag bool
	var auditFlag bool
	var orgFlag string
	var roostFlag string
	var allowFlags []string
	var pathsFlag string
	var localFlag bool
	var rawReplayFlag bool
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the daemon (alias for wt wing start / wt daemon start)",
		RunE: func(cmd *cobra.Command, args []string) error {
			exe, exeErr := os.Executable()
			if exeErr != nil {
				return exeErr
			}
			childArgs := []string{"wing", "start"}
			if roostFlag != "" {
				childArgs = append(childArgs, "--roost", roostFlag)
			}
			if orgFlag != "" {
				childArgs = append(childArgs, "--org", orgFlag)
			}
			for _, ak := range allowFlags {
				childArgs = append(childArgs, "--allow", ak)
			}
			if pathsFlag != "" {
				childArgs = append(childArgs, "--paths", pathsFlag)
			}
			if debugFlag {
				childArgs = append(childArgs, "--debug")
			}
			if auditFlag {
				childArgs = append(childArgs, "--audit")
			}
			if localFlag {
				childArgs = append(childArgs, "--local")
			}
			if rawReplayFlag {
				childArgs = append(childArgs, "--raw-replay")
			}
			child := exec.Command(exe, childArgs...)
			child.Stdout = os.Stdout
			child.Stderr = os.Stderr
			return child.Run()
		},
	}
	cmd.Flags().StringVar(&roostFlag, "roost", "", "roost server URL (default: config or wingthing.ai)")
	cmd.Flags().BoolVar(&debugFlag, "debug", false, "dump raw PTY output to /tmp/wt-pty-<session>.bin for each egg")
	cmd.Flags().StringVar(&orgFlag, "org", "", "org name or ID — share this wing with org members")
	cmd.Flags().StringSliceVar(&allowFlags, "allow", nil, "ephemeral passkey public key(s) for this session")
	cmd.Flags().StringVar(&pathsFlag, "paths", "", "comma-separated directories the wing can browse (default: ~/)")
	cmd.Flags().BoolVar(&auditFlag, "audit", false, "enable audit logging for all egg sessions")
	cmd.Flags().BoolVar(&localFlag, "local", false, "connect to localhost:8080 (for self-hosted wt serve)")
	cmd.Flags().BoolVar(&rawReplayFlag, "raw-replay", false, "use raw replay buffer for reconnect instead of VTerm snapshot")
	return cmd
}

func stopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the daemon (alias for wt wing stop / wt daemon stop)",
		RunE: func(cmd *cobra.Command, args []string) error {
			lifecycleLock, lockErr := daemonctl.AcquireDaemonLifecycleLock()
			if lockErr != nil {
				return lockErr
			}
			defer cmdutil.CloseWithLog("daemon lifecycle lock", lifecycleLock)
			pid, kind, err := daemonctl.ReadDaemon()
			if err != nil {
				return fmt.Errorf("no wing daemon running")
			}
			if err := daemonctl.StopDaemonAndWait(pid, kind, 5*time.Second); err != nil {
				return err
			}
			// Clean up both wing and roost pid/args files
			if err := cmdutil.RemoveFiles(daemonctl.WingPidPath(), daemonctl.WingArgsPath(), daemonctl.RoostPidPath(), daemonctl.RoostArgsPath()); err != nil {
				return fmt.Errorf("remove daemon metadata: %w", err)
			}
			fmt.Printf("wing daemon stopped (pid %d)\n", pid)
			return nil
		},
	}
}

func runCmd() *cobra.Command {
	var skillFlag string
	var agentFlag string
	var afterFlag string
	var noRun bool
	var unsandboxed bool
	var configFlag string

	cmd := &cobra.Command{
		Use:   "run [prompt]",
		Short: "Run a prompt or skill",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && skillFlag == "" {
				return fmt.Errorf("provide a prompt or --skill flag")
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			s, err := store.Open(cfg.DBPath())
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer cmdutil.CloseWithLog("store", s)

			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}
			eggConfigYAML, err := taskrun.ResolveRunEggConfigYAML(configFlag, cwd, unsandboxed)
			if err != nil {
				return err
			}
			t := &store.Task{
				ID:            cmdutil.GenTaskID(),
				RunAt:         time.Now().UTC(),
				CWD:           cwd,
				EggConfigYAML: eggConfigYAML,
			}
			if unsandboxed {
				t.Isolation = "privileged"
			}
			if skillFlag != "" {
				t.What = skillFlag
				t.Type = "skill"
				state, stErr := skill.LoadState(cfg.Dir)
				if stErr == nil && !state.IsEnabled(skillFlag) {
					return fmt.Errorf("skill %q is disabled — run: wt skill enable %s", skillFlag, skillFlag)
				}
				sk, skErr := skill.Load(filepath.Join(cfg.SkillsDir(), skillFlag+".md"))
				if skErr == nil && sk.Schedule != "" {
					t.Cron = &sk.Schedule
				}
			} else {
				t.What = args[0]
				t.Type = "prompt"
			}
			if agentFlag != "" {
				t.Agent = agentFlag
			}
			if afterFlag != "" {
				deps, _ := json.Marshal([]string{afterFlag})
				d := string(deps)
				t.DependsOn = &d
			}
			if err := s.CreateTask(t); err != nil {
				return fmt.Errorf("create task: %w", err)
			}
			fmt.Printf("submitted: %s (%s)\n", t.ID, t.What)

			if noRun {
				return nil
			}

			return taskrun.RunTask(cmd.Context(), cfg, s, t)
		},
	}
	cmd.Flags().StringVar(&skillFlag, "skill", "", "Run a named skill")
	cmd.Flags().StringVar(&agentFlag, "agent", "", "Use specific agent")
	cmd.Flags().StringVar(&afterFlag, "after", "", "Task ID this task depends on")
	cmd.Flags().StringVar(&configFlag, "config", "", "Path to egg config")
	cmd.Flags().BoolVar(&noRun, "no-run", false, "Submit task without running it")
	cmd.Flags().BoolVar(&unsandboxed, "unsandboxed", false, "trust the host boundary; run with the full authority of the local OS user")
	cmd.MarkFlagsMutuallyExclusive("config", "unsandboxed")
	return cmd
}

func timelineCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "timeline",
		Short: "Show upcoming and recent tasks",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			s, err := store.Open(cfg.DBPath())
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer cmdutil.CloseWithLog("store", s)

			tasks, err := s.ListRecent(20)
			if err != nil {
				return err
			}
			if len(tasks) == 0 {
				fmt.Println("no tasks")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			if _, err := fmt.Fprintln(w, "ID\tSTATUS\tAGENT\tWHAT\tRUN AT"); err != nil {
				return err
			}
			for _, t := range tasks {
				what := t.What
				if len(what) > 50 {
					what = what[:47] + "..."
				}
				if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", t.ID, t.Status, t.Agent, what, t.RunAt.Format(time.RFC3339)); err != nil {
					return err
				}
			}
			return w.Flush()
		},
	}
}

func threadCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "thread",
		Short: "Print today's daily thread",
		RunE: func(cmd *cobra.Command, args []string) error {
			yesterday, _ := cmd.Flags().GetBool("yesterday")
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			s, err := store.Open(cfg.DBPath())
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer cmdutil.CloseWithLog("store", s)

			date := time.Now().UTC()
			if yesterday {
				date = date.AddDate(0, 0, -1)
			}
			rendered, err := thread.RenderDay(s, date, 0)
			if err != nil {
				return err
			}
			if rendered == "" {
				fmt.Println("(empty thread)")
				return nil
			}
			fmt.Print(rendered)
			return nil
		},
	}
	cmd.Flags().Bool("yesterday", false, "Show yesterday's thread")
	return cmd
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Task counts and token usage",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			s, err := store.Open(cfg.DBPath())
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer cmdutil.CloseWithLog("store", s)

			var pending, running int
			if err := s.DB().QueryRow("SELECT COUNT(*) FROM tasks WHERE status = 'pending'").Scan(&pending); err != nil {
				return fmt.Errorf("count pending tasks: %w", err)
			}
			if err := s.DB().QueryRow("SELECT COUNT(*) FROM tasks WHERE status = 'running'").Scan(&running); err != nil {
				return fmt.Errorf("count running tasks: %w", err)
			}
			agents, err := s.ListAgents()
			if err != nil {
				return fmt.Errorf("list agents: %w", err)
			}

			now := time.Now().UTC()
			todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
			weekStart := todayStart.AddDate(0, 0, -6)
			tomorrow := todayStart.AddDate(0, 0, 1)

			tokensToday, err := s.SumTokensByDateRange(todayStart, tomorrow)
			if err != nil {
				return fmt.Errorf("sum today's tokens: %w", err)
			}
			tokensWeek, err := s.SumTokensByDateRange(weekStart, tomorrow)
			if err != nil {
				return fmt.Errorf("sum weekly tokens: %w", err)
			}

			fmt.Printf("pending: %d\nrunning: %d\nagents:  %d\ntokens:  %d today / %d this week\n", pending, running, len(agents), tokensToday, tokensWeek)
			return nil
		},
	}
}

func logCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "log [taskId]",
		Short: "Show task log events",
		RunE: func(cmd *cobra.Command, args []string) error {
			last, _ := cmd.Flags().GetBool("last")
			showContext, _ := cmd.Flags().GetBool("context")

			cfg, err := config.Load()
			if err != nil {
				return err
			}
			s, err := store.Open(cfg.DBPath())
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer cmdutil.CloseWithLog("store", s)

			taskID := ""
			if len(args) > 0 {
				taskID = args[0]
			} else if last {
				tasks, err := s.ListRecent(1)
				if err != nil {
					return err
				}
				if len(tasks) == 0 {
					fmt.Println("no tasks")
					return nil
				}
				taskID = tasks[0].ID
			} else {
				return fmt.Errorf("provide a task ID or use --last")
			}

			entries, err := s.ListLogByTask(taskID)
			if err != nil {
				return err
			}
			for _, e := range entries {
				if showContext && e.Event == "prompt_built" && e.Detail != nil {
					fmt.Println(*e.Detail)
					return nil
				}
				detail := ""
				if e.Detail != nil {
					detail = *e.Detail
					if len(detail) > 80 {
						detail = detail[:77] + "..."
					}
				}
				fmt.Printf("%s  %s  %s\n", e.Timestamp.Format(time.RFC3339), e.Event, detail)
			}
			return nil
		},
	}
	cmd.Flags().Bool("last", false, "Show most recent task")
	cmd.Flags().Bool("context", false, "Show full prompt for prompt_built event")
	return cmd
}

func agentCmd() *cobra.Command {
	ag := &cobra.Command{
		Use:   "agent",
		Short: "Manage agent adapters",
	}
	ag.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List configured agents",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			s, err := store.Open(cfg.DBPath())
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer cmdutil.CloseWithLog("store", s)

			agents, err := s.ListAgents()
			if err != nil {
				return err
			}
			if len(agents) == 0 {
				fmt.Println("no agents configured")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			if _, err := fmt.Fprintln(w, "NAME\tADAPTER\tHEALTHY\tCONTEXT"); err != nil {
				return err
			}
			for _, a := range agents {
				healthy := "no"
				if a.Healthy {
					healthy = "yes"
				}
				if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%d\n", a.Name, a.Adapter, healthy, a.ContextWindow); err != nil {
					return err
				}
			}
			return w.Flush()
		},
	})
	var timeoutSeconds float64
	var clientName string
	waitAny := &cobra.Command{
		Use:   "wait-any RUN_ID...",
		Short: "Wait for any owned agent run to finish and print JSON",
		Args:  cobra.RangeArgs(1, 64),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			clientID := strings.TrimSpace(clientName)
			if clientID == "" {
				clientID = strings.TrimSpace(os.Getenv("WT_MCP_CLIENT"))
			}
			explicitClient := clientID != ""
			if clientID == "" {
				clientID = "default"
			}
			if err := eggclient.ValidateSessionName(clientID); err != nil {
				return fmt.Errorf("invalid MCP client name: %w", err)
			}
			clients, err := localmcp.LoadLocalMCPClientsConfig(cfg)
			if err != nil {
				return err
			}
			if clients.RequireClient && !explicitClient {
				return errors.New("clients.yaml requires an explicit MCP client; pass --client or WT_MCP_CLIENT")
			}
			client, configured := clients.Clients[clientID]
			if (clients.RequireClient || len(clients.Clients) > 0) && !configured {
				return fmt.Errorf("MCP client %q is not configured in clients.yaml", clientID)
			}
			owner := clientID
			if configured && strings.TrimSpace(client.Owner) != "" {
				owner = strings.TrimSpace(client.Owner)
				if err := eggclient.ValidateSessionName(owner); err != nil {
					return fmt.Errorf("invalid MCP owner name: %w", err)
				}
			}
			server := &localmcp.Server{Version: version, Cfg: cfg, Principal: owner, Actor: clientID, Logs: cmd.ErrOrStderr()}
			if configured {
				server.Grants = localmcp.GrantSet(client.Grants)
			}
			arguments, err := json.Marshal(map[string]any{"run_ids": args, "timeout_seconds": timeoutSeconds})
			if err != nil {
				return err
			}
			result, err := server.AgentWaitAny(cmd.Context(), arguments)
			if err != nil {
				return err
			}
			if err := json.NewEncoder(cmd.OutOrStdout()).Encode(result); err != nil {
				return err
			}
			finished, _ := result["finished"].([]map[string]any)
			pending, _ := result["pending"].([]string)
			if len(finished) == 0 && len(pending) > 0 {
				return cmdutil.ExitError(2, "timed out waiting for an agent run to finish")
			}
			return nil
		},
	}
	waitAny.Flags().Float64Var(&timeoutSeconds, "timeout", 30, "wait timeout in seconds (0.1-600)")
	waitAny.Flags().StringVar(&clientName, "client", "", "local MCP client name (or WT_MCP_CLIENT)")
	ag.AddCommand(waitAny)
	return ag
}

func scheduleCmd() *cobra.Command {
	sc := &cobra.Command{
		Use:   "schedule",
		Short: "Manage recurring tasks",
	}
	sc.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List recurring tasks",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			s, err := store.Open(cfg.DBPath())
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer cmdutil.CloseWithLog("store", s)

			tasks, err := s.ListRecurring()
			if err != nil {
				return err
			}
			if len(tasks) == 0 {
				fmt.Println("no recurring tasks")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			if _, err := fmt.Fprintln(w, "ID\tSTATUS\tCRON\tWHAT\tNEXT RUN"); err != nil {
				return err
			}
			for _, t := range tasks {
				what := t.What
				if len(what) > 40 {
					what = what[:37] + "..."
				}
				cronExpr := ""
				if t.Cron != nil {
					cronExpr = *t.Cron
				}
				if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", t.ID, t.Status, cronExpr, what, t.RunAt.Format(time.RFC3339)); err != nil {
					return err
				}
			}
			return w.Flush()
		},
	})
	sc.AddCommand(&cobra.Command{
		Use:   "remove [id]",
		Short: "Remove cron schedule from a task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			s, err := store.Open(cfg.DBPath())
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer cmdutil.CloseWithLog("store", s)

			t, err := s.GetTask(args[0])
			if err != nil {
				return err
			}
			if t == nil {
				return fmt.Errorf("task not found: %s", args[0])
			}
			if err := s.ClearTaskCron(args[0]); err != nil {
				return err
			}
			fmt.Printf("removed schedule from %s (%s)\n", t.ID, t.What)
			return nil
		},
	})
	return sc
}

func retryCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "retry [task-id]",
		Short: "Retry a failed task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			s, err := store.Open(cfg.DBPath())
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer cmdutil.CloseWithLog("store", s)

			t, err := s.GetTask(args[0])
			if err != nil {
				return err
			}
			if t == nil {
				return fmt.Errorf("task not found: %s", args[0])
			}
			if t.Status != "failed" {
				return fmt.Errorf("only failed tasks can be retried (status: %s)", t.Status)
			}

			newTask := &store.Task{
				ID:             cmdutil.GenTaskID(),
				Type:           t.Type,
				What:           t.What,
				RunAt:          time.Now().UTC(),
				Agent:          t.Agent,
				Model:          t.Model,
				TimeoutSeconds: t.TimeoutSeconds,
				Isolation:      t.Isolation,
				Memory:         t.Memory,
				Cron:           t.Cron,
				ParentID:       &t.ID,
				Status:         "pending",
				MaxRetries:     t.MaxRetries,
				CWD:            t.CWD,
				PromptName:     t.PromptName,
				PromptRevision: t.PromptRevision,
				Principal:      t.Principal,
				EggConfigYAML:  t.EggConfigYAML,
			}
			if err := s.CreateTask(newTask); err != nil {
				return err
			}
			fmt.Printf("retried: %s\n", newTask.ID)
			return nil
		},
	}
}

func initCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Initialize ~/.wingthing directory",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			dirs := []string{cfg.Dir, cfg.MemoryDir(), cfg.SkillsDir()}
			for _, d := range dirs {
				if err := os.MkdirAll(d, 0755); err != nil {
					return fmt.Errorf("create %s: %w", d, err)
				}
			}

			// Seed index.md
			indexPath := filepath.Join(cfg.MemoryDir(), "index.md")
			if _, err := os.Stat(indexPath); errors.Is(err, os.ErrNotExist) {
				if err := os.WriteFile(indexPath, []byte("# Memory Index\n\nThis file is always loaded into every prompt.\n"), 0644); err != nil {
					return fmt.Errorf("seed memory index: %w", err)
				}
			} else if err != nil {
				return fmt.Errorf("inspect memory index: %w", err)
			}

			// Seed identity.md
			idPath := filepath.Join(cfg.MemoryDir(), "identity.md")
			if _, err := os.Stat(idPath); errors.Is(err, os.ErrNotExist) {
				if err := os.WriteFile(idPath, []byte("---\nname: \"\"\n---\n# Identity\n\nEdit this file with your name, role, and preferences.\n"), 0644); err != nil {
					return fmt.Errorf("seed identity: %w", err)
				}
			} else if err != nil {
				return fmt.Errorf("inspect identity: %w", err)
			}

			// Init database
			s, err := store.Open(cfg.DBPath())
			if err != nil {
				return fmt.Errorf("init db: %w", err)
			}
			if err := s.Close(); err != nil {
				return fmt.Errorf("close initialized database: %w", err)
			}

			// Detect agents
			fmt.Println("initialized:", cfg.Dir)
			fmt.Println("  memory:", cfg.MemoryDir())
			fmt.Println("  skills:", cfg.SkillsDir())
			fmt.Println("  db:", cfg.DBPath())

			for _, definition := range agent.Definitions() {
				if _, err := exec.LookPath(definition.Command); err == nil {
					fmt.Printf("  agent found: %s\n", definition.Name)
				}
			}

			return nil
		},
	}
}

func loginCmd() *cobra.Command {
	var roostFlag string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate this device with the roost",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			if roostFlag != "" {
				cfg.RoostURL = roostFlag
			}

			ts := auth.NewTokenStore(cfg.Dir)

			existing, err := ts.Load()
			if err != nil {
				return err
			}
			reusable, err := reusableLoginForTarget(ts, existing, roostFlag, auth.ValidateTokenRemote)
			if err != nil {
				return err
			}
			if reusable {
				fmt.Println("already logged in")
				return nil
			}
			if ts.IsValid(existing) && roostFlag != "" {
				fmt.Println("existing login is not accepted by the requested roost; starting a new device login")
			}

			if cfg.RoostURL == "" {
				cfg.RoostURL = "https://wingthing.ai"
			}

			// Generate or load X25519 keypair for E2E encryption
			pubKeyB64, err := auth.EnsureKeyPair(cfg.Dir)
			if err != nil {
				return fmt.Errorf("keypair: %w", err)
			}

			dcr, err := auth.RequestDeviceCode(cfg.RoostURL, cfg.WingID, pubKeyB64)
			if err != nil {
				return err
			}

			fmt.Printf("Visit: %s\n", dcr.VerificationURL)

			// Opening the browser is a convenience; the printed URL remains usable.
			switch runtime.GOOS {
			case "darwin":
				if err := exec.Command("open", dcr.VerificationURL).Start(); err != nil {
					log.Printf("open login URL: %v", err)
				}
			case "linux":
				if err := exec.Command("xdg-open", dcr.VerificationURL).Start(); err != nil {
					log.Printf("open login URL: %v", err)
				}
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			defer stop()

			tr, err := auth.PollForToken(ctx, cfg.RoostURL, dcr.DeviceCode, dcr.Interval)
			if err != nil {
				return err
			}

			token := &auth.DeviceToken{
				Token:     tr.Token,
				ExpiresAt: tr.ExpiresAt,
				IssuedAt:  time.Now().Unix(),
				DeviceID:  cfg.WingID,
				PublicKey: pubKeyB64,
			}
			if err := ts.Save(token); err != nil {
				return err
			}

			if tr.DisplayName != "" || tr.Email != "" {
				info := &auth.UserInfo{DisplayName: tr.DisplayName, Email: tr.Email, Provider: tr.Provider}
				fmt.Printf("logged in as %s\n", formatUserIdentity(info))
			} else {
				fmt.Println("logged in successfully")
			}

			// If a daemon was running, restart it so it picks up the new token
			if err := daemonctl.RestartWingDaemonIfRunning(); err != nil {
				fmt.Printf("warning: failed to restart wing daemon: %v\n", err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&roostFlag, "roost", "", "roost URL (default: config or wingthing.ai)")
	return cmd
}

func reusableLoginForTarget(store *auth.TokenStore, existing *auth.DeviceToken, explicitTarget string, validate func(string, string) error) (bool, error) {
	if !store.IsValid(existing) {
		return false, nil
	}
	if explicitTarget == "" {
		return true, nil
	}
	target := strings.TrimRight(explicitTarget, "/")
	if err := validate(target, existing.Token); err != nil {
		if errors.Is(err, auth.ErrAuthFailed) {
			return false, nil
		}
		return false, fmt.Errorf("verify existing login with requested roost: %w", err)
	}
	return true, nil
}

func logoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Remove device authentication",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			lifecycleLock, lockErr := daemonctl.AcquireDaemonLifecycleLock()
			if lockErr != nil {
				return lockErr
			}
			defer cmdutil.CloseWithLog("daemon lifecycle lock", lifecycleLock)

			// Stop wing daemon if running (prevents orphaned daemon with revoked token)
			if pid, kind, pidErr := daemonctl.ReadDaemon(); pidErr == nil {
				fmt.Printf("stopping wing daemon (pid %d)...\n", pid)
				if stopErr := daemonctl.StopDaemonAndWait(pid, kind, 5*time.Second); stopErr != nil {
					return fmt.Errorf("refusing to delete login while daemon is still running: %w", stopErr)
				}
				if err := cmdutil.RemoveFiles(daemonctl.WingPidPath(), daemonctl.WingArgsPath(), daemonctl.RoostPidPath(), daemonctl.RoostArgsPath()); err != nil {
					return fmt.Errorf("remove stopped daemon metadata: %w", err)
				}
			}

			ts := auth.NewTokenStore(cfg.Dir)
			if err := ts.Delete(); err != nil {
				return err
			}

			fmt.Println("logged out")
			return nil
		},
	}
}

// formatUserIdentity formats a user identity string from auth.UserInfo.
func formatUserIdentity(info *auth.UserInfo) string {
	identity := info.DisplayName
	if info.Email != "" {
		if identity != "" {
			identity += " (" + info.Email + ")"
		} else {
			identity = info.Email
		}
	}
	if info.Provider != "" {
		identity += " via " + info.Provider
	}
	if identity == "" {
		identity = info.UserID
	}
	return identity
}

func whoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the currently logged-in user",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			ts := auth.NewTokenStore(cfg.Dir)
			tok, err := ts.Load()
			if err != nil {
				return err
			}
			if !ts.IsValid(tok) {
				return fmt.Errorf("not logged in — run: wt login")
			}

			relayURL := wingpolicy.ResolveRelayHTTPURL(cfg)
			info, err := auth.FetchUserInfo(relayURL, tok.Token)
			if err != nil {
				if errors.Is(err, auth.ErrAuthFailed) {
					return fmt.Errorf("token expired — run: wt logout && wt login")
				}
				return fmt.Errorf("relay: %w", err)
			}

			fmt.Println(formatUserIdentity(info))
			fmt.Printf("  wing_id: %s\n", cfg.WingID)
			return nil
		},
	}
}

func supportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "support",
		Short: "Collect diagnostic bundle for troubleshooting",
		RunE: func(cmd *cobra.Command, args []string) (runErr error) {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			ts := time.Now().Format("20060102-150405")
			zipPath := filepath.Join(os.TempDir(), fmt.Sprintf("wt-support-%s.zip", ts))
			f, err := os.Create(zipPath)
			if err != nil {
				return fmt.Errorf("create zip: %w", err)
			}
			defer func() {
				if err := f.Close(); err != nil {
					runErr = errors.Join(runErr, fmt.Errorf("close support bundle: %w", err))
				}
			}()
			zw := zip.NewWriter(f)
			defer func() {
				if err := zw.Close(); err != nil {
					runErr = errors.Join(runErr, fmt.Errorf("finalize support bundle: %w", err))
				}
			}()

			// meta.json
			hostname, _ := os.Hostname()
			meta := map[string]any{
				"wing_id":   cfg.WingID,
				"hostname":  hostname,
				"platform":  runtime.GOOS,
				"version":   version,
				"timestamp": time.Now().UTC().Format(time.RFC3339),
			}
			if pid, pidErr := daemonctl.ReadPid(); pidErr == nil {
				meta["daemon_pid"] = pid
			}
			tok, _ := auth.NewTokenStore(cfg.Dir).Load()
			if tok != nil {
				meta["token_expires_at"] = tok.ExpiresAt
				meta["token_device_id"] = tok.DeviceID
			}
			var currentWingStatus *daemonctl.WingStatus
			if status, statusErr := daemonctl.ReadWingStatus(); statusErr == nil {
				currentWingStatus = status
				meta["wing_status"] = status.State
				if roostURL := wingpolicy.RelayMetadataURL(status.RoostURL); roostURL != "" {
					meta["wing_status_roost"] = roostURL
				}
				if status.Error != "" {
					meta["wing_status_error"] = status.Error
				}
			}
			// Try whoami
			if tok != nil {
				relayURL := daemonctl.ActiveWingRelayHTTPURL(cfg, currentWingStatus)
				if info, infoErr := auth.FetchUserInfo(relayURL, tok.Token); infoErr == nil {
					meta["account"] = formatUserIdentity(info)
				} else {
					meta["account_error"] = infoErr.Error()
				}
			}
			metaJSON, err := json.MarshalIndent(meta, "", "  ")
			if err != nil {
				return fmt.Errorf("encode support metadata: %w", err)
			}
			if err := addZipFile(zw, "meta.json", metaJSON); err != nil {
				return err
			}

			// wing.log (last 10000 lines)
			if err := addZipTail(zw, "wing.log", daemonctl.WingLogPath(), 10000); err != nil {
				return err
			}

			// egg.log (last 1000 lines)
			if err := addZipTail(zw, "egg.log", filepath.Join(cfg.Dir, "egg.log"), 1000); err != nil {
				return err
			}

			// Session logs (preserved from ~/.wingthing/logs/)
			logsDir := filepath.Join(cfg.Dir, "logs")
			if logEntries, logErr := os.ReadDir(logsDir); logErr == nil {
				for _, e := range logEntries {
					if err := addZipTail(zw, "logs/"+e.Name(), filepath.Join(logsDir, e.Name()), 500); err != nil {
						return err
					}
				}
			}

			// wing.yaml (redact secrets)
			if err := addZipRedacted(zw, "wing.yaml", filepath.Join(cfg.Dir, "wing.yaml"),
				[]string{"jwt_key:", "allow_keys:", "- public_key:"}); err != nil {
				return err
			}

			// wing.status
			if err := addZipCopy(zw, "wing.status", daemonctl.WingStatusPath()); err != nil {
				return err
			}

			// doctor output
			if doctorOut, doctorErr := exec.Command(os.Args[0], "doctor").CombinedOutput(); doctorErr == nil {
				if err := addZipFile(zw, "doctor.txt", doctorOut); err != nil {
					return err
				}
			}

			fmt.Printf("diagnostic bundle: %s\n", zipPath)
			return nil
		},
	}
}

func addZipFile(zw *zip.Writer, name string, data []byte) error {
	w, err := zw.Create(name)
	if err != nil {
		return fmt.Errorf("create support bundle entry %s: %w", name, err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("write support bundle entry %s: %w", name, err)
	}
	return nil
}

func addZipRedacted(zw *zip.Writer, name, srcPath string, redactPrefixes []string) error {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read support source %s: %w", srcPath, err)
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		redacted := false
		for _, prefix := range redactPrefixes {
			if strings.HasPrefix(trimmed, prefix) {
				out = append(out, strings.SplitN(line, ":", 2)[0]+": <redacted>")
				redacted = true
				break
			}
		}
		if !redacted {
			out = append(out, line)
		}
	}
	return addZipFile(zw, name, []byte(strings.Join(out, "\n")))
}

func addZipCopy(zw *zip.Writer, name, srcPath string) error {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read support source %s: %w", srcPath, err)
	}
	return addZipFile(zw, name, data)
}

func addZipTail(zw *zip.Writer, name, srcPath string, maxLines int) error {
	f, err := os.Open(srcPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("open support source %s: %w", srcPath, err)
	}
	defer cmdutil.CloseWithLog("support source", f)

	var lines []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if len(lines) > maxLines {
			lines = lines[1:]
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan support source %s: %w", srcPath, err)
	}

	w, err := zw.Create(name)
	if err != nil {
		return fmt.Errorf("create support bundle entry %s: %w", name, err)
	}
	for _, line := range lines {
		if _, err := io.WriteString(w, line+"\n"); err != nil {
			return fmt.Errorf("write support bundle entry %s: %w", name, err)
		}
	}
	return nil
}
