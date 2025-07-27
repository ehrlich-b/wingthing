package ui

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/behrlich/wingthing/internal/agent"
	"github.com/behrlich/wingthing/internal/config"
	"github.com/behrlich/wingthing/internal/interfaces"
	"github.com/behrlich/wingthing/internal/llm"
	"github.com/behrlich/wingthing/internal/tools"
)


type sessionState int

const (
	sessionReady sessionState = iota
	sessionThinking
	sessionWaitingPermission
)

type Model struct {
	state      sessionState
	width      int
	height     int
	input      InputModel
	modal      ModalModel
	theme      Theme
	renderer   *Renderer
	
	// Agent communication
	events      chan agent.Event
	orchestrator *agent.Orchestrator
	
	// Slash commands
	commandLoader *agent.CommandLoader
	
	// Command completion
	showingCompletions      bool
	completionOptions       []string
	selectedCompletionIndex int
	
	// Permission handling
	currentPermissionRequest *agent.PermissionRequest
	selectedPermissionOption int
	
	
	// Debug logging
	logger *slog.Logger
}

func NewModel() Model {
	events := make(chan agent.Event, 100)
	
	// Set up debug logging
	debugFile, err := os.OpenFile("/tmp/wingthing-debug.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		panic(err)
	}
	logger := slog.New(slog.NewTextHandler(debugFile, &slog.HandlerOptions{Level: slog.LevelDebug}))
	logger.Info("Wingthing debug session started")
	
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
	
	// Create other components
	memoryManager := agent.NewMemory(fs)
	permissionChecker := agent.NewPermissionEngine(fs)
	llmProvider := llm.NewDummyProvider(500 * time.Millisecond)
	
	// Create orchestrator
	orchestrator := agent.NewOrchestrator(
		toolRunner,
		events,
		memoryManager,
		permissionChecker,
		llmProvider,
	)
	
	// Create command loader and load commands
	commandLoader := agent.NewCommandLoader()
	
	// Get config directories
	userConfigDir, err := config.GetUserConfigDir()
	if err != nil {
		logger.Warn("Failed to get user config directory", "error", err)
		userConfigDir = ""
	}
	
	projectDir, err := config.GetProjectDir()
	if err != nil {
		logger.Warn("Failed to get project directory", "error", err)
		projectDir = ""
	}
	
	// Load commands from both directories
	if err := commandLoader.LoadCommands(userConfigDir, projectDir); err != nil {
		logger.Warn("Failed to load commands", "error", err)
	}
	
	theme := DefaultTheme()
	return Model{
		state:        sessionReady,
		input:        NewInputModel(),
		modal:        NewModalModel(),
		theme:        theme,
		renderer:     NewRenderer(theme),
		events:       events,
		orchestrator: orchestrator,
		commandLoader: commandLoader,
		logger:       logger,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.input.Init(),
		m.modal.Init(),
		m.listenForEvents(),
		printToScrollback(m.renderer.Welcome()),
		tea.EnableBracketedPaste,
	)
}

func (m Model) listenForEvents() tea.Cmd {
	return func() tea.Msg {
		return <-m.events
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return m.handleWindowSize(msg), nil
		
	case tea.KeyMsg:
		return m.handleKeyEvent(msg, cmds)
		
	case agent.Event:
		return m.handleAgentEvent(msg, cmds)
	}

	return m, tea.Batch(cmds...)
}

