package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/behrlich/wingthing/internal/agent"
	"github.com/behrlich/wingthing/internal/config"
	"github.com/behrlich/wingthing/internal/interfaces"
	"github.com/behrlich/wingthing/internal/llm"
	"github.com/behrlich/wingthing/internal/tools"
	"github.com/behrlich/wingthing/internal/ui"
)

var (
	prompt   string
	jsonMode bool
	maxTurns int
	resume   bool
	autoYes  bool
)

func main() {
	var rootCmd = &cobra.Command{
		Use:   "wingthing",
		Short: "A Claude Code competitor built with Bubble Tea",
		Long:  "An interactive terminal application for AI-assisted development",
		RunE:  run,
	}

	rootCmd.Flags().StringVarP(&prompt, "prompt", "p", "", "One-shot prompt (headless mode)")
	rootCmd.Flags().BoolVar(&jsonMode, "json", false, "Stream structured JSON events")
	rootCmd.Flags().IntVar(&maxTurns, "max-turns", 0, "Cap agent loops")
	rootCmd.Flags().BoolVar(&resume, "resume", false, "Load last session from local history")
	rootCmd.Flags().BoolVarP(&autoYes, "yes", "y", false, "Auto-accept permission requests in headless mode")

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	// Headless mode with prompt flag
	if prompt != "" {
		return runHeadless(ctx, prompt)
	}

	// Interactive Bubble Tea UI
	return runInteractive(ctx)
}

func runHeadless(ctx context.Context, prompt string) error {
	// Create filesystem
	fs := interfaces.NewOSFileSystem()

	// Create tool runner with registered tools
	toolRunner := tools.NewMultiRunner()
	toolRunner.RegisterRunner("cli", tools.NewCLIRunner())

	// Register file operations from EditRunner
	editRunner := tools.NewEditRunner()
	toolRunner.RegisterRunner("read_file", editRunner)
	toolRunner.RegisterRunner("write_file", editRunner)
	toolRunner.RegisterRunner("edit_file", editRunner)

	// Load configuration
	configManager := config.NewManager(fs)
	userConfigDir, err := config.GetUserConfigDir()
	if err != nil {
		return fmt.Errorf("failed to get user config dir: %w", err)
	}

	projectDir, err := config.GetProjectDir()
	if err != nil {
		return fmt.Errorf("failed to get project dir: %w", err)
	}

	if err := configManager.Load(userConfigDir, projectDir); err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	cfg := configManager.Get()

	// Create logger that discards output for headless mode
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))

	// Create other components
	memoryManager := agent.NewMemory(fs)
	permissionChecker := agent.NewPermissionEngine(fs, logger)

	// Create LLM provider - use dummy if no API key configured
	useDummy := cfg.APIKey == ""
	llmProvider := llm.NewProvider(cfg, useDummy)

	// Load memory from CLAUDE.md files
	if err := memoryManager.LoadUserMemory(userConfigDir); err != nil {
		// Silently ignore memory loading errors in headless mode
	}

	if err := memoryManager.LoadProjectMemory(projectDir); err != nil {
		// Silently ignore memory loading errors in headless mode
	}

	// Create events channel for capturing orchestrator output
	events := make(chan agent.Event, 100)

	// Create orchestrator
	orchestrator := agent.NewOrchestrator(
		toolRunner,
		events,
		memoryManager,
		permissionChecker,
		llmProvider,
		logger,
	)

	// Configure orchestrator for headless mode
	orchestrator.SetHeadlessMode(autoYes)

	// Create renderer for formatting output (only used for terminal mode)
	var renderer *ui.Renderer
	if !jsonMode {
		theme := ui.DefaultTheme()
		renderer = ui.NewRenderer(theme)
	}

	// Start processing in a goroutine
	done := make(chan error, 1)
	go func() {
		done <- orchestrator.ProcessPrompt(ctx, prompt)
	}()

	// Listen for events and handle the conversation loop
	for {
		select {
		case event := <-events:
			if err := handleHeadlessEvent(event, renderer); err != nil {
				return err
			}

			// Check if this is a final event (conversation complete)
			if event.Type == string(agent.EventTypeFinal) {
				return nil
			}

			// Permission requests are now handled internally by the orchestrator in headless mode
			// No special handling needed here

		case err := <-done:
			if err != nil {
				if jsonMode {
					fmt.Printf(`{"type":"error","content":"Error: %s"}%s`, err.Error(), "\n")
				} else {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				}
				return err
			}
			return nil

		case <-ctx.Done():
			if jsonMode {
				fmt.Printf(`{"type":"error","content":"Context cancelled"}%s`, "\n")
			} else {
				fmt.Fprintf(os.Stderr, "Context cancelled\n")
			}
			return ctx.Err()
		}
	}
}

func runInteractive(ctx context.Context) error {
	model := ui.NewModel()

	// Handle --resume flag
	if resume {
		model = model.WithResumeFlag()
	}

	p := tea.NewProgram(
		model,
		tea.WithInput(os.Stdin),
	)

	_, err := p.Run()
	return err
}

// handleHeadlessEvent processes agent events for headless mode output
func handleHeadlessEvent(event agent.Event, renderer *ui.Renderer) error {
	if jsonMode {
		// JSON output mode - format as structured JSON
		eventJSON := map[string]interface{}{
			"type":    event.Type,
			"content": event.Content,
		}

		// Add data field if present
		if event.Data != nil {
			eventJSON["data"] = event.Data
		}

		jsonBytes, err := json.Marshal(eventJSON)
		if err != nil {
			return fmt.Errorf("failed to marshal event to JSON: %w", err)
		}

		fmt.Println(string(jsonBytes))
	} else {
		// Terminal output mode - format like interactive mode
		switch event.Type {
		case string(agent.EventTypePlan):
			// Skip plan messages in terminal mode (they're internal)
			return nil
		case string(agent.EventTypeRunTool):
			fmt.Print(renderer.AgentRun(event.Content))
		case string(agent.EventTypeObservation):
			fmt.Print(renderer.AgentObservation(event.Content))
		case string(agent.EventTypeFinal):
			fmt.Print(renderer.AgentFinal(event.Content))
		case string(agent.EventTypePermissionRequest):
			if permReq, ok := event.Data.(agent.PermissionRequest); ok {
				var msg string
				if autoYes {
					msg = fmt.Sprintf("Permission required for tool '%s' - auto-accepting with --yes flag", permReq.Tool)
				} else {
					msg = fmt.Sprintf("Permission required for tool '%s' - auto-denying in headless mode", permReq.Tool)
				}
				fmt.Print(renderer.AgentFinal(msg))
			}
		case string(agent.EventTypeError):
			fmt.Print(renderer.AgentError(event.Content))
		}
	}

	return nil
}
