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
	"path/filepath"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/daemon"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/transport"
	"github.com/spf13/cobra"
)

func main() {
	var skillFlag string
	var agentFlag string
	var afterFlag string

	root := &cobra.Command{
		Use:   "wt [prompt]",
		Short: "wingthing — local-first AI task runner",
		Long:  "Orchestrates LLM agents on your behalf. Manages context, memory, and task timelines.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && skillFlag == "" {
				return cmd.Help()
			}
			c := clientFromConfig()
			req := transport.SubmitTaskRequest{}
			if skillFlag != "" {
				req.What = skillFlag
				req.Type = "skill"
			} else {
				req.What = args[0]
				req.Type = "prompt"
			}
			if agentFlag != "" {
				req.Agent = agentFlag
			}
			if afterFlag != "" {
				deps, _ := json.Marshal([]string{afterFlag})
				req.DependsOn = string(deps)
			}
			t, err := c.SubmitTask(req)
			if err != nil {
				return fmt.Errorf("submit task: %w", err)
			}
			fmt.Printf("submitted: %s (%s)\n", t.ID, t.What)
			return nil
		},
	}
	root.Flags().StringVar(&skillFlag, "skill", "", "Run a named skill")
	root.Flags().StringVar(&agentFlag, "agent", "", "Use specific agent")
	root.Flags().StringVar(&afterFlag, "after", "", "Task ID this task depends on")

	root.AddCommand(
		timelineCmd(),
		threadCmd(),
		statusCmd(),
		logCmd(),
		agentCmd(),
		skillCmd(),
		scheduleCmd(),
		retryCmd(),
		daemonCmd(),
		initCmd(),
		loginCmd(),
		logoutCmd(),
	)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func clientFromConfig() *transport.Client {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading config: %v\n", err)
		os.Exit(1)
	}
	return transport.NewClient(cfg.SocketPath())
}

func timelineCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "timeline",
		Short: "Show upcoming and recent tasks",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := clientFromConfig()
			tasks, err := c.ListTasks("", 20)
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
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", t.ID, t.Status, t.Agent, what, t.RunAt)
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
			c := clientFromConfig()
			date := ""
			if yesterday {
				date = time.Now().AddDate(0, 0, -1).Format("2006-01-02")
			}
			thread, err := c.GetThread(date, 0)
			if err != nil {
				return err
			}
			if thread == "" {
				fmt.Println("(empty thread)")
				return nil
			}
			fmt.Print(thread)
			return nil
		},
	}
	cmd.Flags().Bool("yesterday", false, "Show yesterday's thread")
	return cmd
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Daemon status and agent health",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := clientFromConfig()
			s, err := c.Status()
			if err != nil {
				return fmt.Errorf("daemon not reachable: %w", err)
			}
			fmt.Printf("pending: %d\nrunning: %d\nagents:  %d\ntokens:  %d today / %d this week\n", s.Pending, s.Running, s.Agents, s.TokensToday, s.TokensWeek)
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
			c := clientFromConfig()

			taskID := ""
			if len(args) > 0 {
				taskID = args[0]
			} else if last {
				tasks, err := c.ListTasks("", 1)
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

			entries, err := c.GetLog(taskID)
			if err != nil {
				return err
			}
			for _, raw := range entries {
				var entry struct {
					Event  string  `json:"event"`
					Detail *string `json:"detail"`
					Time   string  `json:"timestamp"`
				}
				json.Unmarshal(raw, &entry)
				if showContext && entry.Event == "prompt_built" && entry.Detail != nil {
					fmt.Println(*entry.Detail)
					return nil
				}
				detail := ""
				if entry.Detail != nil {
					detail = *entry.Detail
					if len(detail) > 80 {
						detail = detail[:77] + "..."
					}
				}
				fmt.Printf("%s  %s  %s\n", entry.Time, entry.Event, detail)
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
			c := clientFromConfig()
			agents, err := c.ListAgents()
			if err != nil {
				return err
			}
			if len(agents) == 0 {
				fmt.Println("no agents configured")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tADAPTER\tHEALTHY\tCONTEXT")
			for _, raw := range agents {
				var a struct {
					Name          string `json:"name"`
					Adapter       string `json:"adapter"`
					Healthy       bool   `json:"healthy"`
					ContextWindow int    `json:"context_window"`
				}
				json.Unmarshal(raw, &a)
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
				if cfg.RelayURL == "" {
					return fmt.Errorf("relay_url not configured — set it in ~/.wingthing/config.yaml")
				}
				url := strings.TrimRight(cfg.RelayURL, "/") + "/skills"
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
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
					name := strings.TrimSuffix(e.Name(), ".md")
					fmt.Println(name)
				}
			}
			return nil
		},
	}
	listCmd.Flags().Bool("available", false, "List skills from the registry")
	listCmd.Flags().String("category", "", "Filter by category (used with --available)")
	sk.AddCommand(listCmd)

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
			if cfg.RelayURL == "" {
				return fmt.Errorf("relay_url not configured — set it in ~/.wingthing/config.yaml")
			}
			url := strings.TrimRight(cfg.RelayURL, "/") + "/skills/" + src + "/raw"
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
			c := clientFromConfig()
			tasks, err := c.ListSchedule()
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
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", t.ID, t.Status, cronExpr, what, t.RunAt)
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
			c := clientFromConfig()
			t, err := c.RemoveSchedule(args[0])
			if err != nil {
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
			c := clientFromConfig()
			t, err := c.RetryTask(args[0])
			if err != nil {
				return err
			}
			fmt.Printf("retried: %s\n", t.ID)
			return nil
		},
	}
}

func daemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Start the wingthing daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			install, _ := cmd.Flags().GetBool("install")
			if install {
				return installService()
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			return daemon.Run(cfg)
		},
	}
	cmd.Flags().Bool("install", false, "Install as system service")
	return cmd
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

func installService() error {
	fmt.Println("not implemented: service installation")
	fmt.Println("run 'wt daemon' in a terminal or add to your shell startup")
	return nil
}

func loginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Authenticate this device with the relay server",
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

			if cfg.RelayURL == "" {
				return fmt.Errorf("relay_url not configured in config.yaml")
			}

			dcr, err := auth.RequestDeviceCode(cfg.RelayURL, cfg.MachineID)
			if err != nil {
				return err
			}

			fmt.Printf("Visit %s and enter code: %s\n", dcr.VerificationURL, dcr.UserCode)

			// Try to open browser, fail silently
			switch runtime.GOOS {
			case "darwin":
				exec.Command("open", dcr.VerificationURL).Start()
			case "linux":
				exec.Command("xdg-open", dcr.VerificationURL).Start()
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			defer stop()

			tr, err := auth.PollForToken(ctx, cfg.RelayURL, dcr.DeviceCode, dcr.Interval)
			if err != nil {
				return err
			}

			token := &auth.DeviceToken{
				Token:     tr.Token,
				ExpiresAt: tr.ExpiresAt,
				IssuedAt:  time.Now().Unix(),
				DeviceID:  cfg.MachineID,
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
