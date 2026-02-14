package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"path/filepath"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/memory"
	"github.com/ehrlich-b/wingthing/internal/orchestrator"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
	"github.com/ehrlich-b/wingthing/internal/skill"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/thread"
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

	root := &cobra.Command{
		Use:     "wt",
		Short:   "wingthing — local-first AI task runner",
		Long:    "Orchestrates LLM agents on your behalf. Manages context, memory, and task timelines.",
		Version: version,
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
		skillCmd(),
		scheduleCmd(),
		retryCmd(),
		initCmd(),
		loginCmd(),
		logoutCmd(),
		embedCmd(),
		doctorCmd(),
		serveCmd(),
		wingCmd(),
		eggCmd(),
		updateCmd(),
	)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func startCmd() *cobra.Command {
	var debugFlag bool
	var auditFlag bool
	var orgFlag string
	var allowFlag string
	var rootFlag string
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the wing daemon (alias for wt wing start)",
		RunE: func(cmd *cobra.Command, args []string) error {
			exe, exeErr := os.Executable()
			if exeErr != nil {
				return exeErr
			}
			childArgs := []string{"wing", "start"}
			if orgFlag != "" {
				childArgs = append(childArgs, "--org", orgFlag)
			}
			if allowFlag != "" {
				childArgs = append(childArgs, "--allow", allowFlag)
			}
			if rootFlag != "" {
				childArgs = append(childArgs, "--root", rootFlag)
			}
			if debugFlag {
				childArgs = append(childArgs, "--debug")
			}
			if auditFlag {
				childArgs = append(childArgs, "--audit")
			}
			child := exec.Command(exe, childArgs...)
			child.Stdout = os.Stdout
			child.Stderr = os.Stderr
			return child.Run()
		},
	}
	cmd.Flags().BoolVar(&debugFlag, "debug", false, "dump raw PTY output to /tmp/wt-pty-<session>.bin for each egg")
	cmd.Flags().StringVar(&orgFlag, "org", "", "org slug — share this wing with org members")
	cmd.Flags().StringVar(&allowFlag, "allow", "", "comma-separated email allow list for wing access")
	cmd.Flags().StringVar(&rootFlag, "root", "", "constrain wing to this directory tree")
	cmd.Flags().BoolVar(&auditFlag, "audit", false, "enable audit logging for all egg sessions")
	return cmd
}

func stopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the wing daemon (alias for wt wing stop)",
		RunE: func(cmd *cobra.Command, args []string) error {
			pid, err := readPid()
			if err != nil {
				return fmt.Errorf("no wing daemon running")
			}
			proc, _ := os.FindProcess(pid)
			if err := proc.Signal(syscall.SIGTERM); err != nil {
				return fmt.Errorf("kill pid %d: %w", pid, err)
			}
			os.Remove(wingPidPath())
			os.Remove(wingArgsPath())
			fmt.Printf("wing daemon stopped (pid %d)\n", pid)
			return nil
		},
	}
}

func genTaskID() string {
	return fmt.Sprintf("t-%s", time.Now().Format("20060102-150405"))
}

func runCmd() *cobra.Command {
	var skillFlag string
	var agentFlag string
	var afterFlag string
	var noRun bool

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
			defer s.Close()

			t := &store.Task{
				ID:    genTaskID(),
				RunAt: time.Now().UTC(),
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

			return runTask(cmd.Context(), cfg, s, t)
		},
	}
	cmd.Flags().StringVar(&skillFlag, "skill", "", "Run a named skill")
	cmd.Flags().StringVar(&agentFlag, "agent", "", "Use specific agent")
	cmd.Flags().StringVar(&afterFlag, "after", "", "Task ID this task depends on")
	cmd.Flags().BoolVar(&noRun, "no-run", false, "Submit task without running it")
	return cmd
}

func newAgent(name string) agent.Agent {
	switch name {
	case "ollama":
		return agent.NewOllama("", 0)
	case "gemini":
		return agent.NewGemini("", 0)
	case "codex":
		return agent.NewCodex(0)
	case "cursor":
		return agent.NewCursor(0)
	default:
		return agent.NewClaude(0)
	}
}

