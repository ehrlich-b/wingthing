package ui

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/behrlich/wingthing/internal/agent"
	"github.com/behrlich/wingthing/internal/interfaces"
	"github.com/behrlich/wingthing/internal/llm"
	"github.com/behrlich/wingthing/internal/tools"
)

// SimpleUI provides a readline-style interface without complex TUI rendering
type SimpleUI struct {
	renderer     *Renderer
	orchestrator *agent.Orchestrator
	events       chan agent.Event
	thinking     bool
}

// NewSimpleUI creates a new simple UI instance
func NewSimpleUI() *SimpleUI {
	events := make(chan agent.Event, 100)
	theme := DefaultTheme()
	renderer := NewRenderer(theme)

	// Set up components (similar to Model)
	fs := interfaces.NewOSFileSystem()
	toolRunner := tools.NewMultiRunner()
	toolRunner.RegisterRunner("cli", tools.NewCLIRunner())
	toolRunner.RegisterRunner("edit", tools.NewEditRunner())

	memoryManager := agent.NewMemory(fs)
	permissionChecker := agent.NewPermissionEngine(fs)
	llmProvider := llm.NewDummyProvider(500 * time.Millisecond)

	orchestrator := agent.NewOrchestrator(
		toolRunner,
		events,
		memoryManager,
		permissionChecker,
		llmProvider,
	)

	return &SimpleUI{
		renderer:     renderer,
		orchestrator: orchestrator,
		events:       events,
		thinking:     false,
	}
}

// Run starts the simple UI loop
func (ui *SimpleUI) Run(ctx context.Context) error {
	// Print welcome message
	fmt.Print(ui.renderer.Welcome())

	// Start event listener in background
	go ui.handleEvents()

	// Simple readline loop
	scanner := bufio.NewScanner(os.Stdin)
	for {
		// Print prompt
		if ui.thinking {
			fmt.Print("🤔 Thinking... (please wait)\n> ")
		} else {
			fmt.Print("> ")
		}

		// Read user input
		if !scanner.Scan() {
			break // EOF or error
		}

		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}

		// Handle special commands
		if input == "quit" || input == "exit" {
			break
		}

		// Skip if thinking
		if ui.thinking {
			fmt.Println("Please wait for the current response to complete.")
			continue
		}

		// Print user message
		fmt.Print(ui.renderer.User(input))

		// Set thinking state and process
		ui.thinking = true
		go func() {
			ui.orchestrator.ProcessPrompt(ctx, input)
		}()
	}

	return scanner.Err()
}

// handleEvents processes agent events in the background
func (ui *SimpleUI) handleEvents() {
	for event := range ui.events {
		switch event.Type {
		case string(agent.EventTypePlan):
			// Skip internal orchestration events
			continue
		case string(agent.EventTypeRunTool):
			fmt.Print(ui.renderer.AgentRun(event.Content))
		case string(agent.EventTypeObservation):
			fmt.Print(ui.renderer.AgentObservation(event.Content))
		case string(agent.EventTypeFinal):
			fmt.Print(ui.renderer.AgentFinal(event.Content))
			ui.thinking = false
		case string(agent.EventTypeError):
			fmt.Print(ui.renderer.AgentError(event.Content))
			ui.thinking = false
		case string(agent.EventTypePermissionRequest):
			// For now, just print the permission request
			fmt.Printf("Permission requested: %s\n", event.Content)
			// TODO: Implement proper permission handling
		}
	}
}