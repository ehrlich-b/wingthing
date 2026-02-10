package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/memory"
	"github.com/ehrlich-b/wingthing/internal/orchestrator"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
	"github.com/ehrlich-b/wingthing/internal/skill"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/ws"
	"github.com/spf13/cobra"
)

func wingCmd() *cobra.Command {
	var relayFlag string
	var labelsFlag string

	cmd := &cobra.Command{
		Use:   "wing",
		Short: "Connect to relay and accept remote tasks",
		Long:  "Start a wing — your machine becomes reachable from anywhere via the relay. Same as sitting at the keyboard, just remote.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			// Resolve relay URL
			relayURL := relayFlag
			if relayURL == "" {
				relayURL = cfg.RelayURL
			}
			if relayURL == "" {
				relayURL = "https://ws.wingthing.ai"
			}
			// Convert HTTP URL to WebSocket URL
			wsURL := strings.Replace(relayURL, "https://", "wss://", 1)
			wsURL = strings.Replace(wsURL, "http://", "ws://", 1)
			wsURL = strings.TrimRight(wsURL, "/") + "/ws/wing"

			// Load auth token
			ts := auth.NewTokenStore(cfg.Dir)
			tok, err := ts.Load()
			if err != nil || !ts.IsValid(tok) {
				return fmt.Errorf("not logged in — run: wt login")
			}

			// Open local store for task execution
			s, err := store.Open(cfg.DBPath())
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer s.Close()

			// Detect available agents
			var agents []string
			for _, name := range []string{"claude", "ollama", "gemini"} {
				if _, err := exec.LookPath(name); err == nil {
					agents = append(agents, name)
				}
			}

			// List installed skills
			var skills []string
			entries, _ := os.ReadDir(cfg.SkillsDir())
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
					skills = append(skills, strings.TrimSuffix(e.Name(), ".md"))
				}
			}

			// Parse labels
			var labels []string
			if labelsFlag != "" {
				labels = strings.Split(labelsFlag, ",")
			}

			fmt.Printf("connecting to %s\n", wsURL)
			fmt.Printf("  agents: %v\n", agents)
			fmt.Printf("  skills: %v\n", skills)
			if len(labels) > 0 {
				fmt.Printf("  labels: %v\n", labels)
			}

			client := &ws.Client{
				RelayURL:  wsURL,
				Token:     tok.Token,
				MachineID: cfg.MachineID,
				Agents:    agents,
				Skills:    skills,
				Labels:    labels,
			}

			client.OnTask = func(ctx context.Context, task ws.TaskSubmit, send ws.ChunkSender) (string, error) {
				return executeRelayTask(ctx, cfg, s, task, send)
			}

			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()

			return client.Run(ctx)
		},
	}

	cmd.Flags().StringVar(&relayFlag, "relay", "", "relay server URL (default: ws.wingthing.ai)")
	cmd.Flags().StringVar(&labelsFlag, "labels", "", "comma-separated wing labels (e.g. gpu,cuda,research)")

	return cmd
}

// executeRelayTask runs a task received from the relay using the local agent + sandbox.
func executeRelayTask(ctx context.Context, cfg *config.Config, s *store.Store, task ws.TaskSubmit, send ws.ChunkSender) (string, error) {
	fmt.Printf("executing task %s", task.TaskID)
	if task.Skill != "" {
		fmt.Printf(" (skill: %s)", task.Skill)
	}
	fmt.Println()

	// Create a local task record
	t := &store.Task{
		ID:    task.TaskID,
		RunAt: timeNow(),
	}

	if task.Skill != "" {
		t.What = task.Skill
		t.Type = "skill"
		// Check skill exists and is enabled
		state, stErr := skill.LoadState(cfg.Dir)
		if stErr == nil && !state.IsEnabled(task.Skill) {
			return "", fmt.Errorf("skill %q is disabled", task.Skill)
		}
	} else {
		t.What = task.Prompt
		t.Type = "prompt"
	}
	if task.Agent != "" {
		t.Agent = task.Agent
	}

	s.CreateTask(t)
	s.UpdateTaskStatus(t.ID, "running")

	agents := map[string]agent.Agent{
		"claude": newAgent("claude"),
		"ollama": newAgent("ollama"),
		"gemini": newAgent("gemini"),
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
		return "", fmt.Errorf("build prompt: %w", err)
	}

	agentName := pr.Agent
	a := agents[agentName]

	var runOpts agent.RunOpts
	if t.Type == "skill" {
		runOpts.SystemPrompt = `CRITICAL: You are a non-interactive data processor executing a skill. The prompt is a strict specification. Output ONLY what it specifies, EXACTLY in the format it specifies. NO conversational text. NO explanations. NO questions. NO markdown formatting unless specified. NO preamble or commentary.`
		runOpts.ReplaceSystemPrompt = true
	}

	isolation := task.Isolation
	if isolation == "" {
		isolation = pr.Isolation
	}
	if isolation != "privileged" {
		var mounts []sandbox.Mount
		for _, m := range pr.Mounts {
			mounts = append(mounts, sandbox.Mount{Source: m, Target: m})
		}
		sb, sbErr := sandbox.New(sandbox.Config{
			Isolation: sandbox.ParseLevel(isolation),
			Mounts:    mounts,
			Timeout:   pr.Timeout,
		})
		if sbErr != nil {
			s.SetTaskError(t.ID, sbErr.Error())
			return "", fmt.Errorf("create sandbox: %w", sbErr)
		}
		defer sb.Destroy()
		runOpts.CmdFactory = func(ctx context.Context, name string, args []string) (*exec.Cmd, error) {
			return sb.Exec(ctx, name, args)
		}
	}

	stream, err := a.Run(ctx, pr.Prompt, runOpts)
	if err != nil {
		s.SetTaskError(t.ID, err.Error())
		return "", fmt.Errorf("run agent: %w", err)
	}

	// Stream output back to relay
	for {
		chunk, ok := stream.Next()
		if !ok {
			break
		}
		fmt.Print(chunk.Text) // local echo
		send(task.TaskID, chunk.Text)
	}
	fmt.Println()

	if err := stream.Err(); err != nil {
		s.SetTaskError(t.ID, err.Error())
		return "", fmt.Errorf("agent error: %w", err)
	}

	output := stream.Text()
	s.SetTaskOutput(t.ID, output)
	s.UpdateTaskStatus(t.ID, "done")

	// Record in thread
	inputTok, outputTok := stream.Tokens()
	totalTok := inputTok + outputTok
	if totalTok > 0 {
		s.AppendThread(&store.ThreadEntry{
			TaskID:     &t.ID,
			MachineID:  cfg.MachineID,
			Agent:      &agentName,
			UserInput:  &t.What,
			Summary:    truncate(output, 200),
			TokensUsed: &totalTok,
		})
	}

	fmt.Printf("task %s done (%d tokens)\n", task.TaskID, totalTok)
	return output, nil
}

func timeNow() time.Time {
	return time.Now().UTC()
}