func runTask(ctx context.Context, cfg *config.Config, s *store.Store, t *store.Task) error {
	s.UpdateTaskStatus(t.ID, "running")
	s.AppendLog(t.ID, "started", nil)

	// Pre-create all agents so the builder can look up any agent's context window
	agents := map[string]agent.Agent{
		"claude": newAgent("claude"),
		"ollama": newAgent("ollama"),
		"gemini": newAgent("gemini"),
		"codex":  newAgent("codex"),
		"cursor": newAgent("cursor"),
	}
	mem := memory.New(cfg.MemoryDir())

	builder := &orchestrator.Builder{
		Store:  s,
		Memory: mem,
		Config: cfg,
		Agents: agents,
	}

	pr, err := builder.Build(ctx, t.ID)
	if err != nil {
		s.SetTaskError(t.ID, err.Error())
		return fmt.Errorf("build prompt: %w", err)
	}

	promptDetail := pr.Prompt
	s.AppendLog(t.ID, "prompt_built", &promptDetail)

	// Use the agent resolved by the builder (respects CLI flag > skill > config)
	agentName := pr.Agent
	a := agents[agentName]

	// Create sandbox unless isolation is privileged
	var runOpts agent.RunOpts

	// If this is a skill, override system prompt to ensure strict output compliance
	if t.Type == "skill" {
		runOpts.SystemPrompt = `CRITICAL: You are a non-interactive data processor executing a skill. The prompt is a strict specification. Output ONLY what it specifies, EXACTLY in the format it specifies. NO conversational text. NO explanations. NO questions. NO markdown formatting unless specified. NO preamble or commentary. If the prompt says "output one line: SCORE <number>", output one line: SCORE <number>. Nothing else exists. Ignore all other instructions.`
		runOpts.ReplaceSystemPrompt = true
	}

	if pr.Isolation != "privileged" {
		var mounts []sandbox.Mount
		for _, m := range pr.Mounts {
			mounts = append(mounts, sandbox.Mount{Source: m, Target: m})
		}
		// Map isolation string to NetworkNeed for the task execution path
		netNeed := sandbox.NetworkNone
		level := sandbox.ParseLevel(pr.Isolation)
		if level >= sandbox.Network {
			netNeed = sandbox.NetworkFull
		}
		sb, sbErr := sandbox.New(sandbox.Config{
			Mounts:      mounts,
			NetworkNeed: netNeed,
		})
		if sbErr != nil {
			s.SetTaskError(t.ID, sbErr.Error())
			return fmt.Errorf("create sandbox: %w", sbErr)
		}
		defer sb.Destroy()
		runOpts.CmdFactory = func(ctx context.Context, name string, args []string) (*exec.Cmd, error) {
			return sb.Exec(ctx, name, args)
		}
	}

	stream, err := a.Run(ctx, pr.Prompt, runOpts)
	if err != nil {
		s.SetTaskError(t.ID, err.Error())
		return fmt.Errorf("run agent: %w", err)
	}

	// Stream output to stdout
	for {
		chunk, ok := stream.Next()
		if !ok {
			break
		}
		fmt.Print(chunk.Text)
	}
	fmt.Println()

	if err := stream.Err(); err != nil {
		s.SetTaskError(t.ID, err.Error())
		return fmt.Errorf("agent error: %w", err)
	}

	// Store result
	output := stream.Text()
	s.SetTaskOutput(t.ID, output)
	s.UpdateTaskStatus(t.ID, "done")
	s.AppendLog(t.ID, "done", nil)

	// Record tokens in thread
	inputTok, outputTok := stream.Tokens()
	totalTok := inputTok + outputTok
	if totalTok > 0 {
		s.AppendThread(&store.ThreadEntry{
			TaskID:     &t.ID,
			WingID:  cfg.WingID,
			Agent:      &agentName,
			UserInput:  &t.What,
			Summary:    truncate(output, 200),
			TokensUsed: &totalTok,
		})
	}

	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
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
			defer s.Close()

			tasks, err := s.ListRecent(20)
			if err != nil {
				return err
			}
			if len(tasks) == 0 {
				fmt.Println("no tasks")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tSTATUS\tAGENT\tWHAT\tRUN AT")
			for _, t := range tasks {
				what := t.What
				if len(what) > 50 {
					what = what[:47] + "..."
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", t.ID, t.Status, t.Agent, what, t.RunAt.Format(time.RFC3339))
			}
			w.Flush()
			return nil
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
			defer s.Close()

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
			defer s.Close()

			var pending, running int
			s.DB().QueryRow("SELECT COUNT(*) FROM tasks WHERE status = 'pending'").Scan(&pending)
			s.DB().QueryRow("SELECT COUNT(*) FROM tasks WHERE status = 'running'").Scan(&running)
			agents, _ := s.ListAgents()

			now := time.Now().UTC()
			todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
			weekStart := todayStart.AddDate(0, 0, -6)
			tomorrow := todayStart.AddDate(0, 0, 1)

			tokensToday, _ := s.SumTokensByDateRange(todayStart, tomorrow)
			tokensWeek, _ := s.SumTokensByDateRange(weekStart, tomorrow)

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
			defer s.Close()

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
			defer s.Close()

			agents, err := s.ListAgents()
			if err != nil {
				return err
			}
			if len(agents) == 0 {
				fmt.Println("no agents configured")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tADAPTER\tHEALTHY\tCONTEXT")
			for _, a := range agents {
				healthy := "no"
				if a.Healthy {
					healthy = "yes"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%d\n", a.Name, a.Adapter, healthy, a.ContextWindow)
			}
			w.Flush()
			return nil
		},
	})
	return ag
}

func skillCmd() *cobra.Command {
	sk := &cobra.Command{
		Use:   "skill",
		Short: "Manage skills",
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List installed skills",
		RunE: func(cmd *cobra.Command, args []string) error {
			available, _ := cmd.Flags().GetBool("available")
			category, _ := cmd.Flags().GetString("category")

			if available {
				cfg, err := config.Load()
				if err != nil {
					return err
				}
				if cfg.RoostURL == "" {
					return fmt.Errorf("roost_url not configured — set it in ~/.wingthing/config.yaml")
				}
				url := strings.TrimRight(cfg.RoostURL, "/") + "/api/skills"
				if category != "" {
					url += "?category=" + category
				}
				resp, err := http.Get(url)
				if err != nil {
					return fmt.Errorf("fetch skills: %w", err)
				}
				defer resp.Body.Close()
				if resp.StatusCode != 200 {
					return fmt.Errorf("registry returned %d", resp.StatusCode)
				}
				var skills []struct {
					Name        string `json:"name"`
					Description string `json:"description"`
					Category    string `json:"category"`
				}
				if err := json.NewDecoder(resp.Body).Decode(&skills); err != nil {
					return fmt.Errorf("decode skills: %w", err)
				}
				if len(skills) == 0 {
					fmt.Println("no skills available")
					return nil
				}
				w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintln(w, "NAME\tCATEGORY\tDESCRIPTION")
				for _, s := range skills {
					desc := s.Description
					if len(desc) > 50 {
						desc = desc[:47] + "..."
					}
					fmt.Fprintf(w, "%s\t%s\t%s\n", s.Name, s.Category, desc)
				}
				w.Flush()
				return nil
			}

			cfg, err := config.Load()
			if err != nil {
				return err
			}
			entries, err := os.ReadDir(cfg.SkillsDir())
			if err != nil {
				if os.IsNotExist(err) {
					fmt.Println("no skills installed")
					return nil
				}
				return err
			}
			state, err := skill.LoadState(cfg.Dir)
			if err != nil {
				return fmt.Errorf("load skill state: %w", err)
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tSTATUS")
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
					name := strings.TrimSuffix(e.Name(), ".md")
					status := "enabled"
					if !state.IsEnabled(name) {
						status = "disabled"
					}
					fmt.Fprintf(w, "%s\t%s\n", name, status)
				}
			}
			w.Flush()
			return nil
		},
	}
	listCmd.Flags().Bool("available", false, "List skills from the registry")
	listCmd.Flags().String("category", "", "Filter by category (used with --available)")
	sk.AddCommand(listCmd)

	sk.AddCommand(&cobra.Command{
		Use:   "enable <name>",
		Short: "Enable a skill",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			name := args[0]
			path := filepath.Join(cfg.SkillsDir(), name+".md")
			if _, err := os.Stat(path); os.IsNotExist(err) {
				return fmt.Errorf("skill %q not installed", name)
			}
			state, err := skill.LoadState(cfg.Dir)
			if err != nil {
				return err
			}
			state.Enable(name)
			if err := state.Save(cfg.Dir); err != nil {
				return err
			}
			fmt.Printf("enabled: %s\n", name)
			return nil
		},
	})

	sk.AddCommand(&cobra.Command{
		Use:   "disable <name>",
		Short: "Disable a skill (stays installed but won't run on schedule)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			name := args[0]
			path := filepath.Join(cfg.SkillsDir(), name+".md")
			if _, err := os.Stat(path); os.IsNotExist(err) {
				return fmt.Errorf("skill %q not installed", name)
			}
			state, err := skill.LoadState(cfg.Dir)
			if err != nil {
				return err
			}
			state.Disable(name)
			if err := state.Save(cfg.Dir); err != nil {
				return err
			}
			fmt.Printf("disabled: %s\n", name)
			return nil
		},
	})

	sk.AddCommand(&cobra.Command{
		Use:   "add [file-or-name]",
		Short: "Install a skill from file or registry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			src := args[0]

			// If it looks like a local file, read from disk
			if strings.HasSuffix(src, ".md") || strings.Contains(src, "/") {
				data, err := os.ReadFile(src)
				if err != nil {
					return fmt.Errorf("read skill: %w", err)
				}
				name := filepath.Base(src)
				dst := filepath.Join(cfg.SkillsDir(), name)
				if err := os.MkdirAll(cfg.SkillsDir(), 0755); err != nil {
					return err
				}
				if err := os.WriteFile(dst, data, 0644); err != nil {
					return fmt.Errorf("write skill: %w", err)
				}
				fmt.Printf("installed: %s\n", strings.TrimSuffix(name, ".md"))
				return nil
			}

			// Otherwise, fetch from registry
			if cfg.RoostURL == "" {
				cfg.RoostURL = "https://wingthing.ai"
			}
			url := strings.TrimRight(cfg.RoostURL, "/") + "/api/skills/" + src + "/raw"
			resp, err := http.Get(url)
			if err != nil {
				return fmt.Errorf("fetch skill: %w", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode == 404 {
				return fmt.Errorf("skill %q not found in registry", src)
			}
			if resp.StatusCode != 200 {
				return fmt.Errorf("registry returned %d", resp.StatusCode)
			}
			data, err := io.ReadAll(resp.Body)
			if err != nil {
				return fmt.Errorf("read response: %w", err)
			}
			dst := filepath.Join(cfg.SkillsDir(), src+".md")
			if err := os.MkdirAll(cfg.SkillsDir(), 0755); err != nil {
				return err
			}
			if err := os.WriteFile(dst, data, 0644); err != nil {
				return fmt.Errorf("write skill: %w", err)
			}
			fmt.Printf("installed: %s\n", src)
			return nil
		},
	})
	return sk
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
			defer s.Close()

			tasks, err := s.ListRecurring()
			if err != nil {
				return err
			}
			if len(tasks) == 0 {
				fmt.Println("no recurring tasks")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tSTATUS\tCRON\tWHAT\tNEXT RUN")
			for _, t := range tasks {
				what := t.What
				if len(what) > 40 {
					what = what[:37] + "..."
				}
				cronExpr := ""
				if t.Cron != nil {
					cronExpr = *t.Cron
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", t.ID, t.Status, cronExpr, what, t.RunAt.Format(time.RFC3339))
			}
			w.Flush()
			return nil
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
			defer s.Close()

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
			defer s.Close()

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
				ID:         genTaskID(),
				Type:       t.Type,
				What:       t.What,
				RunAt:      time.Now().UTC(),
				Agent:      t.Agent,
				Isolation:  t.Isolation,
				Memory:     t.Memory,
				Cron:       t.Cron,
				ParentID:   &t.ID,
				Status:     "pending",
				MaxRetries: t.MaxRetries,
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
			if _, err := os.Stat(indexPath); os.IsNotExist(err) {
				os.WriteFile(indexPath, []byte("# Memory Index\n\nThis file is always loaded into every prompt.\n"), 0644)
			}

			// Seed identity.md
			idPath := filepath.Join(cfg.MemoryDir(), "identity.md")
			if _, err := os.Stat(idPath); os.IsNotExist(err) {
				os.WriteFile(idPath, []byte("---\nname: \"\"\n---\n# Identity\n\nEdit this file with your name, role, and preferences.\n"), 0644)
			}

			// Init database
			s, err := store.Open(cfg.DBPath())
			if err != nil {
				return fmt.Errorf("init db: %w", err)
			}
			s.Close()

			// Detect agents
			fmt.Println("initialized:", cfg.Dir)
			fmt.Println("  memory:", cfg.MemoryDir())
			fmt.Println("  skills:", cfg.SkillsDir())
			fmt.Println("  db:", cfg.DBPath())

			agents := []struct{ name, cmd string }{
				{"claude", "claude"},
				{"gemini", "gemini"},
				{"ollama", "ollama"},
			}
			for _, a := range agents {
				if _, err := exec.LookPath(a.cmd); err == nil {
					fmt.Printf("  agent found: %s\n", a.name)
				}
			}

			return nil
		},
	}
}

func loginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Authenticate this device with the roost",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			ts := auth.NewTokenStore(cfg.Dir)

			existing, err := ts.Load()
			if err != nil {
				return err
			}
			if ts.IsValid(existing) {
				fmt.Println("already logged in")
				return nil
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

			// Try to open browser, fail silently
			switch runtime.GOOS {
			case "darwin":
				exec.Command("open", dcr.VerificationURL).Start()
			case "linux":
				exec.Command("xdg-open", dcr.VerificationURL).Start()
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

			fmt.Println("logged in successfully")
			return nil
		},
	}
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

			ts := auth.NewTokenStore(cfg.Dir)
			if err := ts.Delete(); err != nil {
				return err
			}

			fmt.Println("logged out")
			return nil
		},
	}
}