func (m Model) View() string {
	if m.modal.IsOpen() {
		return lipgloss.Place(
			m.width, m.height,
			lipgloss.Center, lipgloss.Center,
			m.modal.View(),
			lipgloss.WithWhitespaceChars(""),
		)
	}

	// Simple footer view: input field with thinking indicator above if needed
	var output strings.Builder
	
	// Add thinking indicator above input if thinking
	if m.state == sessionThinking {
		thinkingStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("11")). // Yellow
			Bold(true)
		output.WriteString(thinkingStyle.Render("🤔 Assistant is thinking..."))
		output.WriteString("\n")
	} else if m.state == sessionWaitingPermission && m.currentPermissionRequest != nil {
		// Show permission request overlay
		command := "unknown command"
		if cmd, exists := m.currentPermissionRequest.Parameters["command"]; exists {
			command = fmt.Sprintf("%v", cmd)
		}
		
		headerStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("11")). // Yellow
			Bold(true)
		
		output.WriteString(headerStyle.Render("⚠️  Permission Required"))
		output.WriteString("\n")
		output.WriteString(fmt.Sprintf("Tool: %s", m.currentPermissionRequest.Tool))
		output.WriteString("\n")
		output.WriteString(fmt.Sprintf("Command: %s", command))
		output.WriteString("\n\n")
		
		// Show options with selection
		options := []string{"Allow Once", "No"}
		keys := []string{"A", "N"}
		
		for i, option := range options {
			if i == m.selectedPermissionOption {
				selectedStyle := lipgloss.NewStyle().
					Foreground(lipgloss.Color("0")).  // Black text
					Background(lipgloss.Color("11")). // Yellow background
					Bold(true)
				output.WriteString(selectedStyle.Render(fmt.Sprintf("> [%s] %s", keys[i], option)))
			} else {
				output.WriteString(fmt.Sprintf("  [%s] %s", keys[i], option))
			}
			output.WriteString("\n")
		}
		output.WriteString("\nUse ↑/↓ to navigate, Enter to select, or press the letter key")
		output.WriteString("\n")
	}
	
	// Add the input field
	output.WriteString(m.input.View())
	
	// Add completion dropdown if showing
	if m.showingCompletions && len(m.completionOptions) > 0 {
		output.WriteString("\n")
		
		// Style for completion dropdown
		borderStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("8")). // Gray
			Bold(false)
		
		selectedStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("0")).  // Black text
			Background(lipgloss.Color("12")). // Blue background
			Bold(true)
		
		normalStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("15")). // White text
			Bold(false)
		
		// Show completion header
		output.WriteString(borderStyle.Render("Commands:"))
		output.WriteString("\n")
		
		// Show up to 5 completion options
		maxOptions := 5
		if len(m.completionOptions) < maxOptions {
			maxOptions = len(m.completionOptions)
		}
		
		for i := 0; i < maxOptions; i++ {
			command := m.completionOptions[i]
			if i == m.selectedCompletionIndex {
				output.WriteString(selectedStyle.Render(fmt.Sprintf("> /%s", command)))
			} else {
				output.WriteString(normalStyle.Render(fmt.Sprintf("  /%s", command)))
			}
			output.WriteString("\n")
		}
		
		// Show navigation hint
		if len(m.completionOptions) > 0 {
			hintStyle := lipgloss.NewStyle().
				Foreground(lipgloss.Color("8")). // Gray
				Italic(true)
			output.WriteString(hintStyle.Render("↑/↓ to navigate, Tab/Enter to select, Esc to cancel"))
		}
	}
	
	return output.String()
}

// handlePermissionResponse processes a permission response and updates the transcript
func (m Model) handlePermissionResponse(choice, decision string, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	if m.currentPermissionRequest == nil {
		m.logger.Error("No current permission request to respond to")
		return m, tea.Batch(cmds...)
	}

	// Print the user's choice to the transcript
	responseOutput := m.renderer.PermissionResponse(choice, decision)
	cmds = append(cmds, printToScrollback(responseOutput))

	// Send the permission response to the orchestrator
	tool := m.currentPermissionRequest.Tool
	action := "execute" // Standard action for tool execution
	params := m.currentPermissionRequest.Parameters

	switch decision {
	case "allow_once":
		m.orchestrator.GrantPermission(tool, action, params, agent.AllowOnce)
		m.logger.Debug("Granted permission (allow once)", "tool", tool)
		// Retry the pending tool execution
		go func() {
			ctx := context.Background()
			if err := m.orchestrator.RetryPendingTool(ctx); err != nil {
				m.logger.Error("Failed to retry pending tool", "error", err)
			}
		}()
	case "deny":
		m.orchestrator.DenyPermission(tool, action, params, agent.Deny)
		m.logger.Debug("Denied permission (deny once)", "tool", tool)
	default:
		m.logger.Error("Unknown permission decision", "decision", decision)
	}
	
	// Reset permission state
	m.currentPermissionRequest = nil
	m.selectedPermissionOption = 0
	m.state = sessionThinking
	m.input.SetThinking(true)
	
	m.logger.Debug("Permission response handled, returning to thinking state")
	
	return m, tea.Batch(cmds...)
}

// handleSlashCommand processes slash commands
func (m Model) handleSlashCommand(input string, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	// Parse command and arguments
	parts := strings.Fields(input)
	if len(parts) == 0 {
		return m, tea.Batch(cmds...)
	}
	
	command := strings.TrimPrefix(parts[0], "/")
	args := parts[1:]
	
	m.logger.Debug("Processing slash command", "command", command, "args", args)
	
	// Handle built-in commands
	switch command {
	case "help":
		return m.handleHelpCommand(cmds)
	case "clear":
		return m.handleClearCommand(cmds)
	default:
		// Try to find custom command
		return m.handleCustomCommand(command, args, cmds)
	}
}

