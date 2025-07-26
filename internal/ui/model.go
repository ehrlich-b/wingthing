package ui

import (
	"context"
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
	transcript TranscriptModel
	input      InputModel
	modal      ModalModel
	theme      Theme
	
	// Agent communication
	events      chan agent.Event
	orchestrator *agent.Orchestrator
}

func NewModel() Model {
	events := make(chan agent.Event, 100)
	
	// Create filesystem
	fs := interfaces.NewOSFileSystem()
	
	// Create tool runner with registered tools
	toolRunner := tools.NewMultiRunner()
	toolRunner.RegisterRunner("bash", tools.NewBashRunner())
	toolRunner.RegisterRunner("edit", tools.NewEditRunner())
	
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
	
	return Model{
		state:        sessionReady,
		transcript:   NewTranscriptModel(),
		input:        NewInputModel(),
		modal:        NewModalModel(),
		theme:        DefaultTheme(),
		events:       events,
		orchestrator: orchestrator,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.input.Init(),
		m.transcript.Init(),
		m.modal.Init(),
		m.listenForEvents(),
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
		
		// Update layout
		m.transcript.SetSize(msg.Width, msg.Height-4) // Reserve space for input
		m.input.SetWidth(msg.Width)
		
	case tea.KeyMsg:
		if m.modal.IsOpen() {
			var cmd tea.Cmd
			m.modal, cmd = m.modal.Update(msg)
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
			return m, tea.Batch(cmds...)
		}

		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "enter":
			if m.input.Value() != "" {
				// Handle user input
				userMsg := m.input.Value()
				m.input.Reset()
				
				// Add to transcript
				m.transcript.AddUserMessage(userMsg)
				
				// Send to agent orchestrator
				m.state = sessionThinking
				m.transcript.AddThinkingMessage()
				
				go func() {
					ctx := context.Background()
					m.orchestrator.ProcessPrompt(ctx, userMsg)
				}()
			}
		default:
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
		}

	case agent.Event:
		// Handle agent events
		switch msg.Type {
		case string(agent.EventTypePlan):
			m.transcript.AddAgentMessage("Plan", msg.Content)
		case string(agent.EventTypeRunTool):
			m.transcript.AddAgentMessage("Running", msg.Content)
		case string(agent.EventTypeObservation):
			m.transcript.AddAgentMessage("Observation", msg.Content)
		case string(agent.EventTypeFinal):
			m.transcript.AddAgentMessage("Result", msg.Content)
			m.state = sessionReady
		case string(agent.EventTypePermissionRequest):
			m.modal.ShowPermissionRequest(msg.Content)
			m.state = sessionWaitingPermission
		}
		// Continue listening for more events
		cmds = append(cmds, m.listenForEvents())
	}

	// Update child models
	var cmd tea.Cmd
	m.transcript, cmd = m.transcript.Update(msg)
	if cmd != nil {
		cmds = append(cmds, cmd)
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

	transcript := m.transcript.View()
	input := m.input.View()

	return lipgloss.JoinVertical(
		lipgloss.Left,
		transcript,
		m.theme.InputBorder.Render(input),
	)
}
