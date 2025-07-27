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
	
	theme := DefaultTheme()
	return Model{
		state:        sessionReady,
		input:        NewInputModel(),
		modal:        NewModalModel(),
		theme:        theme,
		renderer:     NewRenderer(theme),
		events:       events,
		orchestrator: orchestrator,
		logger:       logger,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.input.Init(),
		m.modal.Init(),
		m.listenForEvents(),
		printToScrollback(m.renderer.Welcome()),
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
		m.width = msg.Width
		m.height = msg.Height
		m.logger.Debug("Window size changed", "width", msg.Width, "height", msg.Height)
		
		// Update layout - don't set transcript size here, do it in View()
		m.input.SetWidth(msg.Width)
		m.logger.Debug("Window resized", "width", msg.Width, "height", msg.Height)
		
	case tea.KeyMsg:
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

		switch msg.String() {
		case "ctrl+c":
			m.logger.Debug("Ctrl+C pressed, quitting")
			return m, tea.Quit
		case "enter":
			// Ignore enter if thinking
			if m.state == sessionThinking {
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
		default:
			// Only update input if not thinking
			if m.state != sessionThinking {
				var cmd tea.Cmd
				m.input, cmd = m.input.Update(msg)
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		}

	case agent.Event:
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
	}

	// No transcript to update since we print directly to scrollback

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