// handleHelpCommand shows available commands
func (m Model) handleHelpCommand(cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	helpText := `Available slash commands:
  /help     - Show this help message
  /clear    - Clear the conversation history
  
Custom commands will be available when command files are loaded.`
	
	output := m.renderer.AgentFinal(helpText)
	cmds = append(cmds, printToScrollback(output))
	
	return m, tea.Batch(cmds...)
}

// handleClearCommand clears the conversation
func (m Model) handleClearCommand(cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	// Send tea.ClearScreen command to clear the terminal
	cmds = append(cmds, tea.ClearScreen)
	
	// Show a confirmation message
	output := m.renderer.AgentFinal("Conversation cleared.")
	cmds = append(cmds, printToScrollback(output))
	
	return m, tea.Batch(cmds...)
}

// handleCustomCommand executes custom commands loaded from files
func (m Model) handleCustomCommand(command string, args []string, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	slashCmd, exists := m.commandLoader.GetCommand(command)
	if !exists {
		errorMsg := fmt.Sprintf("Unknown command: /%s\nType /help for available commands.", command)
		output := m.renderer.AgentFinal(errorMsg)
		cmds = append(cmds, printToScrollback(output))
		return m, tea.Batch(cmds...)
	}
	
	m.logger.Debug("Executing custom command", "name", slashCmd.Name, "description", slashCmd.Description)
	
	// Prepare environment for template execution
	env := make(map[string]string)
	for _, envVar := range os.Environ() {
		parts := strings.SplitN(envVar, "=", 2)
		if len(parts) == 2 {
			env[parts[0]] = parts[1]
		}
	}
	
	// Execute the command template
	result, err := m.commandLoader.ExecuteCommand(command, args, env)
	if err != nil {
		errorMsg := fmt.Sprintf("Error executing command /%s: %v", command, err)
		output := m.renderer.AgentFinal(errorMsg)
		cmds = append(cmds, printToScrollback(output))
		return m, tea.Batch(cmds...)
	}
	
	// Show the command result
	output := m.renderer.AgentFinal(result)
	cmds = append(cmds, printToScrollback(output))
	
	return m, tea.Batch(cmds...)
}

// updateCompletions updates command completion suggestions based on current input
func (m *Model) updateCompletions() {
	inputValue := m.input.Value()
	
	// Only show completions for slash commands
	if !strings.HasPrefix(inputValue, "/") {
		m.showingCompletions = false
		m.completionOptions = nil
		m.selectedCompletionIndex = 0
		return
	}
	
	// Extract the command part (without leading /)
	commandPart := strings.TrimPrefix(inputValue, "/")
	
	// Get all available commands
	allCommands := []string{"help", "clear"} // Built-in commands
	allCommands = append(allCommands, m.commandLoader.ListCommands()...) // Custom commands
	
	// Filter commands that match the current input (fuzzy matching)
	var matches []string
	for _, cmd := range allCommands {
		if fuzzyMatch(cmd, commandPart) {
			matches = append(matches, cmd)
		}
	}
	
	// Update completion state
	if len(matches) > 0 && commandPart != "" {
		m.showingCompletions = true
		m.completionOptions = matches
		// Keep selected index in bounds
		if m.selectedCompletionIndex >= len(matches) {
			m.selectedCompletionIndex = 0
		}
	} else {
		m.showingCompletions = false
		m.completionOptions = nil
		m.selectedCompletionIndex = 0
	}
}

// selectCompletion applies the selected completion to the input
func (m *Model) selectCompletion() {
	if !m.showingCompletions || len(m.completionOptions) == 0 {
		return
	}
	
	selectedCommand := m.completionOptions[m.selectedCompletionIndex]
	m.input.textarea.SetValue("/" + selectedCommand + " ")
	m.input.textarea.CursorEnd()
	
	// Fix height after setting value
	m.input.FixDynamicHeight()
	
	// Hide completions after selection
	m.showingCompletions = false
	m.completionOptions = nil
	m.selectedCompletionIndex = 0
}

// fuzzyMatch implements simple fuzzy matching for command completion
func fuzzyMatch(command, pattern string) bool {
	if pattern == "" {
		return true
	}
	
	// Convert to lowercase for case-insensitive matching
	command = strings.ToLower(command)
	pattern = strings.ToLower(pattern)
	
	// First try exact prefix match (highest priority)
	if strings.HasPrefix(command, pattern) {
		return true
	}
	
	// Then try fuzzy matching - all pattern characters must appear in order
	cmdIndex := 0
	for _, patternChar := range pattern {
		found := false
		for cmdIndex < len(command) {
			if rune(command[cmdIndex]) == patternChar {
				found = true
				cmdIndex++
				break
			}
			cmdIndex++
		}
		if !found {
			return false
		}
	}
	
	return true
}

// handleWindowSize handles window resize events
func (m Model) handleWindowSize(msg tea.WindowSizeMsg) Model {
	m.width = msg.Width
	m.height = msg.Height
	m.logger.Debug("Window size changed", "width", msg.Width, "height", msg.Height)
	
	// Update layout - don't set transcript size here, do it in View()
	m.input.SetWidth(msg.Width)
	m.logger.Debug("Window resized", "width", msg.Width, "height", msg.Height)
	
	return m
}

// handleKeyEvent handles all keyboard events
func (m Model) handleKeyEvent(msg tea.KeyMsg, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	// Handle paste events (v1 style - uses msg.Paste flag)
	if msg.Paste {
		m.logger.Debug("Paste event detected", "content", string(msg.Runes))
		
		// Add pasted content to input (preserving newlines)
		currentValue := m.input.Value()
		m.input.textarea.SetValue(currentValue + string(msg.Runes))
		
		// Fix height using the new approach
		m.input.FixDynamicHeight()
		return m, nil
	}
	
	if m.modal.IsOpen() {
		var cmd tea.Cmd
		m.modal, cmd = m.modal.Update(msg)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		return m, tea.Batch(cmds...)
	}

	// DEBUG: Log all key events with details
	m.logger.Debug("Key event received", "key", msg.String(), "type", msg.Type, "runes", string(msg.Runes))

	// Handle permission responses
	if m.state == sessionWaitingPermission {
		return m.handlePermissionKeys(msg, cmds)
	}

	// Handle completion navigation if completions are showing
	if m.showingCompletions {
		return m.handleCompletionKeys(msg, cmds)
	}

	return m.handleGeneralKeys(msg, cmds)
}

// handlePermissionKeys handles key events during permission requests
func (m Model) handlePermissionKeys(msg tea.KeyMsg, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		m.logger.Debug("Ctrl+C pressed, quitting")
		return m, tea.Quit
	case "up", "k":
		if m.selectedPermissionOption > 0 {
			m.selectedPermissionOption--
		}
		return m, nil
	case "down", "j":
		if m.selectedPermissionOption < 1 { // 2 options: 0-1
			m.selectedPermissionOption++
		}
		return m, nil
	case "enter":
		options := []struct{choice, decision string}{
			{"Allow Once", "allow_once"},
			{"No", "deny"},
		}
		selected := options[m.selectedPermissionOption]
		return m.handlePermissionResponse(selected.choice, selected.decision, cmds)
	case "a", "A":
		return m.handlePermissionResponse("Allow Once", "allow_once", cmds)
	case "n", "N":
		return m.handlePermissionResponse("No", "deny", cmds)
	default:
		// Ignore other keys during permission wait
		return m, nil
	}
}

// handleCompletionKeys handles key events during command completion
func (m Model) handleCompletionKeys(msg tea.KeyMsg, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		m.logger.Debug("Ctrl+C pressed, quitting")
		return m, tea.Quit
	case "up":
		if m.selectedCompletionIndex > 0 {
			m.selectedCompletionIndex--
		}
		return m, nil
	case "down":
		if m.selectedCompletionIndex < len(m.completionOptions)-1 {
			m.selectedCompletionIndex++
		}
		return m, nil
	case "tab":
		// Tab selects the completion
		m.selectCompletion()
		m.updateCompletions() // Update completions after selection
		return m, nil
	case "enter":
		// Enter completes to selected command AND executes it
		if m.showingCompletions && len(m.completionOptions) > 0 {
			// Complete to the selected command first
			m.selectCompletion()
			m.updateCompletions()
		}
		
		// Hide completions and execute the (now completed) command
		m.showingCompletions = false
		m.completionOptions = nil
		m.selectedCompletionIndex = 0
		return m.handleEnterKey(cmds)
	case "esc":
		// Escape hides completions
		m.showingCompletions = false
		m.completionOptions = nil
		m.selectedCompletionIndex = 0
		return m, nil
	default:
		// Forward other keys to input and update completions
		var cmd tea.Cmd
		inputPtr, cmd := m.input.Update(msg)
		m.input = *inputPtr
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		m.updateCompletions()
		return m, tea.Batch(cmds...)
	}
}

// handleGeneralKeys handles general key events (main input mode)
func (m Model) handleGeneralKeys(msg tea.KeyMsg, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		m.logger.Debug("Ctrl+C pressed, quitting")
		return m, tea.Quit
	case "enter":
		return m.handleEnterKey(cmds)
	default:
		// Only update input if not thinking
		if m.state != sessionThinking {
			var cmd tea.Cmd
			inputPtr, cmd := m.input.Update(msg)
			m.input = *inputPtr
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
			// Update completions after input changes
			m.updateCompletions()
		}
	}
	return m, tea.Batch(cmds...)
}

// handleEnterKey handles enter key press for message submission
func (m Model) handleEnterKey(cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	// Ignore enter if thinking
	if m.state == sessionThinking {
		m.logger.Debug("Enter pressed but thinking, ignoring")
		return m, nil
	}
	
	inputValue := m.input.Value()
	
	m.logger.Debug("Enter pressed", "input_value", inputValue, "input_length", len(inputValue))
	if inputValue != "" {
		m.logger.Debug("Processing user input", "message", inputValue)
		// Handle user input
		userMsg := inputValue
		(&m.input).Reset()
		m.logger.Debug("Reset input field")
		
		// Print user message to scrollback
		userOutput := m.renderer.User(userMsg)
		m.logger.Debug("About to print user message", "content", userOutput)
		cmds = append(cmds, printToScrollback(userOutput))
		m.logger.Debug("Added user message print command to batch")
		
		// Check if this is a slash command
		if strings.HasPrefix(userMsg, "/") {
			m.logger.Debug("Detected slash command")
			return m.handleSlashCommand(userMsg, cmds)
		}
		
		// Send to agent orchestrator
		m.state = sessionThinking
		m.input.SetThinking(true)
		m.logger.Debug("Set state to thinking")
		
		go func() {
			ctx := context.Background()
			m.logger.Debug("Starting orchestrator processing")
			m.orchestrator.ProcessPrompt(ctx, userMsg)
		}()
	} else {
		m.logger.Debug("Enter pressed but input is empty")
	}
	return m, tea.Batch(cmds...)
}

// handleAgentEvent handles events from the agent orchestrator
func (m Model) handleAgentEvent(msg agent.Event, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	// Handle agent events
	m.logger.Debug("Agent event received", "type", msg.Type, "content", msg.Content)
	switch msg.Type {
	case string(agent.EventTypePlan):
		// Skip plan messages - they're internal orchestration
		m.logger.Debug("Skipping plan message (internal only)")
	case string(agent.EventTypeRunTool):
		cmds = append(cmds, printToScrollback(m.renderer.AgentRun(msg.Content)))
		m.logger.Debug("Printed tool message to scrollback")
	case string(agent.EventTypeObservation):
		cmds = append(cmds, printToScrollback(m.renderer.AgentObservation(msg.Content)))
		m.logger.Debug("Printed observation message to scrollback")
	case string(agent.EventTypeFinal):
		cmds = append(cmds, printToScrollback(m.renderer.AgentFinal(msg.Content)))
		m.state = sessionReady
		m.input.SetThinking(false)
		m.logger.Debug("Printed final message to scrollback, set state to ready")
	case string(agent.EventTypePermissionRequest):
		// Parse the permission request data
		if permReq, ok := msg.Data.(agent.PermissionRequest); ok {
			m.currentPermissionRequest = &permReq
			m.selectedPermissionOption = 0 // Reset to first option
			
			m.state = sessionWaitingPermission
			m.input.SetThinking(false) // Allow input for permission response
			m.logger.Debug("Showing permission request", "tool", permReq.Tool)
		} else {
			m.logger.Error("Failed to parse permission request data")
			cmds = append(cmds, printToScrollback(m.renderer.AgentError("Failed to parse permission request")))
			m.state = sessionReady
			m.input.SetThinking(false)
		}
	case string(agent.EventTypeError):
		cmds = append(cmds, printToScrollback(m.renderer.AgentError(msg.Content)))
		m.state = sessionReady
		m.input.SetThinking(false)
		m.logger.Debug("Printed error message to scrollback, set state to ready")
	}
	// Continue listening for more events
	cmds = append(cmds, m.listenForEvents())
	
	return m, tea.Batch(cmds...)
}
